package registration

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dennisvink/yolomancer/internal/config"
)

func assignment(r request) Result {
	sum := sha256.Sum256([]byte(r.Code))
	v := Result{Version: 2, AssignmentID: hex.EncodeToString(sum[:]), PublicKey: r.PublicKey, AccountUser: "prosus-user-100", RedemptionExpiresAt: time.Now().Add(time.Hour).Unix()}
	v.Credentials.AccessKeyID = "AKIA" + strings.Repeat("A", 16)
	v.Credentials.SecretAccessKey = strings.Repeat("s", 40)
	v.Credentials.Region = "eu-west-1"
	return v
}

func TestRegisterPersistsIdentityRetriesAndPreservesSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, _ := config.Dir()
	file, _ := config.File()
	original := "aws_profile = 'old'\naws_session_token = 'stale'\nsandbox_mode = 'workspace-write'\ncustom_future_setting = 'keep'\n"
	if err := config.WritePrivateFile(file, []byte(original), false); err != nil {
		t.Fatal(err)
	}
	var requests []request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body request
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "registration-identity.json")); err != nil {
			t.Error("identity was not persisted before request")
		}
		pub, _ := base64.StdEncoding.DecodeString(body.PublicKey)
		sig, _ := base64.StdEncoding.DecodeString(body.Signature)
		if !ed25519.Verify(pub, signingMessage(body), sig) {
			t.Error("invalid request signature")
		}
		requests = append(requests, body)
		if len(requests) == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(assignment(body))
	}))
	defer server.Close()
	o := Options{Code: "ABCDEF0123", Endpoint: server.URL + "/claim", Client: server.Client()}
	if _, err := Register(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("got %d requests", len(requests))
	}
	for i := 1; i < len(requests); i++ {
		if requests[i].PublicKey != requests[0].PublicKey || requests[i].Nonce == requests[i-1].Nonce {
			t.Fatal("identity changed or nonce reused")
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Registration == nil || cfg.Registration.AccountUser != "prosus-user-100" || cfg.AWSProfile != nil || cfg.AWSSessionToken != nil {
		t.Fatal("incorrect registration config")
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), "custom_future_setting = 'keep'") || strings.Contains(string(raw), o.Code) {
		t.Fatal("lost setting or persisted code")
	}
	for _, p := range []string{file, filepath.Join(dir, "registration-identity.json")} {
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Fatal("private file permissions")
		}
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("private directory permissions")
	}
}

func TestSigningMessageMatchesJavaScript(t *testing.T) {
	r := request{Code: "code", PublicKey: "pub", Timestamp: 123, Nonce: "nonce"}
	want := `["yolomancer-register-v2","code","pub",123,"nonce"]`
	if string(signingMessage(r)) != want {
		t.Fatalf("signing bytes differ: %q", signingMessage(r))
	}
}

func TestRejectedClaimsDoNotChangeConfigOrLeakResponse(t *testing.T) {
	for _, kind := range []string{"forbidden", "mismatch", "oversized", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			file, _ := config.File()
			original := []byte("aws_profile = 'preserve'\n")
			if err := config.WritePrivateFile(file, original, false); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body request
				json.NewDecoder(r.Body).Decode(&body)
				switch kind {
				case "forbidden":
					w.WriteHeader(403)
					w.Write([]byte("SERVER_SECRET"))
				case "redirect":
					w.Header().Set("Location", "https://invalid.example/steal")
					w.WriteHeader(307)
				case "oversized":
					w.Write([]byte(strings.Repeat("SERVER_SECRET", 4000)))
				case "mismatch":
					v := assignment(body)
					v.PublicKey = "other"
					json.NewEncoder(w).Encode(v)
				}
			}))
			defer server.Close()
			_, err := Register(t.Context(), Options{Code: "ABCDEF0123", Endpoint: server.URL, Client: server.Client()})
			if err == nil || strings.Contains(err.Error(), "SERVER_SECRET") {
				t.Fatal("missing or unsafe error")
			}
			raw, _ := os.ReadFile(file)
			if string(raw) != string(original) {
				t.Fatal("config changed after failure")
			}
		})
	}
}

func TestConcurrentIdentityCreationReusesOneKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "identity")
	var wg sync.WaitGroup
	ids := make(chan identity, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := loadIdentity(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	var first identity
	for id := range ids {
		if first.ID == "" {
			first = id
		}
		if id != first {
			t.Error("concurrent startup replaced identity")
		}
	}
}

func TestRegisterValidationBeforeNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, o := range []Options{
		{Code: "bad"},
		{Code: strings.Repeat("A", 32)},
		{Code: strings.Repeat("a", 10)},
		{Code: strings.Repeat("9", 10)},
		{Code: strings.Repeat("G", 10)},
		{Code: "ABCDEF0123", Endpoint: "http://example.com/claim"},
		{Code: "ABCDEF0123", Endpoint: "https://example.com/claim?code=secret"},
	} {
		if _, err := Register(t.Context(), o); err == nil {
			t.Fatal("accepted invalid options")
		}
	}
	dir, _ := config.Dir()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid options wrote local files")
	}
}
