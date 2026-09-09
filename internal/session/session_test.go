package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/model"
)

func TestOptionalFieldsSerializeAsNullOrEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := model.SessionSnapshot{Version: 1, SessionID: "fixture", BedrockMessages: []json.RawMessage{}, Transcript: []model.TranscriptEntry{}, History: []string{}, Usage: &model.Usage{}, CollaborationMode: model.ModeDefault}
	if err := Write(&s); err != nil {
		t.Fatal(err)
	}
	file, _ := File("fixture")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(string(raw))
	for _, want := range []string{`"cwd":null`, `"cwd_history":[]`, `"reasoning_tokens":null`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
}

func TestSessionIDsRejectTraversal(t *testing.T) {
	for _, v := range []string{"../x", "a/b", "", "x.json"} {
		if ValidID(v) {
			t.Errorf("accepted %q", v)
		}
	}
	if !ValidID("abc-123_def") {
		t.Fatal("valid id rejected")
	}
}
func TestDirsAndPreview(t *testing.T) {
	cwd := "/a"
	s := &model.SessionSnapshot{CWD: &cwd, CWDHistory: []string{"/a", "/b"}, Transcript: []model.TranscriptEntry{{Kind: model.EntryTool, Text: "x"}, {Kind: model.EntryUser, Text: "  hello world  "}}}
	d := Dirs(s)
	if len(d) != 2 || d[0] != "/a" || d[1] != "/b" {
		t.Fatal(d)
	}
	if Preview(s) != "hello world" {
		t.Fatal(Preview(s))
	}
}
