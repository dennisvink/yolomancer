package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dennisvink/yolomancer/internal/model"
)

type OpenAI struct {
	Client                                            *http.Client
	BaseURL, APIKey, Model, SessionID, InstallationID string
	Debug                                             bool
}
type ResponseOutcome struct {
	Response      map[string]any
	SawDelta      bool
	StreamedCalls map[string]bool
}

func (o *OpenAI) Create(ctx context.Context, input any, specs []map[string]any, sink model.Sink) (ResponseOutcome, error) {
	body := map[string]any{"model": o.Model, "input": input, "tools": specs, "store": false, "stream": true, "debug": o.Debug, "session_id": o.SessionID, "client_surface": "cli", "client_id": o.InstallationID, "client_metadata": map[string]any{"client": "yolomancer", "surface": "cli"}}
	raw, _ := json.Marshal(body)
	url := strings.TrimRight(o.BaseURL, "/") + "/responses"
	sink.Debug(fmt.Sprintf("POST %s body=%s", url, truncate(string(raw), 4000)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return ResponseOutcome{}, err
	}
	req.Header.Set("authorization", "Bearer "+strings.TrimSpace(o.APIKey))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("x-yolomancer-client", "yolomancer")
	req.Header.Set("x-yolomancer-client-surface", "cli")
	resp, err := o.Client.Do(req)
	if err != nil {
		return ResponseOutcome{}, fmt.Errorf("request /v1/responses: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return ResponseOutcome{}, fmt.Errorf("/v1/responses failed: HTTP %d: %s", resp.StatusCode, formatHTTPError(b))
	}
	p := SSEParser{}
	out := ResponseOutcome{StreamedCalls: map[string]bool{}}
	var items []any
	var text strings.Builder
	names := map[string]string{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		for _, ev := range p.Push(scanner.Text() + "\n") {
			var payload map[string]any
			if strings.TrimSpace(ev.Data) == "" {
				continue
			}
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				return out, fmt.Errorf("parse SSE payload for event `%s`: %w", ev.Event, err)
			}
			typ, _ := payload["type"].(string)
			if typ == "" {
				typ = ev.Event
			}
			switch typ {
			case "response.output_text.delta":
				if d, ok := payload["delta"].(string); ok {
					out.SawDelta = true
					text.WriteString(d)
					sink.AssistantDelta(d)
				}
			case "response.output_item.done":
				if item, ok := payload["item"].(map[string]any); ok {
					items = append(items, item)
					if c, ok := toolCall(item); ok {
						out.StreamedCalls[c.CallID] = true
						names[c.CallID] = c.Name
						sink.ToolCall(c)
					} else if id, output, ok := toolOutput(item); ok {
						sink.ToolResult(model.ToolCall{CallID: id, Name: names[id], Arguments: map[string]any{}}, output)
					}
				}
			case "response.completed", "response.done":
				if r, ok := payload["response"].(map[string]any); ok {
					out.Response = r
				}
			case "response.failed", "error":
				return out, fmt.Errorf("%s", errorMessage(payload))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if out.Response == nil {
		out.Response = map[string]any{"status": "completed", "output": items, "output_text": text.String()}
	}
	if arr, ok := out.Response["output"].([]any); !ok || len(arr) == 0 {
		out.Response["output"] = items
	}
	if _, ok := out.Response["output_text"]; !ok {
		out.Response["output_text"] = text.String()
	}
	return out, nil
}

func ExtractCalls(r map[string]any) ([]model.ToolCall, error) {
	var out []model.ToolCall
	items, _ := r["output"].([]any)
	for _, v := range items {
		if m, ok := v.(map[string]any); ok {
			if c, ok := toolCall(m); ok {
				out = append(out, c)
			}
		}
	}
	return out, nil
}
func OutputText(r map[string]any) string {
	if s, ok := r["output_text"].(string); ok && s != "" {
		return s
	}
	items, _ := r["output"].([]any)
	var b strings.Builder
	for _, v := range items {
		m, _ := v.(map[string]any)
		content, _ := m["content"].([]any)
		for _, c := range content {
			x, _ := c.(map[string]any)
			if x["type"] == "output_text" {
				if s, ok := x["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	return b.String()
}
func ExtractUsage(r map[string]any) model.Usage {
	u, _ := r["yolomancer_usage"].(map[string]any)
	if u == nil {
		u, _ = r["vibecode_usage"].(map[string]any)
	}
	input, output := uintvAny(u, "input_tokens", "inputTokens"), uintvAny(u, "output_tokens", "outputTokens")
	total := uintvAny(u, "total_tokens", "totalTokens", "tokens_used")
	if total == 0 {
		total = input + output
	}
	reason := uintvAny(u, "reasoning_tokens", "reasoningTokens")
	var reasoning *uint64
	if reason > 0 {
		reasoning = &reason
	}
	return model.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total, CacheReadInputTokens: uintvAny(u, "cache_read_input_tokens", "cacheReadInputTokens"), CacheWriteInputTokens: uintvAny(u, "cache_write_input_tokens", "cacheWriteInputTokens"), ReasoningTokens: reasoning}
}
func toolCall(m map[string]any) (model.ToolCall, bool) {
	if m["type"] != "function_call" {
		return model.ToolCall{}, false
	}
	id, _ := m["call_id"].(string)
	name, _ := m["name"].(string)
	args := map[string]any{}
	switch v := m["arguments"].(type) {
	case string:
		_ = json.Unmarshal([]byte(v), &args)
	case map[string]any:
		args = v
	}
	return model.ToolCall{CallID: id, Name: name, Arguments: args}, id != "" && name != ""
}
func toolOutput(m map[string]any) (string, string, bool) {
	if m["type"] != "function_call_output" {
		return "", "", false
	}
	id, _ := m["call_id"].(string)
	o, _ := m["output"].(string)
	return id, o, id != ""
}
func formatHTTPError(b []byte) string {
	var v map[string]any
	if json.Unmarshal(b, &v) == nil {
		if e, ok := v["error"].(map[string]any); ok {
			if s, ok := e["message"].(string); ok {
				return s
			}
		}
	}
	return truncate(strings.TrimSpace(string(b)), 1000)
}
func errorMessage(v map[string]any) string {
	if e, ok := v["error"].(map[string]any); ok {
		if s, ok := e["message"].(string); ok {
			return s
		}
	}
	if s, ok := v["message"].(string); ok {
		return s
	}
	return "Responses request failed upstream."
}
func uintv(m map[string]any, k string) uint64 {
	if v, ok := m[k].(float64); ok {
		return uint64(v)
	}
	return 0
}
func uintvAny(m map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if v := uintv(m, k); v > 0 {
			return v
		}
	}
	return 0
}
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "...(truncated)"
}
