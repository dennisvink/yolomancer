package gitremote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPKCS8(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YOLOMANCER_GIT_KEY", path)
	client, err := Load()
	if err != nil || !key.Equal(client.Key) {
		t.Fatalf("PKCS8 identity: %v", err)
	}
}

func TestLogical(t *testing.T) {
	for _, url := range []string{"yolomancer://prosus/prosus-user-001/workshop", "yolomancer::yolomancer://prosus/prosus-user-001/workshop"} {
		got, err := Logical(url)
		if err != nil || got != "prosus/prosus-user-001/workshop" {
			t.Fatalf("%s: %q %v", url, got, err)
		}
	}
	for _, url := range []string{"s3://bucket/repo", "yolomancer://other/prosus-user-001/repo", "yolomancer://prosus/a/b", "yolomancer://prosus/prosus-user-001/..", "yolomancer://evil@prosus/prosus-user-001/repo", "yolomancer://prosus/prosus-user-001/repo?write=true", "yolomancer://prosus/prosus-user-001/a%2Fb"} {
		if _, err := Logical(url); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
}
func TestSignedCall(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		b, _ := json.Marshal([]any{"yolomancer-git-v1", request.Payload, request.PublicKey, request.Timestamp, request.Nonce})
		sig, _ := base64.StdEncoding.DecodeString(request.Signature)
		if !ed25519.Verify(key.Public().(ed25519.PublicKey), b, sig) {
			t.Error("invalid request signature")
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer endpoint.Close()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := (Client{Endpoint: endpoint.URL, Key: key}).Call(context.Background(), map[string]string{"action": "whoami"}, &result); err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestRejectRedirect(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.com", 302) }))
	defer endpoint.Close()
	var result any
	if err := (Client{Endpoint: endpoint.URL, Key: key}).Call(context.Background(), map[string]string{"action": "whoami"}, &result); err == nil {
		t.Fatal("followed controller redirect")
	}
}
