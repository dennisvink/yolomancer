package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/dennisvink/yolomancer/internal/adkbridge"
	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/security"
	"github.com/dennisvink/yolomancer/internal/tools"
	"github.com/google/uuid"
)

type App struct {
	mu            sync.RWMutex
	turnMu        sync.Mutex
	contextBudget model.ContextBudget
	Config        *model.Config
	Mode          model.CollaborationMode
	SessionID     string
	Messages      []any
	Processes     *process.Manager
	Debug         bool
	Approver      tools.Approver
}

func (a *App) Compact(ctx context.Context) (string, *model.Usage, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.RLock()
	history := append([]any(nil), a.Messages...)
	mode := a.Mode
	a.mu.RUnlock()
	if len(history) == 0 {
		return "No conversation context to compact.", nil, nil
	}
	b := &provider.Bedrock{Config: a.Config, Client: &http.Client{}}
	compacted, usage, err := b.CompactHistory(ctx, history, mode)
	if err != nil {
		return "", nil, err
	}
	a.mu.Lock()
	a.Messages = compacted
	a.contextBudget = model.ContextBudget{}
	a.mu.Unlock()
	return fmt.Sprintf("Context compacted: %d messages replaced with %d checkpoint messages.", len(history), len(compacted)), usage, nil
}

func (a *App) ContextBudget() model.ContextBudget {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.contextBudget
}

func New(cfg *model.Config, debug bool) *App {
	return &App{Config: cfg, Mode: model.ModeDefault, SessionID: uuid.NewString(), Processes: process.New(), Debug: debug}
}
func Restore(cfg *model.Config, debug bool, s *model.SessionSnapshot) *App {
	a := New(cfg, debug)
	a.SessionID = s.SessionID
	a.Mode = s.CollaborationMode
	a.contextBudget = s.ContextBudget
	for _, raw := range s.BedrockMessages {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			a.Messages = append(a.Messages, v)
		}
	}
	return a
}

func (a *App) SetMode(m model.CollaborationMode) { a.mu.Lock(); a.Mode = m; a.mu.Unlock() }
func (a *App) RunTurn(ctx context.Context, prompt string, sink model.Sink) (string, error) {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.mu.RLock()
	mode := a.Mode
	a.mu.RUnlock()
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err = security.CanonicalMissing(root)
	if err != nil {
		return "", err
	}
	executor := &tools.Executor{Config: a.Config, Policy: security.BuildPolicy(a.Config, root), Mode: mode, Processes: a.Processes, Approve: a.Approver, Debug: a.Debug}
	executor.AutoReview = func(reviewCtx context.Context, r tools.ApprovalRequest) (tools.ApprovalDecision, error) {
		reviewer := provider.Bedrock{Config: a.Config, Client: &http.Client{}}
		transcript := a.approvalTranscript()
		if transcript == "<no retained transcript>" {
			transcript = "user: " + truncateApprovalEvidence(prompt, 1000)
		}
		allow, rationale, err := reviewer.PermissionReview(reviewCtx, transcript, r.Kind, r.Command, r.Workdir, r.Reason)
		if err != nil {
			return tools.Deny, err
		}
		sink.Info(fmt.Sprintf("automatic arbitrage %s %s: %s", map[bool]string{true: "approved", false: "denied"}[allow], r.Kind, rationale))
		if allow {
			return tools.ApproveOnce, nil
		}
		return tools.Deny, nil
	}
	specs := tools.Specs(mode, a.Config)
	if appconfig.Provider(a.Config) == "openai" {
		return a.runOpenAI(ctx, prompt, sink, executor, specs)
	}
	return a.runBedrock(ctx, prompt, sink, executor)
}

func (a *App) approvalTranscript() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var lines []string
	for _, message := range a.Messages {
		object, ok := message.(map[string]any)
		if !ok {
			continue
		}
		role, _ := object["role"].(string)
		content, _ := object["content"].([]any)
		for _, rawItem := range content {
			item, _ := rawItem.(map[string]any)
			if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
				lines = append(lines, role+": "+truncateApprovalEvidence(text, 1000))
				continue
			}
			if tool, ok := item["toolUse"].(map[string]any); ok {
				encoded, _ := json.Marshal(tool["input"])
				lines = append(lines, fmt.Sprintf("tool: assistant requested tool `%v` with input %s", tool["name"], truncateApprovalEvidence(string(encoded), 2000)))
				continue
			}
			if result, ok := item["toolResult"].(map[string]any); ok {
				encoded, _ := json.Marshal(result)
				lines = append(lines, "tool: "+truncateApprovalEvidence(string(encoded), 2000))
			}
		}
	}
	if len(lines) == 0 {
		return "<no retained transcript>"
	}
	return strings.Join(lines, "\n")
}

func truncateApprovalEvidence(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func (a *App) runOpenAI(ctx context.Context, prompt string, sink model.Sink, e *tools.Executor, specs []map[string]any) (string, error) {
	base := appconfig.String(a.Config.BaseURL, model.DefaultBaseURL)
	install := appconfig.String(a.Config.InstallationID, "yolomancer")
	client := &provider.OpenAI{Client: &http.Client{}, BaseURL: base, APIKey: a.Config.APIKey, Model: model.DefaultBedrock, SessionID: a.SessionID, InstallationID: install, Debug: a.Debug}
	var input any = prompt
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		out, err := client.Create(ctx, input, specs, sink)
		if err != nil {
			return "", err
		}
		usage := provider.ExtractUsage(out.Response)
		sink.Usage(usage)
		calls, _ := provider.ExtractCalls(out.Response)
		if len(calls) == 0 {
			text := provider.OutputText(out.Response)
			if text == "" {
				text = "(no assistant text returned)"
			}
			if !out.SawDelta {
				sink.AssistantMessage(text)
			}
			sink.AssistantDone()
			return text, nil
		}
		sink.AssistantDone()
		outputs := make([]any, 0, len(calls))
		for _, c := range calls {
			if !out.StreamedCalls[c.CallID] {
				sink.ToolCall(c)
			}
			result := e.Execute(ctx, c)
			sink.ToolResult(c, result)
			outputs = append(outputs, map[string]any{"type": "function_call_output", "call_id": c.CallID, "output": result})
		}
		input = outputs
	}
}

func (a *App) runBedrock(ctx context.Context, prompt string, sink model.Sink, e *tools.Executor) (string, error) {
	a.mu.RLock()
	messages := append([]any{}, a.Messages...)
	mode := a.Mode
	budget := a.contextBudget
	a.mu.RUnlock()
	text, messages, err := adkbridge.RunTurn(ctx, prompt, adkbridge.Config{
		ModelConfig: a.Config,
		Mode:        mode,
		SessionID:   a.SessionID,
		Messages:    messages,
		Executor:    e,
		Sink:        sink,
		Budget:      &budget,
	})
	a.mu.Lock()
	a.Messages = messages
	a.contextBudget = budget
	a.mu.Unlock()
	return text, err
}

func (a *App) RawMessages() []json.RawMessage {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]json.RawMessage, 0, len(a.Messages))
	for _, v := range a.Messages {
		b, _ := json.Marshal(v)
		out = append(out, b)
	}
	return out
}
func bedrockUsage(v map[string]any) model.Usage {
	u, _ := v["usage"].(map[string]any)
	num := func(k string) uint64 {
		if n, ok := u[k].(float64); ok {
			return uint64(n)
		}
		return 0
	}
	input, output := num("inputTokens"), num("outputTokens")
	total := num("totalTokens")
	if total == 0 {
		total = input + output
	}
	reason := num("reasoningTokens")
	var reasoning *uint64
	if reason > 0 {
		reasoning = &reason
	}
	return model.Usage{InputTokens: input, OutputTokens: output, TotalTokens: total, CacheReadInputTokens: num("cacheReadInputTokens"), CacheWriteInputTokens: num("cacheWriteInputTokens"), ReasoningTokens: reasoning}
}
