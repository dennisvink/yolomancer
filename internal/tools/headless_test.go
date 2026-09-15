package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/security"
)

func TestAPIExecutionEnforcesPathsAndAllowlistWithoutApproval(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"read_file", "write_file", "exec_command"} {
		e := &Executor{Headless: true, Config: &model.Config{}, AllowedTools: map[string]bool{"read_file": true, "write_file": true}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}}}
		e.Approve = func(_ context.Context, _ ApprovalRequest) (ApprovalDecision, error) {
			t.Fatal("interactive approval")
			return Deny, nil
		}
		result := e.Execute(t.Context(), model.ToolCall{Name: name, Arguments: map[string]any{"path": filepath.Join(root, "..", "outside"), "content": "bad", "cmd": "echo bad"}})
		if !strings.Contains(result, "permission_denied") || e.PermissionError == nil {
			t.Fatal(name, result)
		}
	}
}

func TestPythonMetadataLoadsLiteralConstantsWithoutImportingModule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.py")
	source := "OPS = ['list', 'create']\nraise RuntimeError('module must not execute')\ndef yolomancer_tool():\n return {'name':'example', 'description':'example tool','parameters':{'type':'object','properties':{'op':{'type':'string','enum':OPS}}}}\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	defs, err := LoadPythonTools([]string{path})
	if err != nil || len(defs) != 1 {
		t.Fatal(err)
	}
	props := defs[0].Parameters["properties"].(map[string]any)
	if len(props["op"].(map[string]any)["enum"].([]any)) != 2 {
		t.Fatal(defs)
	}
}
