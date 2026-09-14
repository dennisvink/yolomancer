package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dennisvink/yolomancer/internal/goal"
)

func goalSpecs() []map[string]any {
	spec := func(name, description string, props map[string]any, required ...string) map[string]any {
		props["reason"] = map[string]any{"type": "string", "description": "Short narration of this goal operation."}
		required = append([]string{"reason"}, required...)
		return map[string]any{"type": "function", "name": name, "description": description, "parameters": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}
	}
	return []map[string]any{
		spec("get_goal", "Read the persisted session goal, status, token budget and usage. A zero budget means no limit.", map[string]any{}),
		spec("create_goal", "Create a persistent goal only when explicitly requested by the user; do not infer goals from ordinary tasks. Only supply token_budget when explicitly requested. Fails if an unfinished goal exists.", map[string]any{"objective": map[string]any{"type": "string", "maxLength": 4000}, "token_budget": map[string]any{"type": "integer", "minimum": 1}}, "objective"),
		spec("update_goal", "Mark complete only after verifying every objective requirement against current evidence; report final consumed tokens when budgeted. Mark blocked only if the same genuine blocker recurred for at least three consecutive goal turns and no meaningful safe action remains. After resume restart the blocked audit. Do not mark complete because time or budget ran out. Only the user/system can pause, resume, edit or clear goals.", map[string]any{"status": map[string]any{"type": "string", "enum": []string{"complete", "blocked"}}}, "status"),
	}
}

func (e *Executor) goalTool(name string, args map[string]any) (string, error) {
	if e.Goals == nil {
		return "", fmt.Errorf("goal state unavailable")
	}
	var err error
	switch name {
	case "get_goal":
		if len(args) != 0 {
			return "", fmt.Errorf("get_goal accepts no arguments")
		}
	case "create_goal":
		var request struct {
			Objective string  `json:"objective"`
			Budget    *uint64 `json:"token_budget"`
		}
		b, _ := json.Marshal(args)
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if err = d.Decode(&request); err != nil {
			return "", err
		}
		var budget uint64
		if request.Budget != nil {
			budget = *request.Budget
			if budget == 0 {
				return "", fmt.Errorf("token_budget must be positive")
			}
		}
		err = e.Goals.Create(request.Objective, budget, false)
	case "update_goal":
		if len(args) != 1 {
			return "", fmt.Errorf("update_goal accepts only status")
		}
		err = e.Goals.ModelStatus(goal.Status(str(args, "status")))
	}
	if err != nil {
		return "", err
	}
	s := e.Goals.Get()
	result := map[string]any{"goal": s}
	if s != nil && s.TokenBudget > 0 {
		result["remaining_tokens"] = s.TokenBudget - min(s.TokenBudget, s.TokensUsed)
		if s.Status == goal.Complete {
			result["completion_report"] = fmt.Sprintf("Goal complete. Tokens consumed: %d / %d.", s.TokensUsed, s.TokenBudget)
		}
	}
	b, err := json.Marshal(result)
	return string(b), err
}
