package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dennisvink/yolomancer/internal/provider"
)

func TestProfileStartupAssumesBedrockRole(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(root, "config"))
	path := filepath.Join(root, "credentials")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)
	t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENTKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	if err := os.WriteFile(path, []byte("[build]\naws_access_key_id=PROFILEKEY\naws_secret_access_key=profile-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("Action") != "AssumeRole" || r.Form.Get("RoleArn") != provider.BedrockRoleARN || r.Form.Get("RoleSessionName") != "yolomancer" {
			t.Errorf("unexpected STS request: %v", r.Form)
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=PROFILEKEY/") {
			t.Error("STS did not use selected profile")
		}
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>BEDROCKKEY</AccessKeyId><SecretAccessKey>bedrock-secret</SecretAccessKey><SessionToken>bedrock-token</SessionToken><Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::183305290766:assumed-role/ProsusBedrockAccess/yolomancer</Arn><AssumedRoleId>role:yolomancer</AssumedRoleId></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	cfg, err := loadConfig(t.Context(), options{profile: "build"})
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.BedrockCredentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "BEDROCKKEY" || calls.Load() != 1 {
		t.Fatalf("startup credentials: %v, %d calls", err, calls.Load())
	}
	if cfg.AWSProfile == nil || *cfg.AWSProfile != "build" || cfg.AWSAccessKeyID != nil {
		t.Fatal("startup changed the tool identity")
	}
}

func TestProfileIsGlobal(t *testing.T) {
	for _, args := range [][]string{
		{"--profile", "build"},
		{"--profile", "build", "run", "hello"},
		{"run", "hello", "--profile", "build"},
		{"resume", "--profile", "build", "session-id"},
	} {
		o, err := parse(args)
		if err != nil || o.profile != "build" {
			t.Fatalf("parse %v: %#v, %v", args, o, err)
		}
	}
	for _, args := range [][]string{{"--profile"}, {"--profile", ""}, {"--profile", "--debug"}} {
		if _, err := parse(args); err == nil {
			t.Fatalf("accepted missing profile: %v", args)
		}
	}
}

func TestParseGlobalFlagsAroundSubcommand(t *testing.T) {
	o, err := parse([]string{"--debug", "run", "hello world", "--local", "--no-alt-screen"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.debug || !o.local || !o.noAlt || o.command != "run" || len(o.args) != 1 || o.args[0] != "hello world" {
		t.Fatalf("%#v", o)
	}
}
func TestParseLoginProfileAndLoginFlags(t *testing.T) {
	o, err := parse([]string{"login", "--profile", "demo", "--aws-region", "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	if o.command != "login" || o.profile != "demo" || len(o.args) != 2 {
		t.Fatalf("%#v", o)
	}
}

func TestParseSubcommandHelpAndRejectsUnknownFlag(t *testing.T) {
	o, err := parse([]string{"login", "--help"})
	if err != nil || o.command != "help" || o.helpTarget != "login" {
		t.Fatalf("%#v %v", o, err)
	}
	if _, err := parse([]string{"--bogus"}); err == nil {
		t.Fatal("unknown option was accepted")
	}
}

func TestLogoutRejectsArguments(t *testing.T) {
	err := run(t.Context(), []string{"logout", "extra"})
	if err == nil {
		t.Fatal("logout accepted a positional argument")
	}
}
