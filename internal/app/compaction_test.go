package app

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/session"
)

type compactionTransport func(*http.Request) (*http.Response, error)

func (f compactionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManualBedrockCompactionPersistsAndResumes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	fail := false
	http.DefaultTransport = compactionTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.Host, "bedrock-runtime.") || !strings.HasSuffix(r.URL.Path, "/converse") {
			t.Fatalf("wrong compaction endpoint: %s", r.URL)
		}
		if fail {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"message":"failed"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"output":{"message":{"role":"assistant","content":[{"text":"Changes done; run the tests."}]}}}`))}, nil
	})
	cfg := &model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "secret", "")}
	a := New(cfg, false)
	a.Messages = []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "finish tests"}}}, map[string]any{"role": "assistant", "content": []any{map[string]any{"text": strings.Repeat("progress ", 1000)}}}}
	a.contextBudget = model.ContextBudget{KnownTokens: 950000}
	if _, _, err := a.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.ContextBudget().KnownTokens != 0 {
		t.Fatal("compaction retained stale token usage")
	}
	if !strings.Contains(string(a.RawMessages()[len(a.Messages)-1]), strings.TrimSpace(provider.SummaryPrefix)) {
		t.Fatal("manual compaction did not replace history")
	}
	snapshot := &model.SessionSnapshot{Version: 1, SessionID: a.SessionID, BedrockMessages: a.RawMessages(), ContextBudget: a.ContextBudget(), CollaborationMode: a.Mode}
	if err := session.Write(snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(a.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := Restore(cfg, false, loaded)
	if !reflect.DeepEqual(resumed.RawMessages(), a.RawMessages()) {
		t.Fatal("resuming lost checkpoint")
	}
	before := a.RawMessages()
	fail = true
	if _, _, err := a.Compact(t.Context()); err == nil {
		t.Fatal("expected compaction failure")
	}
	if !reflect.DeepEqual(before, a.RawMessages()) {
		t.Fatal("failed compaction replaced history")
	}
}
