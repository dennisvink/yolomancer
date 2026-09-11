// Package gitremote integrates native Git with the remote Yolomancer controller.
package gitremote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bucketgit/bgit/repository"
	s3store "github.com/bucketgit/bgit/store/s3"
	"github.com/bucketgit/bgit/transport"
	"golang.org/x/crypto/ssh"
)

type Client struct {
	Endpoint string
	Key      ed25519.PrivateKey
}
type Request struct {
	Payload   string `json:"payload"`
	PublicKey string `json:"publicKey"`
	Timestamp int64  `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

func (c Client) Call(ctx context.Context, payload any, result any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	r := Request{Payload: string(b), PublicKey: base64.StdEncoding.EncodeToString(c.Key.Public().(ed25519.PublicKey)), Timestamp: time.Now().Unix(), Nonce: hex.EncodeToString(nonce)}
	signed, _ := json.Marshal([]any{"yolomancer-git-v1", r.Payload, r.PublicKey, r.Timestamp, r.Nonce})
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(c.Key, signed))
	body, _ := json.Marshal(r)
	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("controller redirects are not allowed") }}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 6<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &e)
		return fmt.Errorf("Git controller: %s (HTTP %d)", e.Error, response.StatusCode)
	}
	return json.Unmarshal(data, result)
}
func Load() (Client, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Client{}, err
	}
	endpoint := os.Getenv("YOLOMANCER_GIT_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://lab.yolomancer.com/git/api"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Client{}, errors.New("Git controller must be an HTTPS URL")
	}
	path := os.Getenv("YOLOMANCER_GIT_KEY")
	if path == "" {
		path = filepath.Join(home, ".ssh", "id_ed25519")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Client{}, fmt.Errorf("read lab SSH identity: %w", err)
	}
	key, err := ssh.ParseRawPrivateKey(b)
	if err != nil {
		return Client{}, err
	}
	var ed ed25519.PrivateKey
	switch k := key.(type) {
	case *ed25519.PrivateKey:
		ed = *k
	case ed25519.PrivateKey:
		ed = k
	default:
		return Client{}, errors.New("Git requires the lab Ed25519 identity")
	}
	return Client{Endpoint: endpoint, Key: ed}, nil
}
func Logical(address string) (string, error) {
	u, err := url.Parse(strings.TrimPrefix(address, "yolomancer::"))
	if err != nil || u.Scheme != "yolomancer" || u.Host != "prosus" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("expected yolomancer://prosus/prosus-user-xxx/repository")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !regexp.MustCompile(`^prosus-user-[0-9]{3}$`).MatchString(parts[0]) || !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`).MatchString(parts[1]) {
		return "", errors.New("invalid repository URL")
	}
	return "prosus/" + strings.Join(parts, "/"), nil
}

type capability struct {
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	Region    string `json:"region"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Token     string `json:"token"`
}

func (c Client) Remote(ctx context.Context, args []string) error {
	resolver := transport.ResolverFunc(func(ctx context.Context, address, service string, input io.Reader, output io.Writer) error {
		logical, err := Logical(address)
		if err != nil {
			return err
		}
		write := service == transport.ReceivePackService
		var cap capability
		err = c.Call(ctx, map[string]any{"action": "capability", "repo": logical, "write": write}, &cap)
		if err != nil {
			return err
		}
		s3 := awss3.New(awss3.Options{Region: cap.Region, Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cap.AccessKey, cap.SecretKey, cap.Token))})
		store, err := s3store.New(s3, cap.Bucket, cap.Prefix)
		if err != nil {
			return err
		}
		repo := repository.Open(store, store)
		refs, err := store.ListRefs(ctx)
		if err != nil {
			return err
		}
		if head := refs["refs/heads/main"]; head != "" {
			refs["HEAD"] = head
		}
		caps := transport.UploadPackCapabilities()
		if write {
			caps = transport.ReceivePackCapabilities()
			filtered := transport.Capabilities{}
			for _, v := range caps {
				if v != "atomic" {
					filtered = append(filtered, v)
				}
			}
			caps = filtered
		}
		// An empty receive-pack advertisement still needs capabilities.
		if write && len(refs) == 0 {
			if err = transport.WriteString(output, strings.Repeat("0", 40)+" capabilities^{}\x00"+caps.String()+"\n"); err != nil {
				return err
			}
			err = transport.WriteFlush(output)
		} else {
			err = transport.WriteAdvertisedRefs(output, service, refs, caps)
		}
		if err != nil {
			return err
		}
		if write {
			return transport.ServeReceivePack(ctx, repo, store, input, output)
		}
		return transport.ServeUploadPack(ctx, repo, input, output)
	})
	return transport.ServeRemoteHelper(ctx, resolver, args, os.Stdin, os.Stdout, os.Stderr)
}
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yolomancer git <repos|create NAME|invite USER|revoke USER|whoami|enroll ID|remote-helper ...|install>")
	}
	if args[0] == "install" {
		return install()
	}
	c, err := Load()
	if err != nil {
		return err
	}
	if args[0] == "remote-helper" {
		return c.Remote(ctx, args[1:])
	}
	p := map[string]any{"action": args[0]}
	switch args[0] {
	case "whoami", "repos":
		if len(args) != 1 {
			return errors.New("unexpected arguments")
		}
	case "create", "enroll":
		if len(args) != 2 {
			return errors.New("provide a repository name or enrollment ID")
		}
		if args[0] == "create" {
			p["name"] = args[1]
		} else {
			p["id"] = args[1]
		}
	case "invite", "revoke":
		if len(args) != 2 {
			return errors.New("provide a username")
		}
		cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
		b, e := cmd.Output()
		if e != nil {
			return errors.New("current checkout needs a Yolomancer origin")
		}
		logical, e := Logical(strings.TrimSpace(string(b)))
		if e != nil {
			return e
		}
		p["repo"] = logical
		p["user"] = args[1]
	default:
		return errors.New("unknown Git command")
	}
	var result any
	if err = c.Call(ctx, p, &result); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
	return nil
}
func install() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	bin := filepath.Join(home, ".local", "bin")
	if configured := os.Getenv("YOLOMANCER_GIT_BIN"); configured != "" {
		bin = configured
	}
	if err = os.MkdirAll(bin, 0755); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(executable), "'", "'\"'\"'") + "'"
	for name, command := range map[string]string{"git-remote-yolomancer": "remote-helper", "git-invite": "invite", "git-revoke": "revoke"} {
		target := filepath.Join(bin, name)
		f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
		if os.IsExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		_, e = f.WriteString("#!/bin/sh\nexec " + quoted + " git " + command + " \"$@\"\n")
		f.Close()
		if e != nil {
			return e
		}
	}
	fmt.Println("Git helpers installed in " + bin + "; ensure it is on PATH.")
	return nil
}
