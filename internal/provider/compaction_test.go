package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestCompactionRecoversOversizedHistory(t *testing.T) {
	history := []any{textMessage("user", "old request"), textMessage("assistant", strings.Repeat("large history ", 100000)), textMessage("user", "Keep changes in /project and finish the tests")}
	before, _ := json.Marshal(history)
	calls, firstSize := 0, 0
	b := &Bedrock{Config: &model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "secret", "")}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["toolConfig"] != nil || body["system"] == nil {
			t.Fatal("summary must use system instructions and no tools")
		}
		if calls == 1 {
			firstSize = len(raw)
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"message":"prompt is too long"}`))}, nil
		}
		if len(raw) >= firstSize {
			t.Fatal("context overflow did not trim oldest input")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"output":{"message":{"role":"assistant","content":[{"text":"Tests remain. Work in /project."}]}},"usage":{"inputTokens":123,"outputTokens":9}}`))}, nil
	})}}
	got, usage, err := b.CompactHistory(t.Context(), history, model.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || usage.InputTokens != 123 {
		t.Fatalf("calls=%d usage=%v", calls, usage)
	}
	if len(got) != 3 || !reflect.DeepEqual(got[1], history[2]) {
		t.Fatalf("lost recent user instruction: %#v", got)
	}
	last, _ := json.Marshal(got[len(got)-1])
	if !strings.Contains(string(last), SummaryPrefix[:len(SummaryPrefix)-1]) {
		t.Fatal("missing checkpoint")
	}
	after, _ := json.Marshal(history)
	if string(before) != string(after) {
		t.Fatal("original history mutated")
	}
}

func TestCompactionCancelledOrEmptyDoesNotReplaceHistory(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		b := &Bedrock{Config: &model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "secret", "")}, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if cancelled {
				cancel()
				return nil, context.Canceled
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"output":{"message":{"role":"assistant","content":[{"text":""}]}}}`))}, nil
		})}}
		got, _, err := b.CompactHistory(ctx, []any{textMessage("user", "task")}, model.ModeDefault)
		cancel()
		if err == nil || got != nil {
			t.Fatal("failed summary returned replacement")
		}
		if cancelled && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}

func TestBoundToolOutputAndUserRetention(t *testing.T) {
	output := `{"ok":false,"content":"` + strings.Repeat("界\\\"", 40000) + `"}`
	got := BoundToolOutput(output)
	if len(got) > ToolOutputBytes || !utf8.ValidString(got) || !json.Valid([]byte(got)) {
		t.Fatal("invalid bounded result")
	}
	var result map[string]any
	json.Unmarshal([]byte(got), &result)
	if result["ok"] != false || result["truncated"] != true {
		t.Fatal("lost tool result status")
	}
	users := retainedUserMessages([]any{textMessage("user", strings.Repeat("old", 50000)), textMessage("user", SummaryPrefix+"previous summary"), textMessage("user", "latest request")})
	if len(users) != 2 || !reflect.DeepEqual(users[1], textMessage("user", "latest request")) {
		t.Fatal("latest user request not retained")
	}
	var total int
	for _, raw := range users {
		total += len(raw.(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string))
	}
	if total > compactUserBytes {
		t.Fatal("retained users exceed budget")
	}
}
