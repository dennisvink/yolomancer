package provider

// RepairToolResults closes incomplete tool exchanges before history is saved or
// sent to Bedrock. Missing results describe an unknown outcome, never success:
// the process may have changed files before cancellation interrupted recording.
// Existing messages and recorded results are preserved without mutating inputs.
func RepairToolResults(messages []any, completed map[string]map[string]any) []any {
	out := make([]any, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		raw := messages[i]
		out = append(out, raw)
		message, _ := raw.(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		blocks, _ := message["content"].([]any)
		var ids []string
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			use, _ := block["toolUse"].(map[string]any)
			if id, _ := use["toolUseId"].(string); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		var next map[string]any
		var content []any
		if i+1 < len(messages) {
			candidate, _ := messages[i+1].(map[string]any)
			if candidate["role"] == "user" {
				next = candidate
				content, _ = next["content"].([]any)
			}
		}
		present := map[string]bool{}
		for _, raw := range content {
			block, _ := raw.(map[string]any)
			result, _ := block["toolResult"].(map[string]any)
			id, _ := result["toolUseId"].(string)
			present[id] = true
		}
		var missing []any
		for _, id := range ids {
			if present[id] {
				continue
			}
			result := completed[id]
			if result == nil {
				result = map[string]any{"ok": false, "error": "Tool execution was interrupted or its result was not recorded. The outcome is unknown; inspect current state before retrying."}
			}
			status := "success"
			if ok, exists := result["ok"].(bool); exists && !ok {
				status = "error"
			}
			missing = append(missing, map[string]any{"toolResult": map[string]any{"toolUseId": id, "status": status, "content": []any{map[string]any{"json": result}}}})
		}
		if len(missing) == 0 {
			continue
		}
		repaired := map[string]any{"role": "user"}
		if next != nil {
			for key, value := range next {
				repaired[key] = value
			}
			i++
		}
		repaired["content"] = append(missing, content...)
		out = append(out, repaired)
	}
	return out
}
