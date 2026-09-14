package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/session"
)

type quietSink struct{}

func (quietSink) ReasoningDelta(string)             {}
func (quietSink) AssistantDelta(string)             {}
func (quietSink) AssistantMessage(string)           {}
func (quietSink) AssistantDone()                    {}
func (quietSink) ToolCall(model.ToolCall)           {}
func (quietSink) ToolResult(model.ToolCall, string) {}
func (quietSink) Info(string)                       {}
func (quietSink) Debug(string)                      {}
func (quietSink) Usage(model.Usage)                 {}

func TestHeadlessGoalContinuesAfterFinalUntilToolCompletes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = compactionTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "Verify all requirements") {
			t.Fatal("goal missing from request")
		}
		frames := goalFrame("messageStart", `{"role":"assistant"}`)
		if calls == 2 {
			frames = append(frames, goalFrame("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"complete-1","name":"update_goal"}}}`)...)
			frames = append(frames, goalFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"status\":\"complete\",\"reason\":\"verified\"}"}}}`)...)
		} else {
			frames = append(frames, goalFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Progress."}}`)...)
		}
		if calls > 3 {
			t.Fatal("continued after completion")
		}
		frames = append(frames, goalFrame("contentBlockStop", `{"contentBlockIndex":0}`)...)
		stop := "end_turn"
		if calls == 2 {
			stop = "tool_use"
		}
		frames = append(frames, goalFrame("messageStop", `{"stopReason":"`+stop+`"}`)...)
		frames = append(frames, goalFrame("metadata", `{"usage":{"inputTokens":10,"outputTokens":5}}`)...)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(frames))}, nil
	})
	a := New(&model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, false)
	if err := a.Goals.Create("Verify all requirements", 1000, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(t.Context(), "start", quietSink{}); err != nil {
		t.Fatal(err)
	}
	g := a.Goals.Get()
	if calls != 3 || g.Status != goal.Complete || g.Turns != 2 || g.TokensUsed != 30 {
		t.Fatalf("calls=%d goal=%+v", calls, g)
	}
	saved, err := session.Load(a.SessionID)
	if err != nil || saved.Goal.Status != goal.Complete || len(saved.BedrockMessages) != 6 {
		t.Fatalf("headless goal not resumable: %+v %v", saved, err)
	}
}

func TestBudgetExhaustionWrapsUpWithoutAnotherGoalTurn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	http.DefaultTransport = compactionTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		b, _ := io.ReadAll(r.Body)
		frames := goalFrame("messageStart", `{"role":"assistant"}`)
		if calls == 1 {
			frames = append(frames, goalFrame("contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"inspect","name":"get_goal"}}}`)...)
			frames = append(frames, goalFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"reason\":\"inspect\"}"}}}`)...)
		} else {
			if calls > 2 || !strings.Contains(string(b), "budget is exhausted") || !strings.Contains(string(b), "toolResult") {
				t.Fatalf("bad budget wrap-up: call=%d body=%s", calls, b)
			}
			frames = append(frames, goalFrame("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Budget reached; more work remains."}}`)...)
		}
		frames = append(frames, goalFrame("contentBlockStop", `{"contentBlockIndex":0}`)...)
		stop := "end_turn"
		if calls == 1 {
			stop = "tool_use"
		}
		frames = append(frames, goalFrame("messageStop", `{"stopReason":"`+stop+`"}`)...)
		frames = append(frames, goalFrame("metadata", `{"usage":{"inputTokens":10,"outputTokens":5}}`)...)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(frames))}, nil
	})
	a := New(&model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, false)
	_ = a.Goals.Create("Finish all work", 10, false)
	if _, err := a.Run(t.Context(), "start", quietSink{}); err != nil {
		t.Fatal(err)
	}
	if g := a.Goals.Get(); g.Status != goal.BudgetLimited || g.Turns != 1 || g.TokensUsed != 30 || calls != 2 {
		t.Fatalf("calls=%d goal=%+v", calls, g)
	}
}

func TestGoalAccountingTransportSemantics(t *testing.T) {
	for _, bedrock := range []bool{true, false} {
		g := goal.New(nil, nil)
		_ = g.Create("work", 0, false)
		_ = g.Begin()
		s := &goalSink{Sink: quietSink{}, goals: g, bedrock: bedrock}
		u := model.Usage{InputTokens: 100, CacheReadInputTokens: 80, CacheWriteInputTokens: 5, OutputTokens: 10}
		s.Usage(u)
		want := uint64(30)
		if bedrock {
			want = 115
		}
		if g.Get().TokensUsed != want {
			t.Fatalf("bedrock=%v got=%d want=%d", bedrock, g.Get().TokensUsed, want)
		}
	}
}

func TestTurnErrorsStopGoalAndStaleTurnDoesNotRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, tc := range []struct {
		message string
		status  goal.Status
	}{{"bad request", goal.Blocked}, {"insufficient_quota", goal.UsageLimited}} {
		http.DefaultTransport = compactionTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("%s", tc.message) })
		a := New(&model.Config{BedrockCredentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, false)
		_ = a.Goals.Create("work", 0, false)
		if _, err := a.Run(t.Context(), "start", quietSink{}); err == nil {
			t.Fatal("expected failure")
		}
		if a.Goals.Get().Status != tc.status {
			t.Fatal(a.Goals.Get())
		}
		if _, err := a.RunGoalTurn(t.Context(), a.Goals.Get().ID, quietSink{}); err != nil {
			t.Fatal("stopped goal still contacted model")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if goalStop(ctx, ctx.Err()) != goal.Paused {
		t.Fatal("interrupt did not pause")
	}
	a := New(&model.Config{}, false)
	_ = a.Goals.Create("work", 0, false)
	if _, err := a.RunGoalTurn(ctx, a.Goals.Get().ID, quietSink{}); err == nil || a.Goals.Get().Status != goal.Paused {
		t.Fatal("pre-start interrupt left active goal")
	}
}

func goalFrame(event, payload string) []byte {
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
