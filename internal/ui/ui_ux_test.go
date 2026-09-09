package ui

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/tools"
)

func TestCollapsedPasteMarkerAndExpansion(t *testing.T) {
	var editor composer
	content := strings.Repeat("é", collapsedPasteCharThreshold)
	editor.InsertPaste(content)
	if got, want := editor.Value(), "[Pasted Content 800 chars]"; got != want {
		t.Fatalf("marker = %q, want %q", got, want)
	}
	if editor.Expanded() != content {
		t.Fatal("collapsed paste did not expand to its original Unicode content")
	}
	editor.InsertPaste(content)
	if !strings.Contains(editor.Value(), "[Pasted Content 800 chars] #2") {
		t.Fatalf("duplicate marker was not made unique: %q", editor.Value())
	}
}

func TestPasteNormalizesNewlinesAndCollapsesEightLines(t *testing.T) {
	var editor composer
	editor.InsertPaste("1\r\n2\r3\n4\n5\n6\n7\n8")
	if editor.Value() != "[Pasted Content 15 chars]" {
		t.Fatalf("marker = %q", editor.Value())
	}
	if editor.Expanded() != "1\n2\n3\n4\n5\n6\n7\n8" {
		t.Fatalf("expanded = %q", editor.Expanded())
	}
}

func TestPasteMarkersMoveAndDeleteAtomically(t *testing.T) {
	marker := "[Pasted Content 1200 chars]"
	editor := composer{input: "a" + marker + "b", cursor: 1, pastes: []pastedBlock{{marker: marker, content: strings.Repeat("x", 1200)}}}
	editor.MoveRight()
	if editor.cursor != 1+len(marker) {
		t.Fatalf("right entered marker: cursor=%d", editor.cursor)
	}
	editor.MoveLeft()
	if editor.cursor != 1 {
		t.Fatalf("left entered marker: cursor=%d", editor.cursor)
	}
	editor.Delete()
	if editor.Value() != "ab" || len(editor.pastes) != 0 || editor.cursor != 1 {
		t.Fatalf("atomic delete failed: %#v", editor)
	}
	editor = composer{input: "a" + marker + "b", cursor: 1 + len(marker), pastes: []pastedBlock{{marker: marker, content: "x"}}}
	editor.Backspace()
	if editor.Value() != "ab" || editor.cursor != 1 {
		t.Fatalf("atomic backspace failed: %#v", editor)
	}
}

func TestUnicodeAndWordNavigation(t *testing.T) {
	editor := composer{input: "é hello_world!", cursor: len("é hello_world!")}
	editor.MoveLeft()
	editor.MoveWordLeft()
	if got := editor.input[editor.cursor:]; got != "hello_world!" {
		t.Fatalf("word-left remainder = %q", got)
	}
	editor.Backspace()
	if editor.Value() != "éhello_world!" {
		t.Fatalf("Unicode backspace = %q", editor.Value())
	}
}

func TestShiftEnterInsertsNewlineAndEnterQueuesWhileBusy(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.composer.SetValue("first")
	_, _ = m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	if m.composer.Value() != "first\n" {
		t.Fatalf("Shift-Enter value = %q", m.composer.Value())
	}
	m.composer.InsertString("second")
	m.running = true
	_, cmd := m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd != nil || len(m.queued) != 1 || m.queued[0] != "first\nsecond" {
		t.Fatalf("busy submit did not queue: queue=%#v cmd=%v", m.queued, cmd)
	}
	if m.entries[len(m.entries)-1].Kind != model.EntryQueued || !strings.Contains(m.entries[len(m.entries)-1].Text, "press Esc") {
		t.Fatal("queued prompt was not visibly represented")
	}
}

func TestPasteEventUsesCollapsedComposerPath(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	_, _ = m.Update(tea.PasteMsg{Content: strings.Repeat("x", 800)})
	if m.composer.Value() != "[Pasted Content 800 chars]" {
		t.Fatalf("paste event value = %q", m.composer.Value())
	}
	m.running = true
	_, _ = m.Update(tea.PasteMsg{Content: "ignored while busy"})
	if strings.Contains(m.composer.Value(), "ignored") {
		t.Fatal("busy paste should be ignored")
	}
}

func TestTerminalColorDetectionStaysInsideBubbleTea(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	if m.Init() == nil {
		t.Fatal("Init must request terminal color and schedule UI ticks")
	}
	_, _ = m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.dark {
		t.Fatal("white terminal background was classified as dark")
	}
	// Explicit Glamour styles must render without starting an independent
	// terminal query/read while Bubble Tea owns stdin.
	if rendered := m.renderMarkdown("**safe**"); !strings.Contains(rendered, "safe") {
		t.Fatalf("markdown render = %q", rendered)
	}
}

func TestC64ThemeAppliesToTheWholeViewAndMarkdown(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height = 84, 22
	view := m.View()
	if view.BackgroundColor != c64BackgroundRGB {
		t.Fatalf("view background = %v, want %v", view.BackgroundColor, c64BackgroundRGB)
	}
	if view.ForegroundColor != c64ForegroundRGB {
		t.Fatalf("view foreground = %v, want %v", view.ForegroundColor, c64ForegroundRGB)
	}
	if view.Cursor == nil || view.Cursor.Color != c64BackgroundRGB {
		t.Fatalf("composer cursor color = %#v, want %v", view.Cursor, c64BackgroundRGB)
	}

	style := c64MarkdownStyle()
	if style.Document.Color == nil || *style.Document.Color != c64ForegroundHex {
		t.Fatalf("Markdown foreground = %v, want %s", style.Document.Color, c64ForegroundHex)
	}
	if style.Document.BackgroundColor == nil || *style.Document.BackgroundColor != c64BlueHex {
		t.Fatalf("Markdown background = %v, want %s", style.Document.BackgroundColor, c64BlueHex)
	}
	if style.CodeBlock.Chroma == nil || style.CodeBlock.Chroma.Keyword.Color == nil || *style.CodeBlock.Chroma.Keyword.Color != c64CyanHex {
		t.Fatal("Markdown syntax highlighting did not inherit the C64 palette")
	}
}

func TestAssistantMarkdownListsDoNotRenderDoubleSpaced(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width = 120
	input := "Pac-Man speeds vary by context:\n\n- Normal movement\n\n- Eating dots slows him down\n\nGhost speeds vary by context:\n\n- Normal\n\n- Tunnel zones"
	rendered := ansi.Strip(m.renderMarkdown(input))
	lines := strings.Split(rendered, "\n")
	for index := 1; index < len(lines)-1; index++ {
		if strings.TrimSpace(lines[index]) == "" && strings.TrimSpace(lines[index-1]) != "" && strings.TrimSpace(lines[index+1]) != "" {
			t.Fatalf("markdown contains a spurious blank row between content lines:\n%s", rendered)
		}
	}
	for _, want := range []string{"• Normal movement", "• Eating dots slows him down", "• Normal", "• Tunnel zones"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, rendered)
		}
	}
}

func TestAssistantMarkdownKeepsIntentionalBlankCodeRows(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width = 100
	rendered := ansi.Strip(m.renderMarkdown("```go\none()\n\ntwo()\n```"))
	lines := strings.Split(rendered, "\n")
	one, two := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "one()") {
			one = index
		}
		if strings.Contains(line, "two()") {
			two = index
		}
	}
	if one < 0 || two != one+2 || strings.TrimSpace(lines[one+1]) != "" {
		t.Fatalf("blank fenced-code row was not preserved:\n%s", rendered)
	}
}

func TestSlashPaletteShowsDescriptionsAndSelection(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height = 100, 24
	m.composer.SetValue("/perm")
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Commands", "/permissions", "Update local model permissions"} {
		if !strings.Contains(view, want) {
			t.Fatalf("palette lacks %q:\n%s", want, view)
		}
	}
	_, _ = m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.slashSelection != 0 { // only one prefix match
		t.Fatalf("single selection moved to %d", m.slashSelection)
	}
}

func TestPermissionsAndApprovalAreCenteredMenus(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height = 100, 30
	m.permissionPrompt = 0
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Update Model Permissions") || !strings.Contains(view, "› 1. Default") || !strings.Contains(view, "Automatic Arbitrage") {
		t.Fatalf("permission modal incomplete:\n%s", view)
	}
	m.permissionPrompt = -1
	m.approval = &approvalState{request: tools.ApprovalRequest{Kind: "network access", Command: "curl https://example.com", Reason: "Needs internet"}, response: make(chan tools.ApprovalDecision, 1)}
	view = ansi.Strip(m.View().Content)
	for _, want := range []string{"[ APPROVAL REQUIRED ]", "yolomancer permission request", "ALLOW ONCE", "Allow *.domain", "Network extra: W"} {
		if !strings.Contains(view, want) {
			t.Fatalf("approval modal lacks %q:\n%s", want, view)
		}
	}
	_, _ = m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if m.approvalSelection != 1 {
		t.Fatalf("approval selection = %d", m.approvalSelection)
	}
}

func TestScreenIsFixedSizeAndExposesCursorAndStatus(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height = 84, 22
	m.composer.SetValue("hello")
	view := m.View()
	lines := strings.Split(view.Content, "\n")
	if len(lines) != 22 {
		t.Fatalf("screen height = %d", len(lines))
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got != 84 {
			t.Fatalf("line %d width = %d", index, got)
		}
	}
	plain := ansi.Strip(view.Content)
	for _, want := range []string{"yolomancer", "idle  mode=Default  model=Opus", "Default mode", "enter send"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("screen lacks %q:\n%s", want, plain)
		}
	}
	if view.Cursor == nil || view.Cursor.X < 3 || view.WindowTitle == "" {
		t.Fatalf("cursor/title missing: cursor=%#v title=%q", view.Cursor, view.WindowTitle)
	}
}

func TestEscapeClearsComposerAfterDismissingPlanNudge(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.composer.SetValue("make a plan")
	_, _ = m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.composer.Value() == "" || !m.planNudgeDismissed {
		t.Fatal("first Escape should dismiss plan nudge without clearing")
	}
	_, _ = m.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.composer.Value() != "" {
		t.Fatal("second Escape should clear the composer")
	}
}

func TestExplorationPresentation(t *testing.T) {
	operations := shellExploringOperations("cd src && rg ToolCall main.go | sed -n '1,20p'")
	want := []exploringOperation{{"Search", "ToolCall in main.go"}, {"Read", "sed -n 1,20p"}}
	if len(operations) != len(want) {
		t.Fatalf("operations = %#v", operations)
	}
	for index := range want {
		if operations[index] != want[index] {
			t.Fatalf("operation %d = %#v, want %#v", index, operations[index], want[index])
		}
	}
	display := exploringDisplay([]exploringOperation{{"Read", "go.mod"}, {"Read", "src/main.go"}, {"Search", "ToolCall in src/main.go"}}, false)
	if want := "• Explored\n  └ Read go.mod, src/main.go\n  └ Search ToolCall in src/main.go"; display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}
	for _, command := range []string{"go build ./cmd/yolomancer", "sed -i s/a/b/ file.txt", "curl -s https://example.com"} {
		if got := shellExploringOperations(command); got != nil {
			t.Fatalf("mutating command %q classified as %#v", command, got)
		}
	}
}

func TestPermissionCopy(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height, m.permissionPrompt = 140, 40, 0
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"Default (current)", "access the internet. Permission is required", "automatic arbiter", "Exercise caution when"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("permission screen lacks %q:\n%s", want, plain)
		}
	}

}
