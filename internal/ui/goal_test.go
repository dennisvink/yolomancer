package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestGoalCommandsAndContinuation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newModel(app.New(&model.Config{}, false), nil)
	cmd := m.submit("/goal Finish the tests")
	if cmd == nil {
		t.Fatal("goal did not schedule a turn")
	}
	msg := cmd().(goalContinueMsg)
	if !strings.Contains(m.statusLine(), "goal=active") {
		t.Fatal(m.statusLine())
	}
	turn := m.startGoal(msg.id)
	if turn == nil || !m.running {
		t.Fatal("goal did not start")
	}
	if m.startGoal(msg.id) != nil {
		t.Fatal("duplicate turn scheduled")
	}
	// Do not execute the command: that would contact Bedrock.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.app.Goals.Get().Status != goal.Paused {
		t.Fatal("Esc failed to pause goal")
	}
	m.Update(turnDoneMsg{err: context.Canceled})
	if m.continueGoal() != nil {
		t.Fatal("interrupted goal restarted")
	}
	if m.submit("/goal resume") == nil {
		t.Fatal("resume did not schedule")
	}
	m.goalCommand("edit Finish tests and verify")
	if m.app.Goals.Get().Objective != "Finish tests and verify" {
		t.Fatal(m.app.Goals.Get())
	}
	m.goalCommand("budget 100")
	if m.app.Goals.Get().TokenBudget != 100 {
		t.Fatal("budget not set")
	}
	m.goalCommand("Other objective")
	if m.app.Goals.Get().Objective == "Other objective" {
		t.Fatal("replacement lacked confirmation")
	}
	if m.continueGoal() != nil {
		t.Fatal("old goal ran while replacement confirmation was pending")
	}
	m.goalCommand("Other objective")
	if m.app.Goals.Get().Objective != "Other objective" {
		t.Fatal("confirmation did not replace")
	}
	if m.startGoal(msg.id) != nil {
		t.Fatal("stale continuation started replacement goal")
	}
	m.goalCommand("clear")
	if m.continueGoal() != nil {
		t.Fatal("cleared goal continued")
	}
	m.goalCommand("--budget 500 Finish with tests")
	if g := m.app.Goals.Get(); g.TokenBudget != 500 || g.Objective != "Finish with tests" {
		t.Fatal(g)
	}
	m.goalCommand("clear old build artifacts")
	if m.app.Goals.Get() == nil {
		t.Fatal("objective interpreted as clear command")
	}
}

func TestGoalCompletionQueuesAndResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := app.New(&model.Config{}, false)
	_ = a.Goals.Create("work", 0, false)
	m := newModel(a, nil)
	m.running = true
	m.workingStarted = time.Now()
	_, cmd := m.Update(turnDoneMsg{})
	if cmd == nil {
		t.Fatal("normal final answer stopped active goal")
	}
	m.planPrompt = 0
	if m.continueGoal() != nil {
		t.Fatal("continued through plan approval")
	}
	m.planPrompt = -1
	m.queued = []string{"user message"}
	if m.continueGoal() != nil {
		t.Fatal("goal overtook queued user")
	}
	m.queued = nil
	_ = a.Goals.ModelStatus(goal.Complete)
	if m.continueGoal() != nil {
		t.Fatal("completed goal continued")
	}
	for _, status := range []goal.Status{goal.Active, goal.Paused, goal.Blocked, goal.BudgetLimited} {
		s := &model.SessionSnapshot{SessionID: "restored", Goal: &goal.State{ID: "g", Objective: "work", Status: status}}
		m := newModel(app.Restore(&model.Config{}, false, s), s)
		if (m.continueGoal() != nil) != (status == goal.Active) {
			t.Fatalf("bad restore for %s", status)
		}
	}
}
