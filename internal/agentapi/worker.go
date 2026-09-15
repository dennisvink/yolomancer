package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/tools"
)

type Work struct {
	Registered      Registered
	Prompt, Profile string
}
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}
type ConfigLoader func(context.Context, string) (*model.Config, error)

// Worker serves one request in its own process. stdout is NDJSON, never CLI text.
func Worker(ctx context.Context, input io.Reader, output io.Writer, load ConfigLoader) error {
	var work Work
	d := json.NewDecoder(io.LimitReader(input, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&work); err != nil {
		return fmt.Errorf("invalid API worker request")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &eventSink{encode: json.NewEncoder(output), cancel: cancel, budget: work.Registered.Agent.Limits.TokenBudget}
	cfg, err := load(ctx, work.Profile)
	if err != nil {
		s.emit("run.failed", map[string]any{"code": "configuration_error", "message": "Unable to load model credentials; check the service account configuration."})
		return nil
	}
	// CLI remembered policies must never grant extra permissions to an API agent.
	cfg.CommandApprovalRules = nil
	cfg.NetworkApprovalRules = nil
	cfg.ProjectProfiles = nil
	a := app.New(cfg, false)
	// API turns have no persistent user goal, sidecar files or CLI session state.
	a.Goals = goal.New(nil, nil)
	a.Executor = &tools.Executor{Headless: true, Config: cfg, Mode: model.ModeDefault,
		Policy: work.Registered.Policy, PythonTools: work.Registered.Python,
		ToolSpecs: work.Registered.Specs, AllowedTools: work.Registered.Allowed}
	defer a.Processes.StopAll()
	text, err := a.RunTurn(ctx, work.Prompt, s)
	var denied *tools.PermissionDenied
	switch {
	case errors.As(err, &denied):
		s.emit("run.blocked", map[string]any{"code": "permission_denied", "message": denied.Error()})
	case s.limitHit:
		s.emit("run.failed", map[string]any{"code": "token_budget_exceeded", "message": "Run token budget reached."})
	case ctx.Err() != nil:
		s.emit("run.cancelled", map[string]any{"code": "cancelled"})
	case err != nil:
		s.emit("run.failed", map[string]any{"code": "agent_error", "message": err.Error()})
	default:
		s.emit("run.completed", map[string]any{"text": text})
	}
	return nil
}

type eventSink struct {
	mu           sync.Mutex
	encode       *json.Encoder
	cancel       context.CancelFunc
	budget, used uint64
	limitHit     bool
}

func (s *eventSink) emit(kind string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.encode.Encode(Event{kind, data}) != nil {
		s.cancel()
	}
}
func (s *eventSink) ReasoningDelta(v string) { s.emit("reasoning.delta", map[string]string{"text": v}) }
func (s *eventSink) AssistantDelta(v string) { s.emit("assistant.delta", map[string]string{"text": v}) }
func (s *eventSink) AssistantMessage(v string) {
	s.emit("assistant.message", map[string]string{"text": v})
}
func (s *eventSink) AssistantDone()            { s.emit("assistant.done", nil) }
func (s *eventSink) ToolCall(v model.ToolCall) { s.emit("tool.started", v) }
func (s *eventSink) ToolResult(v model.ToolCall, result string) {
	var data any
	if json.Unmarshal([]byte(result), &data) != nil {
		data = result
	}
	s.emit("tool.completed", map[string]any{"call_id": v.CallID, "name": v.Name, "result": data})
	if object, ok := data.(map[string]any); ok && object["code"] == "permission_denied" {
		s.emit("permission_denied", map[string]any{"call_id": v.CallID, "name": v.Name, "error": object["error"]})
	}
}
func (s *eventSink) Info(v string) { s.emit("info", map[string]string{"text": v}) }
func (s *eventSink) Debug(string)  {}
func (s *eventSink) Usage(v model.Usage) {
	s.emit("usage.updated", v)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used += v.InputTokens + v.OutputTokens + v.CacheReadInputTokens + v.CacheWriteInputTokens
	if s.budget > 0 && s.used >= s.budget {
		s.limitHit = true
		s.cancel()
	}
}
