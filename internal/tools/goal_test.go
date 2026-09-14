package tools

import (
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestGoalToolsValidationAndUserPrecedence(t *testing.T) {
	e := &Executor{Goals: goal.New(nil, nil)}
	call := func(name string, args map[string]any) string {
		return e.Execute(t.Context(), model.ToolCall{Name: name, Arguments: args})
	}
	for _, b := range []any{0, -1, 1.5, "100"} {
		if out := call("create_goal", map[string]any{"objective": "work", "token_budget": b}); !strings.Contains(out, `"ok":false`) {
			t.Fatal(out)
		}
	}
	if out := call("create_goal", map[string]any{"objective": "work", "token_budget": 100}); !strings.Contains(out, `"active"`) {
		t.Fatal(out)
	}
	if out := call("update_goal", map[string]any{"status": "paused"}); !strings.Contains(out, `"ok":false`) {
		t.Fatal(out)
	}
	_ = e.Goals.SetStatus(goal.Paused)
	if out := call("update_goal", map[string]any{"status": "complete"}); !strings.Contains(out, `"ok":false`) {
		t.Fatal(out)
	}
	_ = e.Goals.SetStatus(goal.Active)
	if out := call("update_goal", map[string]any{"status": "complete"}); !strings.Contains(out, "completion_report") {
		t.Fatal(out)
	}
}
