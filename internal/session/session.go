package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
)

func ValidID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func File(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid session id `%s`", id)
	}
	d, err := appconfig.SessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, id+".json"), nil
}

func Write(s *model.SessionSnapshot) error {
	f, err := File(s.SessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0700); err != nil {
		return err
	}
	copySnapshot := *s
	if copySnapshot.CWDHistory == nil {
		copySnapshot.CWDHistory = []string{}
	}
	if copySnapshot.BedrockMessages == nil {
		copySnapshot.BedrockMessages = []json.RawMessage{}
	}
	if copySnapshot.Transcript == nil {
		copySnapshot.Transcript = []model.TranscriptEntry{}
	}
	if copySnapshot.History == nil {
		copySnapshot.History = []string{}
	}
	b, err := json.MarshalIndent(&copySnapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f, b, 0600)
}

func Load(id string) (*model.SessionSnapshot, error) {
	f, err := File(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, fmt.Errorf("read saved session %s: %w", f, err)
	}
	var s model.SessionSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f, err)
	}
	if s.SessionID != id {
		return nil, fmt.Errorf("session file %s contains mismatched session id `%s`", f, s.SessionID)
	}
	if s.CollaborationMode == "" {
		s.CollaborationMode = model.ModeDefault
	}
	return &s, nil
}

type Summary struct {
	SessionID     string
	UpdatedAtUnix uint64
	Dirs          []string
	Preview       string
}

func Dirs(s *model.SessionSnapshot) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if s.CWD != nil {
		add(*s.CWD)
	}
	for _, v := range s.CWDHistory {
		add(v)
	}
	return out
}

func List(all bool, cwd string) ([]Summary, error) {
	d, err := appconfig.SessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(ent.Name(), ".json")
		s, err := Load(id)
		if err != nil {
			continue
		}
		dirs := Dirs(s)
		if !all && !contains(dirs, cwd) {
			continue
		}
		out = append(out, Summary{s.SessionID, s.UpdatedAtUnix, dirs, Preview(s)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAtUnix > out[j].UpdatedAtUnix })
	return out, nil
}

func Preview(s *model.SessionSnapshot) string {
	for _, e := range s.Transcript {
		if e.Kind == model.EntryUser && strings.TrimSpace(e.Text) != "" {
			return truncate(strings.TrimSpace(e.Text), 72)
		}
	}
	return "(no prompt preview)"
}

func Touch(s *model.SessionSnapshot) { s.UpdatedAtUnix = uint64(time.Now().Unix()) }
func contains(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}
func truncate(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n]) + "..."
}
