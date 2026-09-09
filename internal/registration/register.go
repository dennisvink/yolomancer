// Package registration redeems workshop access codes without involving the LLM.
package registration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
)

const DefaultEndpoint = "https://vendor.yolomancer.com/claim"

var codePattern = regexp.MustCompile(`^[ABCDEF012345678]{10}$`)
var keyPattern = regexp.MustCompile(`^AKIA[A-Z0-9]{16}$`)
var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
var userPattern = regexp.MustCompile(`^prosus-user-(0[0-9]{2}|100)$`)

type Options struct {
	Code, Endpoint string
	Client         *http.Client
}
type identity struct {
	ID   string `json:"id"`
	Seed string `json:"seed"`
}

func (i identity) key() (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(i.Seed)
	if err != nil || len(seed) != ed25519.SeedSize || !strings.HasPrefix(i.ID, "yolomancer-") {
		return nil, errors.New("invalid saved registration identity; do not delete it if an account is already claimed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

type request struct {
	Code      string `json:"code"`
	PublicKey string `json:"public_key"`
	Timestamp int64  `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}
type Result struct {
	Version             int    `json:"version"`
	AssignmentID        string `json:"assignment_id"`
	PublicKey           string `json:"public_key"`
	AccountUser         string `json:"account_user"`
	RedemptionExpiresAt int64  `json:"redemption_expires_at"`
	Credentials         struct {
		AccessKeyID     string `json:"aws_access_key_id"`
		SecretAccessKey string `json:"aws_secret_access_key"`
		Region          string `json:"aws_region"`
	} `json:"credentials"`
}

func loadIdentity(dir string) (identity, error) {
	path := filepath.Join(dir, "registration-identity.json")
	read := func() (identity, error) {
		var id identity
		info, err := os.Lstat(path)
		if err != nil {
			return id, err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
			return id, errors.New("registration identity must be a regular owner-only (0600) file")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return id, err
		}
		if json.Unmarshal(b, &id) != nil {
			return id, errors.New("invalid registration identity file")
		}
		_, err = id.key()
		return id, err
	}
	if id, err := read(); !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return identity{}, err
	}
	id := identity{ID: "yolomancer-" + uuid.NewString(), Seed: base64.StdEncoding.EncodeToString(key.Seed())}
	b, _ := json.Marshal(id)
	if err := config.WritePrivateFile(path, b, true); err != nil {
		if errors.Is(err, os.ErrExist) {
			return read()
		}
		return identity{}, err
	}
	return id, nil
}

func readConfig(path string) (map[string]any, error) {
	m := map[string]any{}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config must be a regular file, not a symlink")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if toml.Unmarshal(b, &m) != nil {
		return nil, errors.New("existing config is invalid; fix it before claiming credentials")
	}
	return m, nil
}

// signingMessage matches the vendor JSON array; every string field is ASCII.
func signingMessage(r request) []byte {
	b, _ := json.Marshal([]any{"yolomancer-register-v2", r.Code, r.PublicKey, r.Timestamp, r.Nonce})
	return b
}

func Register(ctx context.Context, o Options) (*Result, error) {
	if !codePattern.MatchString(o.Code) {
		return nil, errors.New("access code must be exactly 10 characters from ABCDEF012345678")
	}
	if o.Endpoint == "" {
		o.Endpoint = DefaultEndpoint
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("dispenser endpoint must be HTTPS, without credentials, query or fragment")
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	if _, err := readConfig(path); err != nil {
		return nil, err
	}
	id, err := loadIdentity(dir)
	if err != nil {
		return nil, err
	}
	key, err := id.key()
	if err != nil {
		return nil, err
	}
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	client := http.Client{Timeout: 15 * time.Second}
	if o.Client != nil {
		client = *o.Client
		if client.Timeout == 0 {
			client.Timeout = 15 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var result *Result
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		r := request{Code: o.Code, PublicKey: pub, Timestamp: time.Now().Unix(), Nonce: hex.EncodeToString(nonce)}
		r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, signingMessage(r)))
		payload, _ := json.Marshal(r)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, errors.New("could not construct claim request")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, sendErr := client.Do(req)
		retry := sendErr != nil
		if sendErr == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32769))
			resp.Body.Close()
			if resp.StatusCode == 200 {
				if readErr != nil {
					retry = true
				} else {
					var value Result
					sum := sha256.Sum256([]byte(o.Code))
					if len(body) > 32768 || json.Unmarshal(body, &value) != nil || value.Version != 2 || value.PublicKey != pub || value.AssignmentID != hex.EncodeToString(sum[:]) || !userPattern.MatchString(value.AccountUser) || !keyPattern.MatchString(value.Credentials.AccessKeyID) || len(value.Credentials.SecretAccessKey) != 40 || !regionPattern.MatchString(value.Credentials.Region) || value.RedemptionExpiresAt <= time.Now().Unix() {
						return nil, errors.New("dispenser returned an invalid or mismatched assignment; existing config was not changed")
					}
					result = &value
				}
			} else if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				retry = true
			} else if resp.StatusCode == 403 {
				return nil, errors.New("code invalid, expired, or claimed by another identity; check your clock and retain registration-identity.json for retries")
			} else {
				return nil, fmt.Errorf("dispenser rejected registration (HTTP %d); redirects are not followed", resp.StatusCode)
			}
		}
		if result != nil {
			break
		}
		if !retry || attempt == 3 {
			return nil, errors.New("could not complete registration; retry with the same code and saved identity")
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	// Reload immediately before merging so preferences changed while the request
	// was in flight are preserved. Never print the response or persist the code.
	m, err := readConfig(path)
	if err != nil {
		return nil, err
	}
	delete(m, "aws_profile")
	delete(m, "aws_session_token")
	m["aws_access_key_id"] = result.Credentials.AccessKeyID
	m["aws_secret_access_key"] = result.Credentials.SecretAccessKey
	m["aws_region"] = result.Credentials.Region
	if _, ok := m["installation_id"]; !ok {
		m["installation_id"] = uuid.NewString()
	}
	m["registration"] = model.Registration{AssignmentID: result.AssignmentID, Identity: id.ID, PublicKey: pub, AccountUser: result.AccountUser, Endpoint: o.Endpoint}
	b, err := toml.Marshal(m)
	if err != nil {
		return nil, errors.New("could not serialize config")
	}
	if err := config.WritePrivateFile(path, b, false); err != nil {
		return nil, fmt.Errorf("credentials were received but could not be saved; retry with the same code and identity: %w", err)
	}
	return result, nil
}
