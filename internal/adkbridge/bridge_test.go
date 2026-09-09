package adkbridge

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"testing"

	yolomodel "github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/security"
	yolotools "github.com/dennisvink/yolomancer/internal/tools"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestRunTurnUsesADKFunctionCallLoop(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("from tool"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &yolomodel.Config{}
	sink := &recordingSink{}
	executor := &yolotools.Executor{Config: cfg, Policy: security.BuildPolicy(cfg, root), Mode: yolomodel.ModeDefault, Processes: process.New()}
	fake := &loopModel{path: path}

	text, messages, err := RunTurn(context.Background(), "read it", Config{
		ModelConfig: cfg,
		Mode:        yolomodel.ModeDefault,
		SessionID:   "adk-loop-test",
		Executor:    executor,
		Sink:        sink,
		llm:         fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != "The file says from tool." {
		t.Fatalf("text = %q", text)
	}
	if fake.calls != 2 {
		t.Fatalf("model calls = %d, want 2", fake.calls)
	}
	if sink.toolResults != 1 || sink.toolCalls != 1 || sink.done != 2 {
		t.Fatalf("sink calls=%d results=%d done=%d", sink.toolCalls, sink.toolResults, sink.done)
	}
	if len(messages) != 4 {
		t.Fatalf("persisted messages = %d, want user/call/result/final", len(messages))
	}
	result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)["toolResult"].(map[string]any)
	if result["toolUseId"] != "read-1" {
		t.Fatalf("tool result lost call ID: %#v", result)
	}
}

type loopModel struct {
	path  string
	calls int
}

func (m *loopModel) Name() string { return "test-bedrock" }

func (m *loopModel) GenerateContent(_ context.Context, req *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		m.calls++
		switch m.calls {
		case 1:
			call := &genai.FunctionCall{ID: "read-1", Name: "read_file", Args: map[string]any{"path": m.path, "reason": "read fixture"}}
			content := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}}
			yield(&adkmodel.LLMResponse{Content: content, TurnComplete: true}, nil)
		case 2:
			if !containsFunctionResponse(req.Contents, "read-1") {
				yield(nil, fmt.Errorf("ADK did not feed the tool result back to the model"))
				return
			}
			yield(&adkmodel.LLMResponse{Content: genai.NewContentFromText("The file says from tool.", genai.RoleModel), TurnComplete: true}, nil)
		default:
			yield(nil, fmt.Errorf("unexpected extra model call"))
		}
	}
}

func containsFunctionResponse(contents []*genai.Content, id string) bool {
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.ID == id {
				return true
			}
		}
	}
	return false
}

type recordingSink struct {
	toolCalls, toolResults, done int
}

func (*recordingSink) ReasoningDelta(string)                   {}
func (*recordingSink) AssistantDelta(string)                   {}
func (*recordingSink) AssistantMessage(string)                 {}
func (s *recordingSink) AssistantDone()                        { s.done++ }
func (s *recordingSink) ToolCall(yolomodel.ToolCall)           { s.toolCalls++ }
func (s *recordingSink) ToolResult(yolomodel.ToolCall, string) { s.toolResults++ }
func (*recordingSink) Info(string)                             {}
func (*recordingSink) Debug(string)                            {}
func (*recordingSink) Usage(yolomodel.Usage)                   {}
