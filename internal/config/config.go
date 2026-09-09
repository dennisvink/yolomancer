package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".yolomancer"), nil
}

func File() (string, error)        { d, err := Dir(); return filepath.Join(d, "config.toml"), err }
func SessionsDir() (string, error) { d, err := Dir(); return filepath.Join(d, "sessions"), err }

func Save(cfg *model.Config) error {
	file, err := File()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("serialize config toml: %w", err)
	}
	if err := WritePrivateFile(file, b, false); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	return nil
}

func Load() (*model.Config, error) {
	return LoadForProfile("")
}

// LoadForProfile allows starting with a profile before a login config exists.
func LoadForProfile(profile string) (*model.Config, error) {
	file, err := File()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) && profile != "" {
		b, err = nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("missing config. run: yolomancer login --profile <aws-profile> (%s): %w", file, err)
	}
	var cfg model.Config
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse ~/.yolomancer/config.toml: %w", err)
	}
	if profile != "" {
		cfg.AWSProfile = &profile
		cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, cfg.AWSSessionToken = nil, nil, nil
	}
	if cfg.ProjectProfiles == nil {
		cfg.ProjectProfiles = map[string]model.ProjectTrustProfile{}
	}
	if strings.TrimSpace(cfg.APIKey) == "" && blank(cfg.AWSProfile) && (blank(cfg.AWSAccessKeyID) || blank(cfg.AWSSecretAccessKey)) {
		return nil, errors.New("configure AWS Bedrock credentials with `yolomancer login --profile <aws-profile>` or `yolomancer login --aws-access-key-id <id> --aws-secret-access-key <secret>`")
	}
	return &cfg, nil
}

func LoadOrBootstrap() (*model.Config, error) {
	file, fileErr := File()
	if fileErr == nil {
		if _, statErr := os.Stat(file); errors.Is(statErr, os.ErrNotExist) {
			fmt.Printf("No config found at %s.\n", file)
			return nil, errors.New("run: yolomancer login --profile <aws-profile>")
		}
	}
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	if blank(cfg.InstallationID) {
		id := uuid.NewString()
		cfg.InstallationID = &id
		if err := Save(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func Remove() (bool, error) {
	f, err := File()
	if err != nil {
		return false, err
	}
	err = os.Remove(f)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func blank(v *string) bool { return v == nil || strings.TrimSpace(*v) == "" }

func String(v *string, fallback string) string {
	if blank(v) {
		return fallback
	}
	return strings.TrimSpace(*v)
}

func BedrockModel(cfg *model.Config) string {
	v := String(cfg.BedrockModel, model.DefaultBedrock)
	return strings.TrimPrefix(v, "bedrock:")
}

func Region(cfg *model.Config) string { return String(cfg.AWSRegion, "us-east-1") }

func Provider(cfg *model.Config) string {
	return "opus"
}
