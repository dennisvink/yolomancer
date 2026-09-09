package provider

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/dennisvink/yolomancer/internal/model"
)

type roleClient struct {
	t     *testing.T
	calls int
}

func (c *roleClient) AssumeRole(_ context.Context, in *sts.AssumeRoleInput, _ ...func(*sts.Options)) (*sts.AssumeRoleOutput, error) {
	c.calls++
	if aws.ToString(in.RoleArn) != BedrockRoleARN || aws.ToString(in.RoleSessionName) != "yolomancer" {
		c.t.Fatalf("unexpected role request: %#v", in)
	}
	return &sts.AssumeRoleOutput{Credentials: &types.Credentials{
		AccessKeyId: aws.String("BEDROCKKEY"), SecretAccessKey: aws.String("bedrock-secret"),
		SessionToken: aws.String("bedrock-token"), Expiration: aws.Time(time.Now().Add(time.Hour)),
	}}, nil
}

func TestBedrockRoleCacheAndToolIdentityAreSeparate(t *testing.T) {
	root := t.TempDir()
	credentials := filepath.Join(root, "credentials")
	if err := os.WriteFile(credentials, []byte("[build]\naws_access_key_id=PROFILEKEY\naws_secret_access_key=profile-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentials)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(root, "missing-config"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENTKEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_SESSION_TOKEN", "ambient-token")
	profile, savedKey, savedSecret := "build", "SAVEDKEY", "saved-secret"
	client := &roleClient{t: t}
	cache := bedrockRoleCredentials(client).(*aws.CredentialsCache)
	cfg := &model.Config{AWSProfile: &profile, AWSAccessKeyID: &savedKey, AWSSecretAccessKey: &savedSecret, BedrockCredentials: cache}
	transport := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		want := "Credential=PROFILEKEY/"
		if strings.HasPrefix(r.URL.Host, "bedrock-runtime.") {
			want = "Credential=BEDROCKKEY/"
		}
		if !strings.Contains(r.Header.Get("Authorization"), want) {
			t.Errorf("wrong identity for %s: %s", r.URL.Host, r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	b := &Bedrock{Config: cfg, Client: transport}
	for i := 0; i < 2; i++ {
		resp, err := b.signedRequest(t.Context(), []byte("{}"), "converse")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if client.calls != 1 {
		t.Fatalf("role was not cached: %d calls", client.calls)
	}
	cache.Invalidate()
	if err := PrepareBedrock(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 {
		t.Fatal("invalidated role credentials were not refreshed")
	}
	if _, err := SignedAWSRequest(t.Context(), cfg, "sts", "POST", "https://sts.us-east-1.amazonaws.com/", "", nil, "", transport); err != nil {
		t.Fatal(err)
	}
	env := ToolEnvironment(cfg)
	seen := map[string]string{}
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		seen[key] = value
	}
	if seen["AWS_PROFILE"] != "build" {
		t.Fatal("tool profile was lost")
	}
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if _, ok := seen[key]; ok {
			t.Errorf("tool environment retained %s", key)
		}
	}
}
