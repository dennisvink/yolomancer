package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/security"
)

func TestLargeFileCanBeReadInBoundedRanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "large.txt")
	want := strings.Repeat("界\"\n", 25000)
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Config: &model.Config{}, Policy: security.BuildPolicy(&model.Config{}, root)}
	offset := 0
	var got strings.Builder
	for {
		raw := e.Execute(t.Context(), model.ToolCall{Name: "read_file", Arguments: map[string]any{"path": path, "offset_bytes": offset}})
		if len(raw) > provider.ToolOutputBytes || !utf8.ValidString(raw) {
			t.Fatal("read result exceeds limit or corrupts UTF-8")
		}
		var result struct {
			Content   string
			Next      int `json:"next_offset_bytes"`
			Truncated bool
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		got.WriteString(result.Content)
		if !result.Truncated {
			break
		}
		if result.Next <= offset {
			t.Fatalf("pagination stalled: %s", raw)
		}
		offset = result.Next
	}
	if got.String() != want {
		t.Fatal("pagination lost or duplicated content")
	}
}
