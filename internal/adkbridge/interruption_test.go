package adkbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/security"
	"github.com/dennisvink/yolomancer/internal/tools"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type streamFunc func(context.Context, []any, model.CollaborationMode, []map[string]any, func(string), func(string)) (map[string]any, error)

func (f streamFunc) ConverseStream(ctx context.Context, messages []any, mode model.CollaborationMode, specs []map[string]any, text, reasoning func(string)) (map[string]any, error) {
	return f(ctx, messages, mode, specs, text, reasoning)
}

func TestStreamStopsAfterConsumerBreaks(t *testing.T) {
	for _, thought := range []bool{false, true} {
		sink := &recordingSink{}
		llm := &bedrockLLM{state: &turnState{sink: sink}, bedrock: streamFunc(func(ctx context.Context, _ []any, _ model.CollaborationMode, _ []map[string]any, text, reasoning func(string)) (map[string]any, error) {
			callback := text
			if thought {
				callback = reasoning
			}
			callback("first")
			if ctx.Err() == nil {
				t.Error("stopped consumer did not cancel HTTP stream")
			}
			callback("must not yield again")
			return nil, context.Canceled
		})}
		count := 0
		for range llm.GenerateContent(t.Context(), &adkmodel.LLMRequest{}, true) {
			count++
			break
		}
		if count != 1 {
			t.Fatalf("got %d responses", count)
		}
	}
}

func TestADKToolLoopContinuesPast24Rounds(t *testing.T) {
	root := t.TempDir()
	cfg := &model.Config{}
	sink := &recordingSink{}
	executor := &tools.Executor{Config: cfg, Policy: security.BuildPolicy(cfg, root), Processes: process.New()}
	calls := 0
	llm := &bedrockLLM{state: &turnState{sink: sink}, bedrock: streamFunc(func(ctx context.Context, _ []any, _ model.CollaborationMode, _ []map[string]any, _, _ func(string)) (map[string]any, error) {
		calls++
		var block any = map[string]any{"text": "finished"}
		if calls <= 30 {
			block = map[string]any{"toolUse": map[string]any{"toolUseId": fmt.Sprintf("list-%d", calls), "name": "list_files", "input": map[string]any{"path": root, "reason": "inspect"}}}
		}
		return map[string]any{"output": map[string]any{"message": map[string]any{"role": "assistant", "content": []any{block}}}}, ctx.Err()
	})}
	text, _, err := RunTurn(t.Context(), "work", Config{ModelConfig: cfg, SessionID: "long-loop", Executor: executor, Sink: sink, llm: llm})
	if err != nil || text != "finished" || calls != 31 || sink.toolResults != 30 {
		t.Fatalf("text=%q calls=%d results=%d error=%v", text, calls, sink.toolResults, err)
	}
}

type cancelAfterResult struct {
	recordingSink
	cancel context.CancelFunc
}

type cancelAtToolCall struct {
	recordingSink
	cancel context.CancelFunc
}

func (s *cancelAtToolCall) ToolCall(call model.ToolCall) { s.recordingSink.ToolCall(call); s.cancel() }

func TestCancelAtToolCallBoundary(t *testing.T) {
	for i := 0; i < 50; i++ {
		root := t.TempDir()
		cfg := &model.Config{}
		ctx, cancel := context.WithCancel(t.Context())
		sink := &cancelAtToolCall{cancel: cancel}
		executor := &tools.Executor{Config: cfg, Policy: security.BuildPolicy(cfg, root), Processes: process.New()}
		fake := &loopModel{path: root}
		history := []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"text": "previous turn"}}},
			map[string]any{"role": "assistant", "content": []any{map[string]any{"text": "previous answer"}}},
		}
		_, messages, err := RunTurn(ctx, "read", Config{ModelConfig: cfg, SessionID: "cancel-call", Messages: history, Executor: executor, Sink: sink, llm: fake})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if sink.toolResults != 0 {
			t.Fatal("tool ran after cancellation at announcement")
		}
		_, _, err = RunTurn(t.Context(), "continue", Config{ModelConfig: cfg, SessionID: "cancel-call-resumed", Messages: messages, Executor: executor, Sink: &recordingSink{}, llm: &loopModel{calls: 1}})
		if err != nil {
			t.Fatalf("iteration %d could not continue cancelled resume: %v", i, err)
		}
	}
}

func (s *cancelAfterResult) ToolResult(call model.ToolCall, result string) {
	s.recordingSink.ToolResult(call, result)
	s.cancel()
}

func TestInterruptedToolResultIsSavedAndTurnCanResume(t *testing.T) {
	root := t.TempDir()
	cfg := &model.Config{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sink := &cancelAfterResult{cancel: cancel}
	executor := &tools.Executor{Config: cfg, Policy: security.BuildPolicy(cfg, root), Processes: process.New()}
	fake := &loopModel{path: root}
	_, messages, err := RunTurn(ctx, "read", Config{ModelConfig: cfg, SessionID: "cancel", Executor: executor, Sink: sink, llm: fake})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if len(messages) < 3 {
		t.Fatalf("lost tool exchange: %#v", messages)
	}
	if !containsFunctionResponse([]*genai.Content{messageToContent(messages[2].(map[string]any))}, "read-1") {
		t.Fatal("interrupted history lost tool result")
	}
	for _, history := range [][]any{messages, messages[:2]} {
		resumed := &loopModel{calls: 1}
		_, _, err = RunTurn(t.Context(), "continue", Config{ModelConfig: cfg, SessionID: "resume", Messages: history, Executor: executor, Sink: &recordingSink{}, llm: resumed})
		if err != nil {
			t.Fatalf("could not resume: %v", err)
		}
	}
}
