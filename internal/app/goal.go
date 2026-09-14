package app

import (
	"context"
	"os"
	"strings"

	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/session"
)

func goalStop(ctx context.Context, err error) goal.Status {
	if ctx.Err() != nil {
		return goal.Paused
	}
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"usage_limit", "usage limit", "insufficient_quota", "servicequotaexceeded", "quota exceeded"} {
		if strings.Contains(message, marker) {
			return goal.UsageLimited
		}
	}
	return goal.Blocked
}

type goalSink struct {
	model.Sink
	goals   *goal.Manager
	bedrock bool
}

func (s *goalSink) Usage(u model.Usage) {
	// Bedrock reports uncached input separately; Responses includes cache hits
	// in input_tokens. Cache writes consume goal budget in either transport.
	input := u.InputTokens - min(u.InputTokens, u.CacheReadInputTokens)
	if s.bedrock {
		input = u.InputTokens + u.CacheWriteInputTokens
	}
	if err := s.goals.Account(input + u.OutputTokens); err != nil {
		s.Sink.Info(err.Error())
	}
	s.Sink.Usage(u)
}

// Run executes a headless request and any goal continuations. The terminal UI
// schedules the same turns individually so user input always gets priority.
func (a *App) Run(ctx context.Context, prompt string, sink model.Sink) (string, error) {
	// Establish a session before the first tool can persist a goal sidecar.
	cwd, _ := os.Getwd()
	initial := &model.SessionSnapshot{Version: 1, SessionID: a.SessionID, CWD: &cwd, Goal: a.Goals.Get(), BedrockMessages: a.RawMessages(), ContextBudget: a.ContextBudget(), CollaborationMode: a.Mode, History: []string{prompt}}
	session.Touch(initial)
	if err := session.Write(initial); err != nil {
		return "", err
	}
	text, err := a.RunTurn(ctx, prompt, sink)
	for {
		g := a.Goals.Get()
		if g != nil {
			cwd, _ := os.Getwd()
			snapshot := &model.SessionSnapshot{Version: 1, SessionID: a.SessionID, CWD: &cwd, Goal: g, BedrockMessages: a.RawMessages(), ContextBudget: a.ContextBudget(), CollaborationMode: a.Mode, History: []string{prompt}}
			session.Touch(snapshot)
			if saveErr := session.Write(snapshot); saveErr != nil {
				_ = a.Goals.SetStatus(goal.Paused)
				return text, saveErr
			}
		}
		if err != nil || ctx.Err() != nil || g == nil || g.Status != goal.Active {
			break
		}
		text, err = a.RunGoalTurn(ctx, g.ID, sink)
	}
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return text, err
}
