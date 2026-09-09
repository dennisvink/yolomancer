package tools

import (
	"context"
	"encoding/json"
	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/security"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllSpecsRequireReasonAndPlanOmitsWrites(t *testing.T) {
	for _, mode := range []model.CollaborationMode{model.ModeDefault, model.ModePlan} {
		specs := Specs(mode, &model.Config{})
		for _, s := range specs {
			p := s["parameters"].(map[string]any)
			raw, _ := json.Marshal(p["required"])
			if !containsJSON(raw, "reason") {
				t.Errorf("%s lacks reason", s["name"])
			}
			if mode == model.ModePlan && (s["name"] == "write_file" || s["name"] == "replace_in_file") {
				t.Fatalf("plan exposes %s", s["name"])
			}
		}
	}
}
func TestFileToolsAndProtectedPaths(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New()}
	call := func(name string, a map[string]any) map[string]any {
		raw := e.Execute(context.Background(), model.ToolCall{Name: name, Arguments: a})
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := call("write_file", map[string]any{"path": "a.txt", "content": "hello"}); v["ok"] != true {
		t.Fatal(v)
	}
	if v := call("read_file", map[string]any{"path": "a.txt"}); v["content"] != "hello" {
		t.Fatal(v)
	} else if resolved, _ := security.CanonicalMissing(filepath.Join(root, "a.txt")); v["path"] != "a.txt" || v["resolved_path"] != resolved {
		t.Fatalf("Required path fields missing: %#v", v)
	}
	if v := call("replace_in_file", map[string]any{"path": "a.txt", "find": "ell", "replace": "ipp"}); v["ok"] != true {
		t.Fatal(v)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "hippo" {
		t.Fatal(string(b))
	}
	if v := call("write_file", map[string]any{"path": ".git/config", "content": "bad"}); v["ok"] != false {
		t.Fatal("protected write allowed")
	}
}

func TestListFilesDefaultsRecursiveWithAbsoluteEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "x.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New()}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "list_files", Arguments: map[string]any{}})
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	entries := result["entries"].([]any)
	canonicalRoot, _ := security.CanonicalMissing(root)
	found := false
	for _, entry := range entries {
		if entry == filepath.Join(canonicalRoot, "a", "b", "x.txt") {
			found = true
		}
	}
	if !found || result["path"] != "." || result["resolved_path"] != canonicalRoot {
		t.Fatalf("%#v", result)
	}
}

func TestCompactDiffFormat(t *testing.T) {
	diff, added, removed, truncated := compactDiff("a\nb\n", "a\nc\n")
	if diff != "@@\n    ⋮\n2 -b\n2 +c" || added != 1 || removed != 1 || truncated {
		t.Fatalf("%q +%d -%d truncated=%v", diff, added, removed, truncated)
	}
}

func TestMalformedWriteIncludesRepairHint(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New()}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "write_file", Arguments: map[string]any{"path": "x.txt"}})
	if !strings.Contains(raw, `"hint"`) || !strings.Contains(raw, `complete UTF-8 file contents`) {
		t.Fatal(raw)
	}
}
func TestPlanModeBlocksMutation(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Config: &model.Config{}, Mode: model.ModePlan, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New()}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "exec_command", Arguments: map[string]any{"cmd": "touch x"}})
	if !containsJSON([]byte(raw), "Plan mode blocked") {
		t.Fatal(raw)
	}
}

func TestOutsidePathCanBeApprovedOnce(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New(), Approve: func(context.Context, ApprovalRequest) (ApprovalDecision, error) { return ApproveOnce, nil }}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "write_file", Arguments: map[string]any{"path": outside, "content": "approved"}})
	if !containsJSON([]byte(raw), `"ok":true`) {
		t.Fatal(raw)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "approved" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestAutomaticReviewerAliasesAndRules(t *testing.T) {
	root := t.TempDir()
	alias := "auto"
	e := &Executor{Config: &model.Config{ApprovalsReviewer: &alias}, Policy: security.Policy{WorkspaceRoot: root}}
	if !e.automaticReviewFor("rm x") {
		t.Fatal("auto alias ignored")
	}
	effect := model.AutoReview
	e.Config = &model.Config{CommandApprovalRules: []model.CommandApprovalRule{{Prefix: []string{"git", "push"}, Effect: &effect}}}
	if !e.automaticReviewFor("git push origin main") || e.automaticReviewFor("git status") {
		t.Fatal("auto review rule mismatch")
	}
}

func TestRememberNetworkStoresGlobalAndProjectRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root}}
	targets := []security.NetworkTarget{{Protocol: "https", Host: "api.example.com"}}
	if err := e.rememberNetwork(targets, model.NetworkAllow, true); err != nil {
		t.Fatal(err)
	}
	want := model.NetworkApprovalRule{Action: model.NetworkAllow, Protocol: "https", Host: "*.example.com"}
	if len(e.Config.NetworkApprovalRules) != 1 || e.Config.NetworkApprovalRules[0] != want {
		t.Fatalf("global rules: %#v", e.Config.NetworkApprovalRules)
	}
	if got := e.Config.ProjectProfiles[root].NetworkApprovalRules; len(got) != 1 || got[0] != want {
		t.Fatalf("project rules: %#v", got)
	}
}

func TestGappedNetworkRequestsApprovalEvenWhenShellApprovalIsNever(t *testing.T) {
	root := t.TempDir()
	called := false
	e := &Executor{
		Config:    &model.Config{},
		Policy:    security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}, ApprovalMode: "never", NetworkPolicy: "approve"},
		Processes: process.New(),
		Approve: func(_ context.Context, request ApprovalRequest) (ApprovalDecision, error) {
			called = true
			if request.Kind != "network access" {
				t.Fatalf("kind %q", request.Kind)
			}
			return Deny, nil
		},
	}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "exec_command", Arguments: map[string]any{"cmd": "curl https://example.com"}})
	if !called || !strings.Contains(raw, "denied once") {
		t.Fatalf("called=%v result=%s", called, raw)
	}
}

func TestPythonToolMetadataAndExecution(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tools"), 0755); err != nil {
		t.Fatal(err)
	}
	source := `def yolomancer_tool():
    return {"name":"reverse_text","description":"Reverse text.","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}
def run(args):
    return {"text": args["text"][::-1]}
`
	if err := os.WriteFile(filepath.Join(root, "tools", "reverse.py"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	defer os.Chdir(old)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defs, err := DiscoverPythonTools()
	if err != nil || len(defs) != 1 || defs[0].Name != "reverse_text" {
		t.Fatalf("%#v %v", defs, err)
	}
	e := &Executor{Config: &model.Config{}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}, Processes: process.New()}
	raw := e.Execute(context.Background(), model.ToolCall{Name: "reverse_text", Arguments: map[string]any{"text": "abc", "reason": "test"}})
	if !containsJSON([]byte(raw), "cba") {
		t.Fatal(raw)
	}
}

func TestPythonToolRejectsInvalidName(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	path := filepath.Join(t.TempDir(), "invalid.py")
	source := `def yolomancer_tool():
    return {"name":"not-valid","description":"Invalid name."}
`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := pythonMetadata(path)
	if err == nil || !strings.Contains(err.Error(), "invalid Python tool name") {
		t.Fatalf("expected invalid-name error, got %v", err)
	}
}

func TestAWSToolsAndPythonUseSelectedProfile(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Mkdir(filepath.Join(root, "tools"), 0755); err != nil {
		t.Fatal(err)
	}
	// This fake fails if any path attempts role assumption or retains static keys.
	fake := `#!/bin/sh
test "$AWS_PROFILE" = build || exit 11
test -z "$AWS_ACCESS_KEY_ID$AWS_SECRET_ACCESS_KEY$AWS_SESSION_TOKEN" || exit 12
test "$1 $2" = "sts get-caller-identity" || exit 13
printf '%s\n' '{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/builder","UserId":"builder"}'
`
	if err := os.WriteFile(filepath.Join(root, "aws"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_SESSION_TOKEN", "ambient-token")
	source := `def yolomancer_tool():
    return {"name":"identity","description":"Inspect AWS identity."}
import yolomancer_aws
def run(args):
    return yolomancer_aws.sts.get_caller_identity()
`
	if err := os.WriteFile(filepath.Join(root, "tools", "identity.py"), []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	profile, key, secret := "build", "saved-key", "saved-secret"
	e := &Executor{Config: &model.Config{AWSProfile: &profile, AWSAccessKeyID: &key, AWSSecretAccessKey: &secret}, Policy: security.Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}, PermissionMode: security.PermissionYolo, SandboxMode: "danger-full-access"}, Processes: process.New()}
	for _, call := range []model.ToolCall{
		{Name: "aws_tool", Arguments: map[string]any{"action": "sts.get_caller_identity"}},
		{Name: "aws_cli", Arguments: map[string]any{"use_case": "check identity", "args": []any{"sts", "get-caller-identity"}}},
		{Name: "identity", Arguments: map[string]any{}},
		{Name: "exec_command", Arguments: map[string]any{"cmd": "aws sts get-caller-identity", "shell": "/bin/sh", "login": false}},
	} {
		result := e.Execute(t.Context(), call)
		if !strings.Contains(result, "123456789012") || strings.Contains(result, `"ok":false`) {
			t.Errorf("%s did not use build profile: %s", call.Name, result)
		}
	}
}

func TestAWSCLIArgumentMapping(t *testing.T) {
	args, err := awsCLIArgs("dynamodb.create_table", map[string]any{"table_name": "T", "partition_key": "pk"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"create-table", "--table-name T", "AttributeName=pk", "PAY_PER_REQUEST"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q lacks %q", joined, want)
		}
	}
}

func TestAWSHelperNormalization(t *testing.T) {
	value := map[string]any{"Buckets": []any{map[string]any{"Name": "demo", "CreationDate": "today"}}}
	data := normalizeAWSData("s3.list_buckets", nil, value).(map[string]any)
	buckets := data["buckets"].([]any)
	if len(buckets) != 1 || buckets[0].(map[string]any)["name"] != "demo" {
		t.Fatalf("%#v", data)
	}
	value = map[string]any{"Vpcs": []any{map[string]any{"VpcId": "vpc-1", "CidrBlock": "10.0.0.0/16", "State": "available", "IsDefault": false}}}
	data = normalizeAWSData("ec2.describe_vpcs", nil, value).(map[string]any)
	if data["vpcs"].([]any)[0].(map[string]any)["vpc_id"] != "vpc-1" {
		t.Fatalf("%#v", data)
	}
}
func containsJSON(b []byte, s string) bool { return len(b) > 0 && stringContains(string(b), s) }
func stringContains(v, s string) bool {
	for i := 0; i+len(s) <= len(v); i++ {
		if v[i:i+len(s)] == s {
			return true
		}
	}
	return false
}
