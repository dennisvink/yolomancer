package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
)

func TestRegisterArguments(t *testing.T) {
	code := "ABCDEF0123"
	for _, args := range [][]string{{"register", code}, {"register", "--endpoint", "https://vendor.yolomancer.com/claim", code}} {
		o, err := parse(args)
		if err != nil {
			t.Fatal(err)
		}
		r, err := registrationOptions(o)
		if err != nil || r.Code != code {
			t.Fatalf("invalid registration options: %v", err)
		}
	}
	for _, args := range [][]string{{"register"}, {"register", code, "extra"}, {"register", code, "--profile", "admin"}, {"register", code, "--name"}, {"register", code, "--name", "Dennis"}} {
		o, err := parse(args)
		if err == nil {
			_, err = registrationOptions(o)
		}
		if err == nil {
			t.Fatal("accepted invalid arguments")
		}
	}
}

func TestRegisteredStartupSeparatesToolAndBedrockKeys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(root, "missing"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(root, "missing-creds"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient")
	key, secret, region := "REGISTEREDKEY", "registered-secret", "eu-west-1"
	cfg := &model.Config{AWSAccessKeyID: &key, AWSSecretAccessKey: &secret, AWSRegion: &region, Registration: &model.Registration{AccountUser: "prosus-user-100"}}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("RoleArn") != provider.BedrockRoleARN || !strings.Contains(r.Header.Get("Authorization"), "Credential=REGISTEREDKEY/") {
			t.Error("wrong role or source identity")
		}
		fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>BEDROCKKEY</AccessKeyId><SecretAccessKey>bedrock-secret</SecretAccessKey><SessionToken>bedrock-token</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	loaded, err := loadConfig(t.Context(), options{})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := loaded.BedrockCredentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "BEDROCKKEY" {
		t.Fatal("role credentials missing")
	}
	env := strings.Join(provider.ToolEnvironment(loaded), "\n")
	if !strings.Contains(env, "AWS_ACCESS_KEY_ID=REGISTEREDKEY") || strings.Contains(env, "BEDROCKKEY") {
		t.Fatal("tool credential separation failed")
	}
}
