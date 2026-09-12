package ui

import (
	"strings"
	"testing"
	"time"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestResearchOutputKeepsFooterInTerminalCells(t *testing.T) {
	for _, text := range []string{
		"## Case law\n\n| ECLI | Findings |\n|---|---|\n| ECLI:NL:RBROT:2020:3207 | " + strings.Repeat("insurance evidence ", 40) + " |\n\n---\n\n**Conclusion**: investigate.",
		"```text\nfirst\tsecond\rprogress\n\nlast\n```\n\n" + strings.Repeat("界🔎 é ", 50),
		"Source\x1b[2J\x1b[H\x1b]0;bad\a\b\v\fform feed\tend",
		strings.Repeat("⚖️ 👩‍⚖️ 👨‍👩‍👧‍👦 evidence ", 60),
	} {
		m := newModel(app.New(&model.Config{}, false), nil)
		m.width, m.height = 80, 24
		m.workingStarted = time.Now()
		m.entries = []model.TranscriptEntry{{Kind: model.EntryAssistant, Text: text}}
		for _, running := range []bool{true, false, true} {
			m.running = running
			view := m.View()
			rows := strings.Split(view.Content, "\n")
			for _, r := range ansi.Strip(view.Content) {
				if unicode.IsControl(r) && r != '\n' {
					t.Fatalf("terminal control %U escaped into the frame", r)
				}
			}
			if len(rows) != m.height {
				t.Fatalf("height %d", len(rows))
			}
			screen := uv.NewScreenBuffer(m.width, m.height)
			uv.NewStyledString(view.Content).Draw(screen, screen.Bounds())
			for y, row := range rows {
				if y < m.height-6 {
					continue
				}
				var drawn strings.Builder
				for x := 0; x < m.width; x++ {
					if cell := screen.CellAt(x, y); cell != nil {
						drawn.WriteString(cell.Content)
					}
				}
				if want, got := ansi.Strip(row), drawn.String(); want != got {
					t.Fatalf("row %d diverged: want %q got %q", y, want, got)
				}
			}
			if !strings.HasPrefix(ansi.Strip(rows[20]), "›") {
				t.Fatalf("composer displaced: %q", rows[20])
			}
		}
	}
}

func TestTranscriptControlNormalization(t *testing.T) {
	input := "first\tsecond\r\nthird\rfourth\x1b[2J\x1b[H\x1b]0;title\a\b\v\f"
	if got := displayText(input); got != "first    second\nthird\nfourth" {
		t.Fatalf("%q", got)
	}
	for _, kind := range []model.EntryKind{model.EntryAssistant, model.EntryTool, model.EntryUser, model.EntryReasoning, model.EntryError} {
		m := newModel(app.New(&model.Config{}, false), nil)
		m.width = 80
		for _, row := range m.renderTranscriptEntry(model.TranscriptEntry{Kind: kind, Text: input}, 80) {
			for _, r := range ansi.Strip(row) {
				if unicode.IsControl(r) {
					t.Fatalf("%s contains control %U", kind, r)
				}
			}
		}
	}
}
