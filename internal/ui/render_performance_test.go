package ui

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/dennisvink/yolomancer/internal/app"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestTranscriptCacheInvalidation(t *testing.T) {
	m := newModel(app.New(&model.Config{}, false), nil)
	m.width, m.height = 100, 35
	m.entries = []model.TranscriptEntry{{Kind: model.EntryAssistant, Text: "**original**"}, {Kind: model.EntryQueued, Text: "queued"}}
	check := func() {
		t.Helper()
		got := append([]string(nil), m.transcriptBodyLines(m.width)...)
		cold := *m
		cold.transcriptCache = transcriptRenderCache{}
		want := cold.transcriptBodyLines(m.width)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("stale transcript:\ngot %v\nwant %v", got, want)
		}
	}
	check()
	m.composer.SetValue("typing must not change history")
	check()
	m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryAssistant, Text: "stream", Streaming: true})
	check()
	m.entries[2].Text += "ing **text**"
	check()
	m.entries[2].Streaming = false
	check()
	m.removeQueuedEntry("queued")
	check()
	m.entries[0].Text = "changed earlier entry"
	check()
	m.width = 45
	check()
	m.entries = nil
	check()
}

func BenchmarkTypingWithHistory(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			m := newModel(app.New(&model.Config{}, false), nil)
			m.width, m.height = 100, 35
			for i := 0; i < count; i++ {
				m.entries = append(m.entries, model.TranscriptEntry{Kind: model.EntryAssistant, Text: fmt.Sprintf("Step %d: **done**.\n\n- Inspect the file\n- Run tests\n\n```go\nfmt.Println(\"hello\")\n```", i)})
			}
			m.View()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.composer.SetValue(fmt.Sprintf("typing %d", i))
				m.View()
			}
		})
	}
}
