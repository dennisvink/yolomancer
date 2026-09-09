package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func SignedAWSRequest(ctx context.Context, cfg *model.Config, service, method, rawURL, body string, headers map[string]string, region string, client *http.Client) (map[string]any, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("AWS request URL must be an absolute https URL")
	}
	if strings.TrimSpace(service) == "" {
		return nil, fmt.Errorf("missing required string argument: service")
	}
	if region == "" {
		region = config.Region(cfg)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), rawURL, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	awsCfg, err := awsConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(body))
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), service, region, time.Now()); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	result := map[string]any{"ok": resp.StatusCode >= 200 && resp.StatusCode < 300, "status": resp.StatusCode, "body": string(raw)}
	responseHeaders := map[string]string{}
	for key, values := range resp.Header {
		responseHeaders[key] = strings.Join(values, ", ")
	}
	result["headers"] = responseHeaders
	result["text"] = string(raw)
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		result["json"] = parsed
	}
	return result, nil
}
