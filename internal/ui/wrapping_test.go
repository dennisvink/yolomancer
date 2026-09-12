package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestProseWrapsAtWordsWithinLabelBudget(t *testing.T) {
	text := "Er draait een proces in de nacht, geen mens die op het antwoord wacht. De cursor knippert, regel voor regel, de terminal kent geen enkele regel. De wind tikt zacht tegen het raam, het scherm geeft licht maar heeft geen naam."
	for _, kind := range []model.EntryKind{model.EntryAssistant, model.EntryReasoning, model.EntryUser} {
		for _, width := range []int{36, 66, 120} {
			m := newModel(app.New(&model.Config{}, false), nil)
			m.width = width
			label, _ := entryPresentation(kind)
			var body []string
			for _, row := range m.renderTranscriptEntry(model.TranscriptEntry{Kind: kind, Text: text}, width) {
				if ansi.StringWidth(row) > width {
					t.Fatalf("%s width %d overflow: %q", kind, width, row)
				}
				body = append(body, ansi.Strip(row)[len(label):])
			}
			if got := strings.Join(strings.Fields(strings.Join(body, "\n")), " "); got != text {
				t.Fatalf("%s width %d split words:\n%s", kind, width, got)
			}
		}
	}
}

func TestLongDigitSequenceWrapsWithoutLosingCharacters(t *testing.T) {
	text := strings.Repeat("0123456789", 20)
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width = 66
	label, _ := entryPresentation(model.EntryAssistant)
	var body strings.Builder
	for _, row := range m.renderTranscriptEntry(model.TranscriptEntry{Kind: model.EntryAssistant, Text: text}, m.width) {
		if ansi.StringWidth(row) > m.width {
			t.Fatalf("overflow: %q", row)
		}
		body.WriteString(strings.TrimSpace(ansi.Strip(row)[len(label):]))
	}
	if body.String() != text {
		t.Fatalf("digits changed: %q", body.String())
	}
}
