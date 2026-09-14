package adkbridge

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/tools"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type goalModel struct{ calls int }

func (*goalModel) Name() string { return "goal-test" }
func (m *goalModel) GenerateContent(_ context.Context, req *adkmodel.LLMRequest, _ bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		m.calls++
		if m.calls > 1 {
			last := req.Contents[len(req.Contents)-1]
			text := last.Parts[len(last.Parts)-1].Text
			want := "active"
			if m.calls == 4 {
				want = "complete"
			}
			if !strings.Contains(text, `"status":"`+want+`"`) || !strings.Contains(text, "Verify fixture") {
				yield(nil, fmt.Errorf("stale goal context: %s", text))
				return
			}
		}
		var call *genai.FunctionCall
		switch m.calls {
		case 1:
			call = &genai.FunctionCall{ID: "create", Name: "create_goal", Args: map[string]any{"objective": "Verify fixture", "token_budget": 100, "reason": "User explicitly requested goal"}}
		case 2:
			call = &genai.FunctionCall{ID: "get", Name: "get_goal", Args: map[string]any{"reason": "Inspect goal"}}
		case 3:
			call = &genai.FunctionCall{ID: "update", Name: "update_goal", Args: map[string]any{"status": "complete", "reason": "Fixture verified"}}
		case 4:
			yield(&adkmodel.LLMResponse{Content: genai.NewContentFromText("Verified.", genai.RoleModel), TurnComplete: true}, nil)
			return
		default:
			yield(nil, fmt.Errorf("unexpected continuation"))
			return
		}
		yield(&adkmodel.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}}, TurnComplete: true}, nil)
	}
}

func TestADKGoalToolsAndFreshContext(t *testing.T) {
	g := goal.New(nil, nil)
	cfg := &model.Config{}
	fake := &goalModel{}
	_, messages, err := RunTurn(t.Context(), "Set a goal to verify fixture", Config{ModelConfig: cfg, SessionID: "goals", Executor: &tools.Executor{Goals: g, Config: cfg}, Sink: &recordingSink{}, llm: fake})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 4 || g.Get().Status != goal.Complete {
		t.Fatalf("calls=%d goal=%+v", fake.calls, g.Get())
	}
	// Dynamic state is an overlay, not a repeated history rewrite. Retain all
	// tool/result pairs and the final response exactly once.
	if len(messages) != 8 {
		t.Fatalf("messages=%d", len(messages))
	}
}

func TestGoalOverlayDoesNotMutateHistory(t *testing.T) {
	g := goal.New(nil, nil)
	_ = g.Create("Verify fixture", 0, false)
	_ = g.ModelStatus(goal.Complete)
	contents := []*genai.Content{genai.NewContentFromText("original", genai.RoleUser)}
	fake := &goalModel{calls: 3}
	wrapper := &goalLLM{LLM: fake, goals: g}
	for _, err := range wrapper.GenerateContent(t.Context(), &adkmodel.LLMRequest{Contents: contents}, true) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "original" {
		t.Fatal("overlay mutated conversation")
	}
}
