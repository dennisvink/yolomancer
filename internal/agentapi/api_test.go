package agentapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestMain(m *testing.M) {
	// Real subprocess protocol, with a synthetic model endpoint rather than AWS.
	if len(os.Args) == 2 && os.Args[1] == "internal-api-worker" {
		endpoint, err := url.Parse(os.Getenv("YOLOMANCER_TEST_MODEL"))
		if err != nil || endpoint.Host == "" {
			os.Exit(2)
		}
		original := http.DefaultTransport
		http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
			copy := r.Clone(r.Context())
			u := *r.URL
			u.Scheme = endpoint.Scheme
			u.Host = endpoint.Host
			copy.URL = &u
			return original.RoundTrip(copy)
		})
		err = Worker(context.Background(), os.Stdin, os.Stdout, func(context.Context, string) (*model.Config, error) {
			return &model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, nil
		})
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func frame(event, payload string) []byte {
	header := func(name, value string) []byte {
		b := append([]byte{byte(len(name))}, name...)
		b = append(b, 7, byte(len(value)>>8), byte(len(value)))
		return append(b, value...)
	}
	headers := append(header(":message-type", "event"), header(":event-type", event)...)
	b := make([]byte, 16+len(headers)+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	binary.BigEndian.PutUint32(b[4:8], uint32(len(headers)))
	copy(b[12:], headers)
	copy(b[12+len(headers):], payload)
	binary.BigEndian.PutUint32(b[8:12], crc32.ChecksumIEEE(b[:8]))
	binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
	return b
}

func configFile(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"+extra), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestConfigValidation(t *testing.T) {
	base := "agents:\n  reader:\n    tools:\n      builtin: [read_file]\n    permissions:\n      mode: restricted\n      read_roots: [.]\n"
	path := configFile(t, base)
	cfg, agents, err := Load(path, "", 0)
	if err != nil || cfg.Server.Host != "127.0.0.1" || cfg.Server.Port != 8081 || len(agents["reader"].Specs) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, _, err := Load(path, "0.0.0.0", 0); err == nil {
		t.Fatal("public unauthenticated bind")
	}
	for _, bad := range []string{
		"version: 1\n", // duplicate key
		"unknown: true\n",
		"server:\n  host: 0.0.0.0\n",
		"server:\n  bearer_token_env: NONEXISTENT_YOLOMANCER_TEST_TOKEN\n",
		"server:\n  bearer_token: short\n",
		"server:\n  max_concurrent: -1\n",
		"server:\n  port: 99999\n",
	} {
		if _, _, err := Load(configFile(t, bad+base), "", 0); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	t.Setenv("YOLOMANCER_TEST_TOKEN", strings.Repeat("x", 32))
	path = configFile(t, "server:\n  host: 0.0.0.0\n  bearer_token_env: YOLOMANCER_TEST_TOKEN\n"+base)
	if cfg, _, err := Load(path, "", 0); err != nil || cfg.Server.BearerToken != strings.Repeat("x", 32) {
		t.Fatal(err)
	}
	for _, tool := range []string{"exec_command", "write_stdin", "aws_cli", "create_goal", "not_a_tool"} {
		if _, _, err := Load(configFile(t, strings.Replace(base, "read_file", tool, 1)), "", 0); err == nil {
			t.Fatal("unsafe/unknown tool accepted:", tool)
		}
	}
}

func testServer(t *testing.T, runner Runner) *Server {
	t.Helper()
	_, agents, err := Load(configFile(t, "agents:\n  reader:\n    tools:\n      builtin: [read_file]\n    permissions:\n      read_roots: [.]\n"), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(ServerConfig{Host: "127.0.0.1", Port: 8081, MaxConcurrent: 1}, agents, "", runner)
}
func request(t *testing.T, s *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:8081"+path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAuthAndRequestValidation(t *testing.T) {
	s := testServer(t, func(context.Context, Work, func(Event) error) error { t.Error("unexpected run"); return nil })
	s.cfg.BearerToken = strings.Repeat("z", 32)
	if w := request(t, s, "GET", "/healthz", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	h := map[string]string{"Authorization": "Bearer " + s.cfg.BearerToken}
	if w := request(t, s, "GET", "/v1/agents", "", h); w.Code != 200 || strings.Contains(w.Body.String(), s.cfg.BearerToken) {
		t.Fatal(w.Body.String())
	}
	h["Origin"] = "https://evil.example"
	if w := request(t, s, "POST", "/v1/runs", `{}`, h); w.Code != 403 {
		t.Fatal(w.Code)
	}
	delete(h, "Origin")
	for _, body := range []string{`{}`, `{"agent":"reader","prompt":"hi","permissions":"yolo"}`, `{"agent":"reader","prompt":"hi"} {}`, strings.Repeat("x", 70000)} {
		if w := request(t, s, "POST", "/v1/runs", body, h); w.Code != 400 {
			t.Fatal(w.Code, body[:min(30, len(body))])
		}
	}
	if w := request(t, s, "POST", "/v1/runs", `{"agent":"unknown","prompt":"hi"}`, h); w.Code != 404 {
		t.Fatal(w.Code)
	}
	s.cfg.BearerToken = ""
	r := httptest.NewRequest("GET", "http://evil.example/healthz", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("DNS rebinding allowed")
	}
}

func TestStreamCancellationConcurrencyAndDeadline(t *testing.T) {
	started := make(chan struct{}, 1)
	s := testServer(t, func(ctx context.Context, _ Work, emit func(Event) error) error {
		_ = emit(Event{"assistant.delta", map[string]string{"text": "hello"}})
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- request(t, s, "POST", "/v1/runs", `{"agent":"reader","prompt":"hi"}`, nil) }()
	<-started
	if w := request(t, s, "POST", "/v1/runs", `{"agent":"reader","prompt":"hi"}`, nil); w.Code != 429 {
		t.Fatal(w.Code)
	}
	s.mu.Lock()
	id := ""
	for k := range s.runs {
		id = k
	}
	s.mu.Unlock()
	if w := request(t, s, "DELETE", "/v1/runs/"+id, "", nil); w.Code != 202 {
		t.Fatal(w.Code)
	}
	select {
	case w := <-finished:
		if !strings.Contains(w.Body.String(), "run.cancelled") {
			t.Fatal(w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation hung")
	}
	a := s.agents["reader"]
	a.Agent.Limits.TimeoutSeconds = 1
	s.agents["reader"] = a
	w := request(t, s, "POST", "/v1/runs", `{"agent":"reader","prompt":"hi"}`, nil)
	if !strings.Contains(w.Body.String(), "deadline_exceeded") {
		t.Fatal(w.Body.String())
	}
}

func TestProcessWorkerStreamingToolsAndPermissionDenial(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			calls := 0
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				system, _ := json.Marshal(body["system"])
				if !strings.Contains(string(system), "non-interactive API request") {
					t.Error("missing API system instructions")
				}
				registered := body["toolConfig"].(map[string]any)["tools"].([]any)
				if len(registered) != 1 || registered[0].(map[string]any)["toolSpec"].(map[string]any)["name"] != "read_file" {
					t.Error("unexpected tools", registered)
				}
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				w.Write(frame("messageStart", `{"role":"assistant"}`))
				if calls == 1 {
					name := "read_file"
					if denied {
						name = "write_file"
					}
					w.Write(frame("contentBlockStart", fmt.Sprintf(`{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"call-1","name":%q}}}`, name)))
					args := `{"path":"example.txt","reason":"test"}`
					w.Write(frame("contentBlockDelta", fmt.Sprintf(`{"contentBlockIndex":0,"delta":{"toolUse":{"input":%q}}}`, args)))
				} else {
					b, _ := json.Marshal(body["messages"])
					if !strings.Contains(string(b), "sample content") {
						t.Error("tool result missing", string(b))
					}
					w.Write(frame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"done"}}`))
				}
				w.Write(frame("contentBlockStop", `{"contentBlockIndex":0}`))
				stop := "end_turn"
				if calls == 1 {
					stop = "tool_use"
				}
				w.Write(frame("messageStop", fmt.Sprintf(`{"stopReason":%q}`, stop)))
				w.Write(frame("metadata", `{"usage":{"inputTokens":10,"outputTokens":5}}`))
			}))
			defer modelServer.Close()
			t.Setenv("YOLOMANCER_TEST_MODEL", modelServer.URL)
			s := testServer(t, nil)
			root := s.agents["reader"].Agent.Workspace
			if err := os.WriteFile(filepath.Join(root, "example.txt"), []byte("sample content"), 0600); err != nil {
				t.Fatal(err)
			}
			w := request(t, s, "POST", "/v1/runs", `{"agent":"reader","prompt":"read example"}`, nil)
			want := "run.completed"
			if denied {
				want = "run.blocked"
			}
			if !strings.Contains(w.Body.String(), want) || !strings.Contains(w.Body.String(), "tool.completed") {
				t.Fatal(w.Body.String())
			}
			if denied && calls != 1 {
				t.Fatal("continued after denial", calls)
			}
			got, _ := os.ReadFile(filepath.Join(root, "example.txt"))
			if string(got) != "sample content" {
				t.Fatal("denied write executed")
			}
		})
	}
}

func TestBudgetSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	s := &eventSink{encode: json.NewEncoder(&out), cancel: cancel, budget: 10}
	s.Usage(model.Usage{InputTokens: 8, OutputTokens: 3})
	if ctx.Err() == nil || !s.limitHit {
		t.Fatal("budget did not stop run")
	}
}

func TestPythonWorkerUsesExplicitToolAndHidesServiceToken(t *testing.T) {
	root := t.TempDir()
	code := "def yolomancer_tool():\n return {'name':'probe','description':'Return a test marker','parameters':{'type':'object','properties':{}}}\ndef run(args):\n import os\n return {'marker':'python-worked', 'service_token_present':'TEST_SERVICE_TOKEN' in os.environ}\n"
	if err := os.WriteFile(filepath.Join(root, "probe.py"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := register(Agent{Workspace: root, Tools: ToolConfig{Python: []string{"probe.py"}}, Permissions: Permissions{Mode: "yolo", Network: "allow"}}, root)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		w.Write(frame("messageStart", `{"role":"assistant"}`))
		if calls == 1 {
			w.Write(frame("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"py1","name":"probe"}}}`))
			w.Write(frame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"reason\":\"test\"}"}}}`))
		} else {
			b, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(b), "python-worked") || !strings.Contains(string(b), `\"service_token_present\":false`) && !strings.Contains(string(b), `"service_token_present":false`) {
				t.Error("Python result missing/token leaked", string(b))
			}
			w.Write(frame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"verified"}}`))
		}
		w.Write(frame("contentBlockStop", `{"contentBlockIndex":0}`))
		stop := "end_turn"
		if calls == 1 {
			stop = "tool_use"
		}
		w.Write(frame("messageStop", fmt.Sprintf(`{"stopReason":%q}`, stop)))
		w.Write(frame("metadata", `{"usage":{"inputTokens":10,"outputTokens":5}}`))
	}))
	defer stub.Close()
	t.Setenv("YOLOMANCER_TEST_MODEL", stub.URL)
	t.Setenv("TEST_SERVICE_TOKEN", strings.Repeat("t", 32))
	s := NewServer(ServerConfig{Host: "127.0.0.1", Port: 8081, MaxConcurrent: 1, BearerToken: strings.Repeat("t", 32), BearerTokenEnv: "TEST_SERVICE_TOKEN"}, map[string]Registered{"probe": r}, "", nil)
	w := request(t, s, "POST", "/v1/runs", `{"agent":"probe","prompt":"test"}`, map[string]string{"Authorization": "Bearer " + strings.Repeat("t", 32)})
	if !strings.Contains(w.Body.String(), "run.completed") || calls != 2 {
		t.Fatal(w.Body.String(), calls)
	}
}
