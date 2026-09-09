package adkbridge

import (
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/genai"
)

func TestBedrockMessageRoundTripPreservesReasoningAndToolCalls(t *testing.T) {
	original := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"reasoningContent": map[string]any{"reasoningText": map[string]any{"text": "inspect first", "signature": "signed"}}},
			map[string]any{"text": "I will inspect it."},
			map[string]any{"toolUse": map[string]any{"toolUseId": "call-1", "name": "read_file", "input": map[string]any{"path": "README.md", "reason": "inspect"}}},
		},
	}
	roundTrip := contentToMessage(messageToContent(original))
	if !reflect.DeepEqual(roundTrip, original) {
		want, _ := json.Marshal(original)
		got, _ := json.Marshal(roundTrip)
		t.Fatalf("round trip mismatch\nwant %s\n got %s", want, got)
	}
}

func TestBedrockMessageRoundTripPreservesRedactedReasoning(t *testing.T) {
	original := map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{"reasoningContent": map[string]any{
			"redactedContentBytes": []any{float64(1), float64(2), float64(255)},
		}}},
	}
	roundTrip := contentToMessage(messageToContent(original))
	if !reflect.DeepEqual(roundTrip, original) {
		t.Fatalf("round trip mismatch: %#v", roundTrip)
	}
}

func TestFunctionResponsePreservesToolResultIDAndErrorStatus(t *testing.T) {
	content := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: "call-2", Name: "read_file", Response: map[string]any{"ok": false, "error": "nope"},
	}}}}
	message := contentToMessage(content)
	blocks := message["content"].([]any)
	result := blocks[0].(map[string]any)["toolResult"].(map[string]any)
	if result["toolUseId"] != "call-2" || result["status"] != "error" {
		t.Fatalf("unexpected tool result: %#v", result)
	}
}

func TestResultObjectKeepsToolJSON(t *testing.T) {
	got := resultObject(`{"ok":true,"output":"done"}`)
	if got["ok"] != true || got["output"] != "done" {
		t.Fatalf("unexpected result: %#v", got)
	}
}
