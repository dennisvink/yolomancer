package ui

import (
	"testing"
	"time"

	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestSlashPaletteSelectsPrefixMatch(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.composer.SetValue("/perm")
	matches := m.slashMatches()
	if len(matches) != 1 || matches[0] != "/permissions" {
		t.Fatalf("%#v", matches)
	}
}

func TestElapsedFormattingAndTerminalTitleSanitization(t *testing.T) {
	if got := compactDuration(65 * time.Second); got != "1m 05s" {
		t.Fatal(got)
	}
	if got := compactDuration(3661 * time.Second); got != "1h 01m 01s" {
		t.Fatal(got)
	}
	if got := sanitizeTitle("  work\u200b\n space  "); got != "work space" {
		t.Fatalf("%q", got)
	}
}

func TestWriteToolDisplayDoesNotExposeContent(t *testing.T) {
	call := model.ToolCall{Name: "write_file", Arguments: map[string]any{"path": "secret.txt", "content": "sensitive payload"}}
	got := reasonOrArgs(call)
	if got != "path=secret.txt" {
		t.Fatalf("%q", got)
	}
}

func TestPlanCompletionOpensImplementationPrompt(t *testing.T) {
	a := app.New(&model.Config{}, false)
	a.SetMode(model.ModePlan)
	m := newModel(a, nil)
	m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryAssistant, Text: "<proposed_plan>Build it</proposed_plan>"})
	if !m.latestAssistantHasPlan() {
		t.Fatal("completed proposed plan was not detected")
	}
	m.planPrompt = 2
	m.acceptPlanPrompt()
	if a.Mode != model.ModeDefault || m.planPrompt != -1 {
		t.Fatalf("mode=%s prompt=%d", a.Mode, m.planPrompt)
	}
}

func TestPermissionModeIndexes(t *testing.T) {
	for mode, want := range map[string]int{"default": 0, "gapped": 1, "automatic-arbitrage": 2, "yolo": 3} {
		if got := permissionModeIndex(mode); got != want {
			t.Errorf("%s: got %d want %d", mode, got, want)
		}
	}
}

func TestPlanNudgeOnlyMatchesStandaloneKeyword(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	for _, value := range []string{"make a plan", "Plan this", "a plan?"} {
		m.composer.SetValue(value)
		if !m.planNudgeVisible() {
			t.Errorf("nudge hidden for %q", value)
		}
	}
	for _, value := range []string{"planet", "planning", "explain"} {
		m.composer.SetValue(value)
		if m.planNudgeVisible() {
			t.Errorf("nudge shown for %q", value)
		}
	}
}
