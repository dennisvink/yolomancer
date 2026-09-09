package provider

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/dennisvink/yolomancer/internal/model"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSSEParserChunkBoundariesAndMultiline(t *testing.T) {
	p := SSEParser{}
	if got := p.Push("event: x\nda"); len(got) != 0 {
		t.Fatal(got)
	}
	got := p.Push("ta: one\ndata: two\n\n")
	if len(got) != 1 || got[0].Event != "x" || got[0].Data != "one\ntwo" {
		t.Fatalf("%#v", got)
	}
}
func TestOpenAIStreamingResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-yolomancer-client") != "yolomancer" {
			t.Error("missing client header")
		}
		body := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"hi\",\"output\":[]}}\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	s := &testSink{}
	o := OpenAI{Client: client, BaseURL: "http://example.test", APIKey: "x"}
	out, err := o.Create(context.Background(), "hello", nil, s)
	if err != nil {
		t.Fatal(err)
	}
	if !out.SawDelta || s.text != "hi" || OutputText(out.Response) != "hi" {
		t.Fatalf("%#v %#v", out, s)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBedrockConverseSignsCompatibleRequest(t *testing.T) {
	access, secret, region, modelID := "AKIDEXAMPLE", "secret", "us-east-1", "bedrock:global.anthropic.claude-opus-4-6-v1"
	cfg := &model.Config{AWSAccessKeyID: &access, AWSSecretAccessKey: &secret, AWSRegion: &region, BedrockModel: &modelID}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.Path, "/model/global.anthropic.claude-opus-4-6-v1/converse") {
			t.Errorf("bad path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			t.Error("request was not signed")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["toolConfig"] == nil || body["system"] == nil {
			t.Errorf("missing Bedrock fields: %#v", body)
		}
		response := `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"usage":{"inputTokens":1,"outputTokens":1}}`
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})}
	b := Bedrock{Config: cfg, Client: client}
	out, err := b.Converse(context.Background(), []any{map[string]any{"role": "user", "content": []any{map[string]any{"text": "hi"}}}}, model.ModeDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := BedrockMessage(out)
	if err != nil || BedrockText(m) != "ok" {
		t.Fatalf("%#v %v", out, err)
	}
}

func TestBedrockConverseStreamReconstructsTextAndToolUse(t *testing.T) {
	access, secret := "AKIDEXAMPLE", "secret"
	cfg := &model.Config{AWSAccessKeyID: &access, AWSSecretAccessKey: &secret}
	var stream bytes.Buffer
	for _, event := range [][2]string{{"messageStart", `{"role":"assistant"}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hello "}}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"world"}}`}, {"contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"call-1","name":"read_file"}}}`}, {"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"path\":"}}}`}, {"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"\"README.md\"}"}}}`}, {"contentBlockStop", `{"contentBlockIndex":1}`}, {"messageStop", `{"stopReason":"tool_use"}`}, {"metadata", `{"usage":{"inputTokens":4,"outputTokens":3}}`}} {
		stream.Write(eventFrame(event[0], []byte(event[1])))
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/converse-stream") {
			t.Errorf("bad stream path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(stream.Bytes())), Request: r}, nil
	})}
	var deltas strings.Builder
	b := Bedrock{Config: cfg, Client: client}
	out, err := b.ConverseStream(context.Background(), nil, model.ModeDefault, nil, func(s string) { deltas.WriteString(s) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := BedrockMessage(out)
	if err != nil {
		t.Fatal(err)
	}
	if deltas.String() != "hello world" || BedrockText(m) != "hello world" {
		t.Fatalf("text mismatch: %q %#v", deltas.String(), m)
	}
	calls := BedrockCalls(m)
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].Arguments["path"] != "README.md" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestBedrockReasoningRoundTripsIntoNextRequest(t *testing.T) {
	access, secret := "AKIDEXAMPLE", "secret"
	cfg := &model.Config{AWSAccessKeyID: &access, AWSSecretAccessKey: &secret}
	var stream bytes.Buffer
	events := [][2]string{
		{"messageStart", `{"role":"assistant"}`},
		{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"checking","signature":"signed"}}}`},
		{"contentBlockStop", `{"contentBlockIndex":0}`},
		{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"answer"}}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
	}
	for _, event := range events {
		stream.Write(eventFrame(event[0], []byte(event[1])))
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(stream.Bytes())), Header: http.Header{}, Request: r}, nil
	})}
	var reasoning strings.Builder
	b := Bedrock{Config: cfg, Client: client}
	out, err := b.ConverseStream(context.Background(), nil, model.ModeDefault, nil, nil, func(s string) { reasoning.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	m, err := BedrockMessage(out)
	if err != nil {
		t.Fatal(err)
	}
	content := m["content"].([]any)
	first := content[0].(map[string]any)["reasoningContent"].(map[string]any)["reasoningText"].(map[string]any)
	if first["signature"] != "signed" || reasoning.String() != "checking" {
		t.Fatalf("reasoning lost: %#v %q", m, reasoning.String())
	}
	wire := wireMessages([]any{m})
	wc := wire[0].(map[string]any)["content"].([]any)
	wr := wc[0].(map[string]any)["reasoningContent"].(map[string]any)
	if wr["reasoningText"].(map[string]any)["signature"] != "signed" {
		t.Fatalf("signature not replayed: %#v", wire)
	}
}

func TestBedrockSchemaConversion(t *testing.T) {
	got := cleanSchema(map[string]any{
		"type":                 []any{"null", "object"},
		"additionalProperties": false,
		"properties": map[string]any{
			"choice": map[string]any{"oneOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string"}}},
		},
	}).(map[string]any)
	if got["type"] != "object" {
		t.Fatalf("type conversion: %#v", got)
	}
	props := got["properties"].(map[string]any)
	if props["choice"].(map[string]any)["type"] != "integer" {
		t.Fatalf("oneOf conversion: %#v", got)
	}
	if _, exists := got["additionalProperties"]; exists {
		t.Fatalf("unsupported key retained: %#v", got)
	}
}

func eventFrame(event string, payload []byte) []byte {
	headers := eventHeader(":message-type", "event")
	headers = append(headers, eventHeader(":event-type", event)...)
	total := 16 + len(headers) + len(payload)
	out := make([]byte, total)
	binary.BigEndian.PutUint32(out[:4], uint32(total))
	binary.BigEndian.PutUint32(out[4:8], uint32(len(headers)))
	copy(out[12:], headers)
	copy(out[12+len(headers):], payload)
	binary.BigEndian.PutUint32(out[8:12], crc32.ChecksumIEEE(out[:8]))
	binary.BigEndian.PutUint32(out[len(out)-4:], crc32.ChecksumIEEE(out[:len(out)-4]))
	return out
}
func eventHeader(name, value string) []byte {
	out := []byte{byte(len(name))}
	out = append(out, []byte(name)...)
	out = append(out, 7, byte(len(value)>>8), byte(len(value)))
	return append(out, []byte(value)...)
}

type testSink struct{ text string }

func (*testSink) ReasoningDelta(string)             {}
func (s *testSink) AssistantDelta(v string)         { s.text += v }
func (*testSink) AssistantMessage(string)           {}
func (*testSink) AssistantDone()                    {}
func (*testSink) ToolCall(model.ToolCall)           {}
func (*testSink) ToolResult(model.ToolCall, string) {}
func (*testSink) Info(string)                       {}
func (*testSink) Debug(string)                      {}
func (*testSink) Usage(model.Usage)                 {}
