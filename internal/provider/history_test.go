package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRepairInterruptedToolHistory(t *testing.T) {
	call := func(id string) any {
		return map[string]any{"toolUse": map[string]any{"toolUseId": id, "name": "exec_command", "input": map[string]any{}}}
	}
	result := map[string]any{"toolResult": map[string]any{"toolUseId": "done", "status": "success", "content": []any{map[string]any{"text": "built"}}}}
	for _, trailingUser := range []bool{false, true} {
		messages := []any{map[string]any{"role": "assistant", "content": []any{call("done"), call("missing")}}}
		if trailingUser {
			messages = append(messages, map[string]any{"role": "user", "content": []any{result, map[string]any{"text": "continue"}}})
		}
		before, _ := json.Marshal(messages)
		got := RepairToolResults(messages, nil)
		after, _ := json.Marshal(messages)
		if string(before) != string(after) {
			t.Fatal("repair mutated input")
		}
		if len(got) != 2 {
			t.Fatalf("unexpected repaired history: %#v", got)
		}
		blocks := got[1].(map[string]any)["content"].([]any)
		ids := map[string]int{}
		for _, raw := range blocks {
			block := raw.(map[string]any)
			if r, ok := block["toolResult"].(map[string]any); ok {
				ids[r["toolUseId"].(string)]++
			}
		}
		if ids["done"] != 1 || ids["missing"] != 1 {
			t.Fatalf("unmatched results: %v", ids)
		}
		if trailingUser && !reflect.DeepEqual(blocks[1], result) {
			t.Fatal("existing result was changed")
		}
		if !reflect.DeepEqual(got, RepairToolResults(got, nil)) {
			t.Fatal("repair is not idempotent")
		}
		encoded, _ := json.Marshal(got)
		if !strings.Contains(string(encoded), "outcome is unknown") {
			t.Fatal("missing result claimed a known outcome")
		}
	}
}

func TestRepairUsesCompletedResultAndPreservesLaterMessages(t *testing.T) {
	call := map[string]any{"role": "assistant", "content": []any{map[string]any{"toolUse": map[string]any{"toolUseId": "built", "name": "exec_command"}}}}
	next := map[string]any{"role": "assistant", "content": []any{map[string]any{"text": "Next turn"}}}
	completed := map[string]map[string]any{"built": {"ok": true, "session_id": 123, "output": "still running"}}
	got := RepairToolResults([]any{call, next}, completed)
	if len(got) != 3 || !reflect.DeepEqual(got[2], next) {
		t.Fatal("later history was lost")
	}
	result := got[1].(map[string]any)["content"].([]any)[0].(map[string]any)["toolResult"].(map[string]any)
	if result["status"] != "success" || !reflect.DeepEqual(result["content"].([]any)[0].(map[string]any)["json"], completed["built"]) {
		t.Fatal("completed result was lost")
	}
}
