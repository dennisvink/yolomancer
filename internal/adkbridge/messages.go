package adkbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func seedSession(ctx context.Context, service session.Service, destination session.Session, messages []any) error {
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content := messageToContent(message)
		if content == nil || len(content.Parts) == 0 {
			continue
		}
		event := session.NewEvent(ctx, "restored")
		event.LLMResponse = adkmodel.LLMResponse{Content: content, TurnComplete: true}
		event.Author = authorFor(content)
		if err := service.AppendEvent(ctx, destination, event); err != nil {
			return fmt.Errorf("seed ADK session: %w", err)
		}
	}
	return nil
}

func authorFor(content *genai.Content) string {
	if content != nil && content.Role == genai.RoleUser {
		for _, part := range content.Parts {
			if part != nil && part.FunctionResponse != nil {
				return agentName
			}
		}
		return "user"
	}
	return agentName
}

func sessionMessages(value session.Session) []any {
	var out []any
	for event := range value.Events().All() {
		if event == nil || event.Partial || event.Content == nil || len(event.Content.Parts) == 0 {
			continue
		}
		if message := contentToMessage(event.Content); message != nil {
			out = append(out, message)
		}
	}
	return out
}

func contentsToMessages(contents []*genai.Content) []any {
	out := make([]any, 0, len(contents))
	for _, content := range contents {
		if message := contentToMessage(content); message != nil {
			out = append(out, message)
		}
	}
	return out
}

func contentToMessage(content *genai.Content) map[string]any {
	if content == nil {
		return nil
	}
	role := "user"
	if content.Role == genai.RoleModel {
		role = "assistant"
	}
	blocks := make([]any, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part == nil {
			continue
		}
		switch {
		case part.FunctionCall != nil:
			blocks = append(blocks, map[string]any{"toolUse": map[string]any{
				"toolUseId": part.FunctionCall.ID,
				"name":      part.FunctionCall.Name,
				"input":     part.FunctionCall.Args,
			}})
		case part.FunctionResponse != nil:
			response := part.FunctionResponse.Response
			status := "success"
			if ok, present := response["ok"].(bool); present && !ok {
				status = "error"
			}
			blocks = append(blocks, map[string]any{"toolResult": map[string]any{
				"toolUseId": part.FunctionResponse.ID,
				"content":   []any{map[string]any{"json": response}},
				"status":    status,
			}})
		case part.Thought:
			reasoning := map[string]any{}
			if part.Text != "" || len(part.ThoughtSignature) > 0 {
				reasoning["reasoningText"] = map[string]any{"text": part.Text, "signature": string(part.ThoughtSignature)}
			}
			if encoded, ok := part.PartMetadata["bedrock_redacted_content"].(string); ok {
				if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
					values := make([]any, len(decoded))
					for i, value := range decoded {
						values[i] = float64(value)
					}
					reasoning["redactedContentBytes"] = values
				}
			}
			if len(reasoning) > 0 {
				blocks = append(blocks, map[string]any{"reasoningContent": reasoning})
			}
		case part.Text != "":
			blocks = append(blocks, map[string]any{"text": part.Text})
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return map[string]any{"role": role, "content": blocks}
}

func messageToContent(message map[string]any) *genai.Content {
	role := genai.RoleUser
	if message["role"] == "assistant" || message["role"] == "model" {
		role = genai.RoleModel
	}
	content := &genai.Content{Role: role}
	for _, raw := range anySlice(message["content"]) {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if text, ok := item["text"].(string); ok {
			content.Parts = append(content.Parts, &genai.Part{Text: text})
			continue
		}
		if use, ok := item["toolUse"].(map[string]any); ok {
			args, _ := use["input"].(map[string]any)
			content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{
				ID: stringValue(use, "toolUseId"), Name: stringValue(use, "name"), Args: args,
			}})
			continue
		}
		if result, ok := item["toolResult"].(map[string]any); ok {
			response := map[string]any{}
			items := anySlice(result["content"])
			if len(items) > 0 {
				if object, ok := items[0].(map[string]any); ok {
					if value, ok := object["json"].(map[string]any); ok {
						response = value
					} else if value, exists := object["json"]; exists {
						response = map[string]any{"result": value}
					} else if text, ok := object["text"].(string); ok {
						response = resultObject(text)
					}
				}
			}
			content.Parts = append(content.Parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID: stringValue(result, "toolUseId"), Response: response,
			}})
			continue
		}
		if reasoning, ok := item["reasoningContent"].(map[string]any); ok {
			part := &genai.Part{Thought: true}
			if value, ok := reasoning["reasoningText"].(map[string]any); ok {
				part.Text = stringValue(value, "text")
				part.ThoughtSignature = []byte(stringValue(value, "signature"))
			}
			if values := anySlice(reasoning["redactedContentBytes"]); len(values) > 0 {
				data := make([]byte, 0, len(values))
				for _, raw := range values {
					switch value := raw.(type) {
					case float64:
						data = append(data, byte(value))
					case json.Number:
						n, _ := value.Int64()
						data = append(data, byte(n))
					}
				}
				part.PartMetadata = map[string]any{"bedrock_redacted_content": base64.StdEncoding.EncodeToString(data)}
			}
			content.Parts = append(content.Parts, part)
		}
	}
	return content
}

func anySlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}
