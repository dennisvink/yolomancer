package config

import (
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/dennisvink/yolomancer/internal/model"
)

func TestProfileStartupAndLegacyRoleConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadForProfile("build")
	if err != nil || cfg.AWSProfile == nil || *cfg.AWSProfile != "build" {
		t.Fatalf("profile startup without config: %#v, %v", cfg, err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	file, _ := File()
	legacy := "aws_profile = 'old'\naws_access_key_id = 'saved'\naws_secret_access_key = 'secret'\naws_bridge_role_arn = 'arn:aws:iam::123456789012:role/old'\n"
	if err := os.WriteFile(file, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadForProfile("build")
	if err != nil {
		t.Fatal(err)
	}
	if *cfg.AWSProfile != "build" || cfg.AWSAccessKeyID != nil || cfg.AWSSecretAccessKey != nil {
		t.Fatal("profile did not override saved keys")
	}
	cfg.BedrockCredentials = credentials.NewStaticCredentialsProvider("BEDROCKKEY", "BEDROCKSECRET", "BEDROCKTOKEN")
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "BEDROCK") || strings.Contains(string(raw), "aws_bridge_role_arn") {
		t.Fatal("saved runtime credentials or retired role")
	}
}

func TestConfigRoundTripPreservesKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	profile, base, provider := "workshop", "https://example.test/v1", "opus"
	effect := model.AllowAlways
	cfg := &model.Config{BaseURL: &base, AWSProfile: &profile, ModelProvider: &provider, ProjectProfiles: map[string]model.ProjectTrustProfile{"/workspace": {}}, CommandApprovalRules: []model.CommandApprovalRule{{Prefix: []string{"git", "status"}, Effect: &effect}}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	file, _ := File()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"base_url", "aws_profile", "model_provider", "project_profiles", "command_approval_rules", "allow_always"} {
		if !strings.Contains(text, key) {
			t.Errorf("serialized TOML lacks %q:\n%s", key, text)
		}
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.AWSProfile == nil || *got.AWSProfile != profile || len(got.CommandApprovalRules) != 1 || *got.CommandApprovalRules[0].Effect != effect {
		t.Fatalf("round trip mismatch: %#v", got)
	}
}

func TestLoadNestedEmptyArrays(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	file, _ := File()
	if err := os.MkdirAll(strings.TrimSuffix(file, "/config.toml"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := `api_key = ""
aws_profile = "workshop"
writable_roots = []
command_approval_rules = []
network_approval_rules = []

[project_profiles."/workspace"]
read_roots = []
writable_roots = []
network_approval_rules = []
`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AWSProfile == nil || *cfg.AWSProfile != "workshop" || len(cfg.ProjectProfiles) != 1 {
		t.Fatalf("%#v", cfg)
	}
}
