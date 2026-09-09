package provider

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/dennisvink/yolomancer/internal/model"
)

const BedrockRoleARN = "arn:aws:iam::183305290766:role/ProsusBedrockAccess"

func bedrockRoleCredentials(client stscreds.AssumeRoleAPIClient) aws.CredentialsProvider {
	return aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(client, BedrockRoleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "yolomancer"
	}))
}

// PrepareBedrock resolves Bedrock credentials without changing the tool identity.
// The cache refreshes temporary credentials when they expire.
func PrepareBedrock(ctx context.Context, cfg *model.Config) error {
	if cfg.BedrockCredentials == nil {
		base, err := awsConfig(ctx, cfg)
		if err != nil {
			return err
		}
		cfg.BedrockCredentials = base.Credentials
		if cfg.Registration != nil || cfg.AWSProfile != nil && strings.TrimSpace(*cfg.AWSProfile) != "" {
			cfg.BedrockCredentials = bedrockRoleCredentials(sts.NewFromConfig(base))
		}
	}
	if _, err := cfg.BedrockCredentials.Retrieve(ctx); err != nil {
		return fmt.Errorf("prepare Bedrock credentials (profiles and registered accounts use %s): %w", BedrockRoleARN, err)
	}
	return nil
}

// ToolEnvironment keeps Bedrock role credentials out of child processes.
// An explicit profile takes precedence over ambient or saved static credentials.
func ToolEnvironment(cfg *model.Config) []string {
	env := os.Environ()
	set := func(key, value string) {
		filtered := env[:0]
		for _, item := range env {
			if !strings.HasPrefix(item, key+"=") {
				filtered = append(filtered, item)
			}
		}
		env = filtered
		if value != "" {
			env = append(env, key+"="+value)
		}
	}
	if cfg.AWSProfile != nil && strings.TrimSpace(*cfg.AWSProfile) != "" {
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN", "AWS_DEFAULT_PROFILE"} {
			set(key, "")
		}
		set("AWS_PROFILE", strings.TrimSpace(*cfg.AWSProfile))
	} else if cfg.AWSAccessKeyID != nil && cfg.AWSSecretAccessKey != nil {
		set("AWS_PROFILE", "")
		set("AWS_DEFAULT_PROFILE", "")
		set("AWS_ACCESS_KEY_ID", *cfg.AWSAccessKeyID)
		set("AWS_SECRET_ACCESS_KEY", *cfg.AWSSecretAccessKey)
		set("AWS_SESSION_TOKEN", "")
		set("AWS_SECURITY_TOKEN", "")
		if cfg.AWSSessionToken != nil {
			set("AWS_SESSION_TOKEN", *cfg.AWSSessionToken)
		}
	}
	if cfg.AWSRegion != nil && *cfg.AWSRegion != "" {
		set("AWS_REGION", *cfg.AWSRegion)
		set("AWS_DEFAULT_REGION", *cfg.AWSRegion)
	}
	return env
}
