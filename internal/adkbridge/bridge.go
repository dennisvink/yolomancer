// Package adkbridge adapts Yolomancer's Bedrock transport and guarded tool
// executor to Google ADK. ADK owns the agent loop and session event flow while
// Yolomancer retains its wire format, security policy, and terminal UX.
package adkbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
	yolotools "github.com/dennisvink/yolomancer/internal/tools"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	adkmodel "google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

const (
	appName   = "yolomancer"
	userID    = "local"
	agentName = "yolomancer"
)

// Config contains one ADK turn's dependencies and retained Bedrock history.
type Config struct {
	ModelConfig  *model.Config
	Mode         model.CollaborationMode
	SessionID    string
	Messages     []any
	Executor     *yolotools.Executor
	Sink         model.Sink
	llm          adkmodel.LLM
	Budget       *model.ContextBudget
	compactor    historyCompactor
	continuation bool
}

type historyCompactor interface {
	CompactHistory(context.Context, []any, model.CollaborationMode) ([]any, *model.Usage, error)
}

var errNeedsCompaction = errors.New("context reached auto-compaction threshold")

// RunTurn executes a turn through an ADK LLM agent and returns
// Bedrock messages for the existing session persistence layer.
func RunTurn(ctx context.Context, prompt string, cfg Config) (string, []any, error) {
	if cfg.ModelConfig == nil || cfg.Sink == nil || cfg.Executor == nil {
		return "", cfg.Messages, fmt.Errorf("ADK bridge requires model config, executor, and sink")
	}
	if cfg.Budget == nil {
		cfg.Budget = &model.ContextBudget{}
	}
	if cfg.compactor == nil {
		cfg.compactor = &provider.Bedrock{Config: cfg.ModelConfig, Client: &http.Client{}}
	}
	for {
		text, messages, err := runSegment(ctx, prompt, cfg)
		if !errors.Is(err, errNeedsCompaction) && !provider.ContextLimitError(err) {
			return text, messages, err
		}
		if ctx.Err() != nil {
			return "", messages, ctx.Err()
		}
		cfg.Sink.Info("Compacting context (900,000-token threshold; 1,000,000-token model window)...")
		compacted, usage, compactErr := cfg.compactor.CompactHistory(ctx, messages, cfg.Mode)
		if compactErr != nil {
			return "", messages, compactErr
		}
		if provider.EstimateContextTokens(compacted, nil, cfg.Mode) >= provider.EstimateContextTokens(messages, nil, cfg.Mode) {
			return "", messages, fmt.Errorf("compaction did not reduce context; original history retained")
		}
		*cfg.Budget = model.ContextBudget{}
		cfg.Messages = compacted
		cfg.continuation = true
		if usage != nil {
			cfg.Sink.Usage(*usage)
		}
		cfg.Sink.Info("Context compacted. Continuing from the checkpoint.")
	}
}

func runSegment(ctx context.Context, prompt string, cfg Config) (string, []any, error) {
	if cfg.Executor == nil || cfg.Sink == nil || cfg.ModelConfig == nil {
		return "", cfg.Messages, fmt.Errorf("ADK bridge requires model config, executor, and sink")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg.Messages = provider.RepairToolResults(cfg.Messages, nil)
	state := &turnState{sink: cfg.Sink, executor: cfg.Executor, malformed: map[string]int{}, results: map[string]map[string]any{}, cancel: cancel}
	llm := cfg.llm
	if llm == nil {
		llm = &bedrockLLM{bedrock: &provider.Bedrock{Config: cfg.ModelConfig, Client: &http.Client{}}, mode: cfg.Mode, state: state, budget: cfg.Budget}
	}
	adkTools, err := buildTools(yolotools.Specs(cfg.Mode, cfg.ModelConfig), state)
	if err != nil {
		return "", cfg.Messages, err
	}
	instruction := systemInstruction(cfg.Mode)
	root, err := llmagent.New(llmagent.Config{
		Name:                agentName,
		Description:         "Agentic coding assistant for the local terminal and workspace.",
		Model:               llm,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) { return instruction, nil },
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: model.BedrockMaxTokens,
		},
		Tools: adkTools,
	})
	if err != nil {
		return "", cfg.Messages, fmt.Errorf("create ADK agent: %w", err)
	}

	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: appName, UserID: userID, SessionID: cfg.SessionID})
	if err != nil {
		return "", cfg.Messages, fmt.Errorf("create ADK session: %w", err)
	}
	if err := seedSession(ctx, sessions, created.Session, cfg.Messages); err != nil {
		return "", cfg.Messages, err
	}
	r, err := runner.New(runner.Config{AppName: appName, Agent: directAgent{root}, SessionService: sessions})
	if err != nil {
		return "", cfg.Messages, fmt.Errorf("create ADK runner: %w", err)
	}

	var runErr error
	input := genai.NewContentFromText(prompt, genai.RoleUser)
	if cfg.continuation {
		input = nil
	}
	for event, err := range r.Run(ctx, userID, cfg.SessionID, input, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if err != nil {
			runErr = err
			break
		}
		if event == nil || event.Partial || event.Content == nil || event.Content.Role != genai.RoleModel {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionCall != nil {
				call := model.ToolCall{CallID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args}
				cfg.Sink.ToolCall(call)
			}
		}
		state.mu.Lock()
		state.text = responseText(event.Content)
		state.mu.Unlock()
		if event.CustomMetadata != nil {
			if usage, ok := event.CustomMetadata["yolomancer_usage"].(model.Usage); ok {
				cfg.Sink.Usage(usage)
			}
		}
		cfg.Sink.AssistantDone()
	}
	if fatal := state.fatalError(); fatal != nil {
		runErr = fatal
	} else if runErr == nil {
		runErr = ctx.Err()
	}

	messages := append([]any{}, cfg.Messages...)
	if got, err := sessions.Get(context.WithoutCancel(ctx), &session.GetRequest{AppName: appName, UserID: userID, SessionID: cfg.SessionID}); err == nil {
		messages = sessionMessages(got.Session)
	}
	state.mu.Lock()
	messages = provider.RepairToolResults(messages, state.results)
	state.mu.Unlock()
	if runErr != nil {
		return "", messages, runErr
	}
	text := state.finalText()
	if text == "" {
		text = "(no assistant text returned)"
		cfg.Sink.AssistantMessage(text)
	}
	return text, messages, nil
}

type turnState struct {
	mu        sync.Mutex
	execMu    sync.Mutex
	sink      model.Sink
	executor  *yolotools.Executor
	malformed map[string]int
	results   map[string]map[string]any
	text      string
	fatal     error
	cancel    context.CancelFunc
}

// Expose only the public Agent interface so Runner uses its direct agent path.
// ADK v2.3.0's synthetic dynamic workflow calls yield again with an error after
// makeEmit's yield returned false during cancellation. Its scheduler then reports
// a range-iterator panic. This single-agent CLI needs no workflow dispatch: the
// direct path retains the LLM/tool loop, streaming, and session persistence.
type directAgent struct{ agent.Agent }

func (s *turnState) finalText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text
}

func (s *turnState) fatalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fatal
}

func buildTools(specs []map[string]any, state *turnState) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(specs))
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		description, _ := spec["description"].(string)
		schema, err := schemaFrom(spec["parameters"])
		if err != nil {
			return nil, fmt.Errorf("tool %s schema: %w", name, err)
		}
		toolName := name
		t, err := functiontool.New[map[string]any, map[string]any](functiontool.Config{
			Name: name, Description: description, InputSchema: schema,
		}, func(ctx agent.Context, args map[string]any) (map[string]any, error) {
			call := model.ToolCall{CallID: ctx.FunctionCallID(), Name: toolName, Arguments: args}
			state.execMu.Lock()
			defer state.execMu.Unlock()
			if err := ctx.Err(); err != nil {
				return map[string]any{"ok": false, "error": "Tool was not started: " + err.Error()}, nil
			}
			result := state.executor.Execute(ctx, call)
			parsed := resultObject(result)
			state.mu.Lock()
			state.results[call.CallID] = parsed
			state.mu.Unlock()
			state.sink.ToolResult(call, result)
			if strings.Contains(result, "missing required string argument") {
				key := call.Name + ":" + result
				state.mu.Lock()
				state.malformed[key]++
				if state.malformed[key] >= 6 && state.fatal == nil {
					state.fatal = fmt.Errorf("model repeatedly produced an invalid tool call (%s); last result: %s", key, result)
					state.cancel()
				}
				state.mu.Unlock()
			}
			return parsed, nil
		})
		if err != nil {
			return nil, fmt.Errorf("create ADK tool %s: %w", name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

func schemaFrom(value any) (*jsonschema.Schema, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}

func resultObject(value string) map[string]any {
	var parsed any
	if json.Unmarshal([]byte(value), &parsed) != nil {
		return map[string]any{"text": value}
	}
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{"result": parsed}
}

type bedrockTransport interface {
	ConverseStream(context.Context, []any, model.CollaborationMode, []map[string]any, func(string), func(string)) (map[string]any, error)
}

type bedrockLLM struct {
	bedrock bedrockTransport
	mode    model.CollaborationMode
	state   *turnState
	budget  *model.ContextBudget
}

func (b *bedrockLLM) Name() string { return model.DefaultBedrock }

func (b *bedrockLLM) GenerateContent(ctx context.Context, req *adkmodel.LLMRequest, stream bool) iter.Seq2[*adkmodel.LLMResponse, error] {
	return func(yield func(*adkmodel.LLMResponse, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		messages := contentsToMessages(req.Contents)
		specs := requestSpecs(req)
		estimate := provider.EstimateContextTokens(messages, specs, b.mode)
		projected := estimate
		if b.budget != nil {
			growth := uint64(0)
			if estimate > b.budget.LastEstimate {
				growth = estimate - b.budget.LastEstimate
			}
			projected = max(projected, b.budget.KnownTokens+growth)
		}
		if projected >= provider.AutoCompactTokens || projected+uint64(model.BedrockMaxTokens) > provider.UsableContextTokens {
			yield(nil, errNeedsCompaction)
			return
		}
		active := true
		onText := func(delta string) {
			if active {
				b.state.sink.AssistantDelta(delta)
				active = yield(&adkmodel.LLMResponse{Content: contentWithText(delta, false), Partial: true}, nil)
				if !active {
					cancel()
				}
			}
		}
		onReasoning := func(delta string) {
			if active {
				b.state.sink.ReasoningDelta(delta)
				active = yield(&adkmodel.LLMResponse{Content: contentWithText(delta, true), Partial: true}, nil)
				if !active {
					cancel()
				}
			}
		}
		response, err := b.bedrock.ConverseStream(ctx, messages, b.mode, specs, onText, onReasoning)
		if !active {
			return
		}
		if err != nil {
			yield(nil, err)
			return
		}
		message, err := provider.BedrockMessage(response)
		if err != nil {
			yield(nil, err)
			return
		}
		usage := usageFromBedrock(response)
		if b.budget != nil {
			b.budget.KnownTokens = usage.InputTokens + usage.CacheReadInputTokens + usage.CacheWriteInputTokens + usage.OutputTokens
			b.budget.LastEstimate = estimate
		}
		if !active {
			return
		}
		yield(&adkmodel.LLMResponse{
			Content:       messageToContent(message),
			UsageMetadata: usageToADK(usage),
			TurnComplete:  true,
			CustomMetadata: map[string]any{
				"provider": "aws-bedrock", "stop_reason": response["stopReason"], "yolomancer_usage": usage,
			},
		}, nil)
	}
}

func responseText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

func contentWithText(text string, thought bool) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: text, Thought: thought}}}
}

func requestSpecs(req *adkmodel.LLMRequest) []map[string]any {
	if req == nil || req.Config == nil {
		return nil
	}
	var out []map[string]any
	for _, group := range req.Config.Tools {
		if group == nil {
			continue
		}
		for _, declaration := range group.FunctionDeclarations {
			if declaration == nil {
				continue
			}
			parameters := declaration.ParametersJsonSchema
			if parameters == nil && declaration.Parameters != nil {
				raw, _ := json.Marshal(declaration.Parameters)
				_ = json.Unmarshal(raw, &parameters)
			}
			out = append(out, map[string]any{"type": "function", "name": declaration.Name, "description": declaration.Description, "parameters": parameters})
		}
	}
	return out
}

func usageFromBedrock(response map[string]any) model.Usage {
	u, _ := response["usage"].(map[string]any)
	num := func(key string) uint64 {
		switch value := u[key].(type) {
		case float64:
			return uint64(value)
		case int:
			return uint64(value)
		case int64:
			return uint64(value)
		case json.Number:
			n, _ := value.Int64()
			return uint64(n)
		default:
			return 0
		}
	}
	input, output := num("inputTokens"), num("outputTokens")
	return model.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output, CacheReadInputTokens: num("cacheReadInputTokens"), CacheWriteInputTokens: num("cacheWriteInputTokens")}
}

func usageToADK(usage model.Usage) *genai.GenerateContentResponseUsageMetadata {
	return &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: int32(usage.InputTokens), CandidatesTokenCount: int32(usage.OutputTokens), TotalTokenCount: int32(usage.TotalTokens)}
}

func systemInstruction(mode model.CollaborationMode) string {
	items := provider.SystemPrompt(mode)
	if len(items) == 0 {
		return ""
	}
	if object, ok := items[0].(map[string]any); ok {
		text, _ := object["text"].(string)
		return text
	}
	return ""
}
