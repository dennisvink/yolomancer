package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
)

type goalContinueMsg struct{ id string }

func (m *Model) continueGoal() tea.Cmd {
	g := m.app.Goals.Get()
	if g == nil || g.Status != goal.Active || m.running || len(m.queued) > 0 || m.planPrompt >= 0 || m.approval != nil {
		return nil
	}
	return func() tea.Msg { return goalContinueMsg{id: g.ID} }
}

func (m *Model) startGoal(id string) tea.Cmd {
	g := m.app.Goals.Get()
	if g == nil || g.ID != id || g.Status != goal.Active || m.running || len(m.queued) > 0 || m.planPrompt >= 0 || m.approval != nil {
		return nil
	}
	m.running = true
	m.interrupted = false
	m.workingStarted = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	return func() tea.Msg {
		text, err := m.app.RunGoalTurn(ctx, id, programSink{m.program})
		return turnDoneMsg{text, err}
	}
}

func (m *Model) pauseGoal() {
	if g := m.app.Goals.Get(); g != nil && g.Status == goal.Active {
		if err := m.app.Goals.SetStatus(goal.Paused); err != nil {
			m.add(model.EntryError, err.Error())
		}
	}
}

func (m *Model) goalCommand(args string) {
	args = strings.TrimSpace(args)
	command, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	// Bare control words are commands; phrases such as "clear old build
	// artifacts" are objectives, not permission to clear the existing goal.
	if rest != "" && (command == "clear" || command == "pause" || command == "resume") {
		command = ""
	}
	g := m.app.Goals.Get()
	var err error
	if args == "" {
		if g == nil {
			m.add(model.EntryInfo, "No goal set. /goal <objective>")
		} else {
			budget := "unlimited"
			if g.TokenBudget > 0 {
				budget = fmt.Sprint(g.TokenBudget)
			}
			m.add(model.EntryInfo, fmt.Sprintf("Goal: %s\n%s · tokens %d / %s · %s · %d turns", g.Objective, g.Status, g.TokensUsed, budget, compactDuration(time.Duration(g.TimeUsedMillis)*time.Millisecond), g.Turns))
		}
		m.add(model.EntryInfo, "/goal edit [objective] · pause · resume · clear · budget <tokens|none>")
		return
	}
	if m.running && command != "pause" && command != "clear" {
		m.add(model.EntryInfo, "Pause or interrupt the current turn before changing the goal.")
		return
	}
	switch command {
	case "pause":
		err = m.app.Goals.SetStatus(goal.Paused)
	case "resume":
		err = m.app.Goals.SetStatus(goal.Active)
	case "clear":
		err = m.app.Goals.Clear()
	case "edit":
		if g == nil {
			err = fmt.Errorf("no goal is set")
		} else if rest == "" {
			m.pauseGoal()
			m.save()
			m.composer.SetValue("/goal edit " + g.Objective)
			m.composer.CursorEnd()
			return
		} else {
			err = m.app.Goals.Edit(rest, nil)
		}
	case "budget":
		var n uint64
		if rest != "none" {
			n, err = strconv.ParseUint(rest, 10, 64)
			if err == nil && n == 0 {
				err = fmt.Errorf("use a positive token budget, or none")
			}
		}
		if err == nil {
			err = m.app.Goals.Edit("", &n)
		}
	default:
		objective := args
		var budget uint64
		if command == "--budget" {
			number, text, _ := strings.Cut(rest, " ")
			budget, err = strconv.ParseUint(number, 10, 64)
			objective = strings.TrimSpace(text)
			if err != nil || budget == 0 || objective == "" {
				m.add(model.EntryError, "Usage: /goal --budget <positive tokens> <objective>")
				return
			}
		}
		replace := g != nil && g.Status != goal.Complete
		if replace && m.pendingGoal != args {
			m.pauseGoal()
			m.save()
			m.pendingGoal = args
			m.add(model.EntryInfo, "Replace the existing goal and reset its accounting? Repeat the same /goal command to confirm, or use /goal edit to preserve accounting.")
			return
		}
		err = m.app.Goals.Create(objective, budget, replace)
	}
	m.pendingGoal = ""
	if err != nil {
		m.add(model.EntryError, err.Error())
		return
	}
	m.history = append(m.history, "/goal "+args)
	if current := m.app.Goals.Get(); current != nil {
		m.add(model.EntryInfo, fmt.Sprintf("Goal %s: %s", current.Status, current.Objective))
	} else {
		m.add(model.EntryInfo, "Goal cleared.")
	}
	if (command == "pause" || command == "clear") && m.running && m.cancel != nil {
		m.interrupted = true
		m.cancel()
	}
	m.save()
}
