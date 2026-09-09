package adkbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/security"
	"github.com/dennisvink/yolomancer/internal/tools"
)

type compactFunc func(context.Context, []any, model.CollaborationMode) ([]any, *model.Usage, error)

func (f compactFunc) CompactHistory(ctx context.Context, m []any, mode model.CollaborationMode) ([]any, *model.Usage, error) {
	return f(ctx, m, mode)
}

func TestAutomaticCompactionAcrossToolLoopAndResume(t *testing.T) {
	for _, trigger := range []string{"preflight", "mid-turn", "overflow"} {
		t.Run(trigger, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "note")
			if err := os.WriteFile(path, []byte("recorded result"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := &model.Config{}
			sink := &recordingSink{}
			executor := &tools.Executor{Config: cfg, Policy: security.BuildPolicy(cfg, root), Processes: process.New()}
			budget := &model.ContextBudget{}
			if trigger == "preflight" {
				budget.KnownTokens = provider.AutoCompactTokens
			}
			compactions, calls := 0, 0
			compactor := compactFunc(func(_ context.Context, messages []any, _ model.CollaborationMode) ([]any, *model.Usage, error) {
				compactions++
				raw, _ := json.Marshal(messages)
				if trigger == "mid-turn" && !strings.Contains(string(raw), "recorded result") {
					t.Fatal("compacted before recording tool result")
				}
				return []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": provider.SummaryPrefix + "Continue the current task; the file was read."}}}}, nil, nil
			})
			llm := &bedrockLLM{budget: budget, state: &turnState{sink: sink}, bedrock: streamFunc(func(_ context.Context, messages []any, _ model.CollaborationMode, _ []map[string]any, _, _ func(string)) (map[string]any, error) {
				calls++
				if compactions == 0 {
					if trigger == "preflight" {
						t.Fatal("oversized preflight reached Bedrock")
					}
					if trigger == "overflow" {
						return nil, fmt.Errorf("Bedrock ConverseStream failed: prompt is too long")
					}
					return map[string]any{"output": map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"toolUse": map[string]any{"toolUseId": "read-compact", "name": "read_file", "input": map[string]any{"path": path, "reason": "inspect"}}}}}}, "usage": map[string]any{"inputTokens": 899999, "outputTokens": 100}}, nil
				}
				raw, _ := json.Marshal(messages)
				if !strings.Contains(string(raw), "Context checkpoint") || strings.Contains(string(raw), "old history") {
					t.Fatal("old context survived compaction")
				}
				return map[string]any{"output": map[string]any{"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"text": "finished"}}}}}, nil
			})}
			history := []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": strings.Repeat("old history ", 1000)}}}}
			text, messages, err := RunTurn(t.Context(), "do the task", Config{ModelConfig: cfg, Messages: history, SessionID: "compact-test", Executor: executor, Sink: sink, llm: llm, Budget: budget, compactor: compactor})
			if err != nil || text != "finished" || compactions != 1 {
				t.Fatalf("text=%s compactions=%d calls=%d err=%v", text, compactions, calls, err)
			}
			if len(messages) != 2 {
				t.Fatalf("checkpoint was not persisted or prompt duplicated: %#v", messages)
			}
			if trigger == "mid-turn" && sink.toolResults != 1 {
				t.Fatal("tool was replayed")
			}
		})
	}
}
