package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	appconfig "github.com/dennisvink/yolomancer/internal/config"
	"github.com/dennisvink/yolomancer/internal/model"
	proc "github.com/dennisvink/yolomancer/internal/process"
	"github.com/dennisvink/yolomancer/internal/provider"
	"github.com/dennisvink/yolomancer/internal/security"
)

type ApprovalRequest struct {
	Kind, Command, Workdir, Reason string
	SuggestedPrefix                []string
	NetworkTargets                 []security.NetworkTarget
	SuggestedRoot                  string
	Write                          bool
}
type ApprovalDecision int

const (
	ApproveOnce ApprovalDecision = iota
	ApproveRemember
	ApproveRememberWildcard
	DenyRemember
	Deny
)

type Approver func(context.Context, ApprovalRequest) (ApprovalDecision, error)

type Executor struct {
	Config     *model.Config
	Policy     security.Policy
	Mode       model.CollaborationMode
	Processes  *proc.Manager
	Approve    Approver
	AutoReview Approver
	Debug      bool
}

func Specs(mode model.CollaborationMode, cfg *model.Config) []map[string]any {
	reason := map[string]any{"type": "string", "description": "A short narration of what the agent is about to do with this tool call."}
	spec := func(name, desc string, props map[string]any, required ...string) map[string]any {
		props["reason"] = reason
		required = append([]string{"reason"}, required...)
		return map[string]any{"type": "function", "name": name, "description": desc, "parameters": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}
	}
	out := []map[string]any{
		spec("exec_command", "Runs a command, returning output or a session ID for ongoing interaction.", map[string]any{"cmd": map[string]any{"type": "string"}, "workdir": map[string]any{"type": "string"}, "shell": map[string]any{"type": "string"}, "login": map[string]any{"type": "boolean"}, "yield_time_ms": map[string]any{"type": "integer"}, "max_output_tokens": map[string]any{"type": "integer"}, "tty": map[string]any{"type": "boolean"}}, "cmd"),
		spec("write_stdin", "Write characters to an existing exec_command session and return recent output. Pass empty chars to poll.", map[string]any{"session_id": map[string]any{"type": "integer"}, "chars": map[string]any{"type": "string"}, "yield_time_ms": map[string]any{"type": "integer"}, "max_output_tokens": map[string]any{"type": "integer"}}, "session_id"),
		spec("read_file", "Read a bounded UTF-8 file range. For truncated results, continue at next_offset_bytes; prefer searching for relevant sections of large files.", map[string]any{"path": map[string]any{"type": "string"}, "offset_bytes": map[string]any{"type": "integer", "minimum": 0}, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 32000}}, "path"),
		spec("write_file", "Write UTF-8 content to local disk. Always provide both required arguments: path and content. Put the complete file text in content.", map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "path", "content"),
		spec("replace_in_file", "Replace one or all exact text matches in a local file.", map[string]any{"path": map[string]any{"type": "string"}, "find": map[string]any{"type": "string"}, "replace": map[string]any{"type": "string"}, "all": map[string]any{"type": "boolean"}}, "path", "find", "replace"),
		spec("list_files", "List local files/directories.", map[string]any{"path": map[string]any{"type": "string"}, "recursive": map[string]any{"type": "boolean"}, "max_entries": map[string]any{"type": "integer"}}),
	}
	if cfg != nil {
		if _, err := exec.LookPath("aws"); err == nil {
			out = append(out, spec("aws_cli", "Fallback/debug AWS tool. Prefer aws_tool for supported AWS operations. Provide AWS CLI arguments as an array, excluding the leading aws binary.", map[string]any{"use_case": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1}, "timeout_sec": map[string]any{"type": "integer"}, "max_output_tokens": map[string]any{"type": "integer"}}, "use_case", "args"))
		}
	}
	if mode == model.ModePlan {
		filtered := out[:0]
		for _, s := range out {
			if s["name"] != "write_file" && s["name"] != "replace_in_file" {
				filtered = append(filtered, s)
			}
		}
		out = filtered
	}
	defs, _ := DiscoverPythonTools()
	for _, d := range defs {
		p := cloneMap(d.Parameters)
		props, _ := p["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			p["properties"] = props
		}
		props["reason"] = reason
		req, _ := p["required"].([]any)
		found := false
		for _, v := range req {
			if v == "reason" {
				found = true
			}
		}
		if !found {
			req = append(req, "reason")
		}
		p["required"] = req
		p["type"] = "object"
		if _, ok := p["additionalProperties"]; !ok {
			p["additionalProperties"] = false
		}
		out = append(out, map[string]any{"type": "function", "name": d.Name, "description": d.Description, "parameters": p})
	}
	return out
}

func (e *Executor) Execute(ctx context.Context, call model.ToolCall) string {
	args := cloneMap(call.Arguments)
	delete(args, "reason")
	if e.Mode == model.ModePlan {
		if call.Name == "write_file" || call.Name == "replace_in_file" {
			return fail("Plan mode permits non-mutating exploration only. Switch to /code before editing files.")
		}
		if call.Name == "exec_command" {
			if r := security.PlanMutationReason(str(args, "cmd")); r != "" {
				return fail("Plan mode blocked this shell command because it appears mutating: " + r + ". Switch to /code before carrying out implementation work.")
			}
		}
	}
	var value string
	var err error
	switch call.Name {
	case "exec_command":
		value, err = e.exec(ctx, args)
	case "write_stdin":
		value, err = e.writeStdin(ctx, args)
	case "read_file":
		value, err = e.readFile(ctx, args)
	case "write_file":
		value, err = e.writeFile(ctx, args)
	case "replace_in_file":
		value, err = e.replace(ctx, args)
	case "list_files":
		value, err = e.list(ctx, args)
	case "aws_tool":
		value, err = e.awsTool(ctx, args)
	case "aws_cli":
		value, err = e.awsCLI(ctx, args)
	default:
		value, err = e.python(ctx, call.Name, args)
	}
	if err != nil {
		return provider.BoundToolOutput(toolFailure(call, err.Error()))
	}
	return provider.BoundToolOutput(value)
}

func (e *Executor) exec(ctx context.Context, a map[string]any) (string, error) {
	cmd := str(a, "cmd")
	if cmd == "" {
		return "", fmt.Errorf("missing required string argument: cmd")
	}
	originalCommand := cmd
	wd := str(a, "workdir")
	if wd == "" {
		wd = e.Policy.WorkspaceRoot
	}
	resolved, err := e.resolvePath(ctx, wd, true)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(resolved); err != nil || !st.IsDir() {
		return "", fmt.Errorf("working directory does not exist")
	}
	needs := security.DangerousReason(cmd)
	network := security.RequestsNetwork(cmd)
	networkTargets := security.NetworkTargets(cmd)
	if network {
		allAllowed := len(networkTargets) > 0
		for _, target := range networkTargets {
			targetAllowed := false
			rules := append([]model.NetworkApprovalRule{}, e.Config.NetworkApprovalRules...)
			if profile, ok := e.Config.ProjectProfiles[e.Policy.WorkspaceRoot]; ok {
				rules = append(rules, profile.NetworkApprovalRules...)
			}
			for _, rule := range rules {
				if security.RuleMatches(rule, target) {
					if rule.Action == model.NetworkDeny {
						return "", fmt.Errorf("network access denied by remembered local policy for %s://%s", target.Protocol, target.Host)
					}
					if rule.Action == model.NetworkAllow {
						targetAllowed = true
					}
				}
			}
			allAllowed = allAllowed && targetAllowed
		}
		if allAllowed {
			network = false
		}
	}
	if network && e.Policy.NetworkPolicy == "deny" {
		return "", fmt.Errorf("network access denied by local policy")
	}
	if network && e.Policy.NetworkPolicy == "approve" {
		needs = "network access requires approval"
	}
	isNetworkApproval := network && e.Policy.NetworkPolicy == "approve"
	commandApproved := false
	for _, r := range e.Config.CommandApprovalRules {
		if security.CommandRuleMatches(r, cmd) && r.Effect != nil && *r.Effect == model.AllowAlways {
			commandApproved = true
		}
		if !isNetworkApproval && security.CommandRuleMatches(r, cmd) && r.Effect != nil && *r.Effect == model.AllowAlways {
			needs = ""
		}
	}
	if needs != "" && (e.Policy.ApprovalMode != "never" || isNetworkApproval) {
		reviewer := e.Approve
		if e.automaticReviewFor(cmd) {
			reviewer = e.AutoReview
		}
		if reviewer == nil {
			return "", fmt.Errorf("%s", needs)
		}
		kind := "shell command"
		if isNetworkApproval {
			kind = "network access"
		}
		req := ApprovalRequest{Kind: kind, Command: cmd, Workdir: resolved, Reason: needs, SuggestedPrefix: security.ApprovalPrefix(cmd), NetworkTargets: networkTargets}
		d, err := reviewer(ctx, req)
		if err != nil {
			return "", err
		}
		if d == Deny || d == DenyRemember {
			if d == DenyRemember && isNetworkApproval {
				if err := e.rememberNetwork(req.NetworkTargets, model.NetworkDeny, false); err != nil {
					return "", err
				}
				return "", fmt.Errorf("network access denied and remembered by local approval policy. Do not retry equivalent network requests unless the user explicitly asks")
			}
			return "", fmt.Errorf("shell command denied once by local approval policy. Do not retry equivalent permission requests unless the user explicitly asks.")
		}
		if d == ApproveRemember {
			if isNetworkApproval {
				if err := e.rememberNetwork(req.NetworkTargets, model.NetworkAllow, false); err != nil {
					return "", err
				}
			} else {
				effect := model.AllowAlways
				e.Config.CommandApprovalRules = append(e.Config.CommandApprovalRules, model.CommandApprovalRule{Prefix: req.SuggestedPrefix, Effect: &effect})
				if err := appconfig.Save(e.Config); err != nil {
					return "", err
				}
			}
		}
		if d == ApproveRememberWildcard && isNetworkApproval {
			if err := e.rememberNetwork(req.NetworkTargets, model.NetworkAllow, true); err != nil {
				return "", err
			}
		}
	}
	yield := durationMS(a, "yield_time_ms", 1000, 250, 30000)
	max := integer(a, "max_output_tokens", 10000)
	login := boolean(a, "login", true)
	tty := boolean(a, "tty", false)
	shell := str(a, "shell")
	executionPolicy := e.Policy
	if commandApproved {
		executionPolicy.SandboxMode = "danger-full-access"
	}
	program, sargs, sandboxed := security.SandboxCommand(cmd, resolved, executionPolicy)
	if sandboxed {
		wrapped := strings.Join(append([]string{program}, quoteArgs(sargs)...), " ")
		cmd = wrapped
		shell = "/bin/sh"
		login = false
	}
	result, err := e.Processes.Exec(ctx, proc.ExecOptions{Env: provider.ToolEnvironment(e.Config), Command: cmd, DisplayCommand: originalCommand, Workdir: resolved, Shell: shell, Login: login, TTY: tty, Yield: yield, MaxTokens: max})
	if err != nil {
		return "", err
	}
	var payload map[string]any
	if json.Unmarshal([]byte(result), &payload) == nil {
		payload["workdir"] = wd
		return marshal(payload), nil
	}
	return result, nil
}

func (e *Executor) rememberNetwork(targets []security.NetworkTarget, action model.NetworkRuleAction, wildcard bool) error {
	if e.Config.ProjectProfiles == nil {
		e.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
	}
	profile := e.Config.ProjectProfiles[e.Policy.WorkspaceRoot]
	for _, target := range targets {
		host := target.Host
		if wildcard {
			parts := strings.Split(host, ".")
			if len(parts) >= 3 {
				host = "*." + strings.Join(parts[1:], ".")
			}
		}
		rule := model.NetworkApprovalRule{Action: action, Protocol: target.Protocol, Host: host}
		e.Config.NetworkApprovalRules = appendUniqueNetworkRule(e.Config.NetworkApprovalRules, rule)
		profile.NetworkApprovalRules = appendUniqueNetworkRule(profile.NetworkApprovalRules, rule)
	}
	e.Config.ProjectProfiles[e.Policy.WorkspaceRoot] = profile
	return appconfig.Save(e.Config)
}

func appendUniqueNetworkRule(rules []model.NetworkApprovalRule, rule model.NetworkApprovalRule) []model.NetworkApprovalRule {
	for _, existing := range rules {
		if existing == rule {
			return rules
		}
	}
	return append(rules, rule)
}

func (e *Executor) writeStdin(ctx context.Context, a map[string]any) (string, error) {
	id := int32(integer(a, "session_id", -1))
	if id < 0 {
		return "", fmt.Errorf("missing required integer argument: session_id")
	}
	return e.Processes.Write(ctx, id, str(a, "chars"), durationMS(a, "yield_time_ms", 0, 250, 30000), integer(a, "max_output_tokens", 10000))
}

func (e *Executor) resolvePath(ctx context.Context, raw string, write bool) (string, error) {
	candidate, err := security.ResolveCandidate(raw, e.Policy)
	if err != nil {
		return "", err
	}
	resolved, err := security.EnsureAllowed(candidate, e.Policy, write)
	if err == nil {
		return resolved, nil
	}
	if !strings.Contains(err.Error(), "outside the allowed workspace roots") || (e.Approve == nil && e.AutoReview == nil) {
		return "", err
	}
	reviewer := e.Approve
	if e.automaticReviewFor("") {
		reviewer = e.AutoReview
	}
	if reviewer == nil {
		return "", err
	}
	decision, approveErr := reviewer(ctx, ApprovalRequest{Kind: "filesystem access", Command: candidate, Workdir: e.Policy.WorkspaceRoot, Reason: map[bool]string{true: "write path is outside configured writable roots", false: "read path is outside configured read roots"}[write], SuggestedRoot: candidate, Write: write})
	if approveErr != nil {
		return "", approveErr
	}
	if decision == Deny {
		return "", fmt.Errorf("filesystem access denied once by local approval policy")
	}
	if write {
		e.Policy.WritableRoots = append(e.Policy.WritableRoots, candidate)
	}
	e.Policy.ReadRoots = append(e.Policy.ReadRoots, candidate)
	if decision == ApproveRemember {
		profile := e.Config.ProjectProfiles[e.Policy.WorkspaceRoot]
		if write {
			profile.WritableRoots = appendUniqueString(profile.WritableRoots, candidate)
		} else {
			profile.ReadRoots = appendUniqueString(profile.ReadRoots, candidate)
		}
		if e.Config.ProjectProfiles == nil {
			e.Config.ProjectProfiles = map[string]model.ProjectTrustProfile{}
		}
		e.Config.ProjectProfiles[e.Policy.WorkspaceRoot] = profile
		if err := appconfig.Save(e.Config); err != nil {
			return "", err
		}
	}
	return security.EnsureAllowed(candidate, e.Policy, write)
}

func (e *Executor) automaticReviewFor(cmd string) bool {
	if e.Policy.PermissionMode == security.PermissionAutoReview {
		return true
	}
	if e.Config.ApprovalsReviewer != nil {
		switch strings.ToLower(strings.TrimSpace(*e.Config.ApprovalsReviewer)) {
		case "auto", "automatic", "automatic-arbitrage", "automatic_arbitrage", "auto-review", "auto_review":
			return true
		}
	}
	for _, rule := range e.Config.CommandApprovalRules {
		if rule.Effect != nil && *rule.Effect == model.AutoReview && security.CommandRuleMatches(rule, cmd) {
			return true
		}
	}
	return false
}
func (e *Executor) readFile(ctx context.Context, a map[string]any) (string, error) {
	path := str(a, "path")
	if path == "" {
		return "", fmt.Errorf("missing required string argument: path")
	}
	p, err := e.resolvePath(ctx, path, false)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("read_file requires a regular file")
	}
	offset := integer(a, "offset_bytes", 0)
	if offset < 0 {
		return "", fmt.Errorf("offset_bytes must be nonnegative")
	}
	limit := min(32000, max(1, integer(a, "max_bytes", 32000)))
	b := make([]byte, limit+4)
	n, err := f.ReadAt(b, int64(offset))
	if err != nil && err != io.EOF {
		return "", err
	}
	end := min(n, limit)
	if end < n {
		for end > 0 && !utf8.RuneStart(b[end]) {
			end--
		}
	}
	if end == 0 && n > 0 {
		_, end = utf8.DecodeRune(b[:n])
	}
	for {
		result := map[string]any{"ok": true, "path": path, "resolved_path": p, "content": string(b[:end])}
		if offset > 0 || int64(end) < st.Size() {
			result["offset_bytes"] = offset
			result["next_offset_bytes"] = offset + end
			result["total_bytes"] = st.Size()
			result["truncated"] = int64(offset+end) < st.Size()
		}
		encoded := marshal(result)
		if len(encoded) <= provider.ToolOutputBytes || end <= 4 {
			return encoded, nil
		}
		end = end * 3 / 4
		for end > 0 && !utf8.RuneStart(b[end]) {
			end--
		}
	}
}
func (e *Executor) writeFile(ctx context.Context, a map[string]any) (string, error) {
	path := str(a, "path")
	if path == "" {
		return "", fmt.Errorf("missing required string argument: path")
	}
	content, ok := firstString(a, "content", "text", "body")
	if !ok {
		return "", fmt.Errorf("missing required string argument: content or text or body")
	}
	p, err := e.resolvePath(ctx, path, true)
	if err != nil {
		return "", err
	}
	beforeBytes, readErr := os.ReadFile(p)
	var before *string
	if readErr == nil {
		value := string(beforeBytes)
		before = &value
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		return "", err
	}
	return marshal(map[string]any{"ok": true, "path": path, "resolved_path": p, "edit": editSummary(path, before, content)}), nil
}
func (e *Executor) replace(ctx context.Context, a map[string]any) (string, error) {
	path := str(a, "path")
	if path == "" {
		return "", fmt.Errorf("missing required string argument: path")
	}
	p, err := e.resolvePath(ctx, path, true)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	find, repl := str(a, "find"), str(a, "replace")
	if find == "" {
		return "", fmt.Errorf("missing required string argument: find")
	}
	old := string(b)
	count := strings.Count(old, find)
	n := 1
	if boolean(a, "all", false) {
		n = -1
	}
	updated := strings.Replace(old, find, repl, n)
	if err := os.WriteFile(p, []byte(updated), 0644); err != nil {
		return "", err
	}
	replacements := count
	if n == 1 {
		if count > 0 {
			replacements = 1
		}
	}
	return marshal(map[string]any{"ok": true, "path": path, "resolved_path": p, "replacements": replacements, "edit": editSummary(path, &old, updated)}), nil
}
func (e *Executor) list(ctx context.Context, a map[string]any) (string, error) {
	raw := str(a, "path")
	if raw == "" {
		raw = "."
	}
	p, err := e.resolvePath(ctx, raw, false)
	if err != nil {
		return "", err
	}
	recursive := boolean(a, "recursive", true)
	max := integer(a, "max_entries", 500)
	if max < 1 {
		max = 1
	} else if max > 5000 {
		max = 5000
	}
	var entries []string
	if recursive {
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == p {
				return nil
			}
			entries = append(entries, path)
			if len(entries) >= max {
				return filepath.SkipAll
			}
			return nil
		})
	} else {
		var ds []os.DirEntry
		ds, err = os.ReadDir(p)
		for _, d := range ds {
			entries = append(entries, filepath.Join(p, d.Name()))
			if len(entries) >= max {
				break
			}
		}
	}
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	entries = uniqueStrings(entries)
	return marshal(map[string]any{"ok": true, "path": raw, "resolved_path": p, "entries": entries}), nil
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

type PythonDefinition struct {
	Name, Description, Path string
	Parameters              map[string]any
}

func DiscoverPythonTools() ([]PythonDefinition, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "tools")
	ds, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []PythonDefinition
	for _, d := range ds {
		if d.IsDir() || filepath.Ext(d.Name()) != ".py" {
			continue
		}
		path := filepath.Join(dir, d.Name())
		def, err := pythonMetadata(path)
		if err != nil {
			return nil, err
		}
		if def != nil {
			out = append(out, *def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func pythonMetadata(path string) (*PythonDefinition, error) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil, nil
	}
	script := `import ast,json,sys
s=open(sys.argv[1],encoding='utf-8').read(); m=ast.parse(s); f=next((x for x in m.body if isinstance(x,ast.FunctionDef) and x.name=='yolomancer_tool'),None)
if f is None: sys.exit(3)
ns={}; exec(compile(ast.Module(body=[f],type_ignores=[]),sys.argv[1],'exec'),ns); print(json.dumps(ns['yolomancer_tool']()))`
	o, err := exec.Command(py, "-I", "-c", script, path).Output()
	if x, ok := err.(*exec.ExitError); ok && x.ExitCode() == 3 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("parse Python tool metadata in %s: %w", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(o, &m); err != nil {
		return nil, err
	}
	name := str(m, "name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".py")
	}
	if !validLocalToolName(name) {
		return nil, fmt.Errorf("invalid Python tool name `%s`; use ASCII letters, numbers, and underscores", name)
	}
	desc := str(m, "description")
	if desc == "" {
		return nil, fmt.Errorf("Python tool `%s` missing description", name)
	}
	params, _ := m["parameters"].(map[string]any)
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &PythonDefinition{name, desc, path, params}, nil
}

func validLocalToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		letter := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
		digit := ch >= '0' && ch <= '9'
		if (!letter && !digit && ch != '_') || (i == 0 && !letter && ch != '_') {
			return false
		}
	}
	return true
}
func (e *Executor) python(ctx context.Context, name string, a map[string]any) (string, error) {
	defs, err := DiscoverPythonTools()
	if err != nil {
		return "", err
	}
	var d *PythonDefinition
	for i := range defs {
		if defs[i].Name == name {
			d = &defs[i]
			break
		}
	}
	if d == nil {
		return "", fmt.Errorf("unknown local tool `%s`", name)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return "", fmt.Errorf("Python tool execution requires python3 in PATH")
	}
	raw, _ := json.Marshal(a)
	script := `import importlib.util,json,subprocess,sys,types
bridge=types.ModuleType('yolomancer_aws')
def call(args):
    p=subprocess.run(['aws']+args+['--output','json'],capture_output=True,text=True)
    if p.returncode: raise RuntimeError(p.stderr.strip())
    return json.loads(p.stdout or '{}')
class NS: pass
bridge.sts=NS(); bridge.sts.get_caller_identity=lambda:call(['sts','get-caller-identity'])
bridge.s3=NS(); bridge.s3.list_buckets=lambda:call(['s3api','list-buckets']); bridge.s3.list_objects=lambda bucket,prefix=None:call(['s3api','list-objects-v2','--bucket',bucket]+(['--prefix',prefix] if prefix else [])); bridge.s3.create_bucket=lambda bucket:call(['s3api','create-bucket','--bucket',bucket]); bridge.s3.delete_bucket=lambda bucket:call(['s3api','delete-bucket','--bucket',bucket])
bridge.iam=NS(); bridge.iam.list_users=lambda:call(['iam','list-users']); bridge.iam.get_user=lambda user_name=None:call(['iam','get-user']+(['--user-name',user_name] if user_name else []))
bridge.ec2=NS(); bridge.ec2.describe_vpcs=lambda:call(['ec2','describe-vpcs'])
bridge.dynamodb=NS(); bridge.dynamodb.list_tables=lambda:call(['dynamodb','list-tables']); bridge.dynamodb.describe_table=lambda table_name:call(['dynamodb','describe-table','--table-name',table_name]); bridge.dynamodb.delete_table=lambda table_name:call(['dynamodb','delete-table','--table-name',table_name]); bridge.dynamodb.create_table=lambda table_name,partition_key='id':call(['dynamodb','create-table','--table-name',table_name,'--attribute-definitions',f'AttributeName={partition_key},AttributeType=S','--key-schema',f'AttributeName={partition_key},KeyType=HASH','--billing-mode','PAY_PER_REQUEST'])
bridge.cloudformation=NS(); bridge.cloudformation.list_stacks=lambda:call(['cloudformation','list-stacks']); bridge.cloudformation.describe_stacks=lambda stack_name=None:call(['cloudformation','describe-stacks']+(['--stack-name',stack_name] if stack_name else [])); bridge.cloudformation.delete_stack=lambda stack_name:call(['cloudformation','delete-stack','--stack-name',stack_name]); bridge.cloudformation.create_stack=lambda stack_name,template_body,capabilities=None:call(['cloudformation','create-stack','--stack-name',stack_name,'--template-body',template_body]+(['--capabilities']+capabilities if capabilities else []))
bridge.route53=NS(); bridge.route53.list_hosted_zones=lambda:call(['route53','list-hosted-zones'])
bridge.account=NS(); bridge.account.list_regions=lambda:call(['account','list-regions']); bridge.get_caller_identity=bridge.sts.get_caller_identity
def request(service,method,url,body='',headers=None,region=None):
    if headers is None: headers={}
    if not isinstance(body,str): body=json.dumps(body); headers=dict(headers); headers.setdefault('content-type','application/json')
    payload={'service':service,'method':method,'url':url,'body':body,'headers':headers,'region':region or ''}
    p=subprocess.run([sys.argv[3],'internal-aws-request'],input=json.dumps(payload),capture_output=True,text=True)
    if p.returncode: raise RuntimeError((p.stderr or p.stdout).strip())
    return json.loads(p.stdout)
bridge.request=request
sys.modules['yolomancer_aws']=bridge
spec=importlib.util.spec_from_file_location('yolomancer_workspace_tool',sys.argv[1]);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);v=m.run(json.loads(sys.argv[2]));print(v if isinstance(v,str) else json.dumps(v))`
	env := provider.ToolEnvironment(e.Config)
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, py, "-I", "-c", script, d.Path, string(raw), executable)
	cmd.Env = env
	cmd.Dir = e.Policy.WorkspaceRoot
	o, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Python tool failed: %s", strings.TrimSpace(string(o)))
	}
	var v any
	if json.Unmarshal(o, &v) != nil {
		v = map[string]any{"ok": true, "output": strings.TrimSpace(string(o))}
	}
	if m, ok := v.(map[string]any); ok {
		if _, exists := m["ok"]; !exists {
			m["ok"] = true
		}
		m["tool"] = name
	}
	return marshal(v), nil
}

func (e *Executor) awsTool(ctx context.Context, a map[string]any) (string, error) {
	action := str(a, "action")
	params, _ := a["arguments"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	if action == "help" {
		return e.python(ctx, "aws_tool", a)
	}
	if action == "request" {
		return e.awsSignedRequest(ctx, params)
	}
	args, err := awsCLIArgs(action, params)
	if err != nil {
		return "", err
	}
	aws, err := exec.LookPath("aws")
	if err != nil {
		return "", fmt.Errorf("AWS CLI is required for Go AWS tool execution: %w", err)
	}
	env := provider.ToolEnvironment(e.Config)
	cmd := exec.CommandContext(ctx, aws, args...)
	cmd.Env = env
	cmd.Dir = e.Policy.WorkspaceRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("AWS operation %s failed: %s", action, strings.TrimSpace(string(out)))
	}
	var value any
	if json.Unmarshal(out, &value) != nil {
		value = map[string]any{"output": strings.TrimSpace(string(out))}
	}
	descriptor := awsDescriptorForAction(action)
	if action == "sts.get_caller_identity" {
		m, _ := value.(map[string]any)
		return marshal(map[string]any{"ok": true, "account": m["Account"], "arn": m["Arn"], "user_id": m["UserId"], "assumed_role": strings.Contains(fmt.Sprint(m["Arn"]), ":assumed-role/"), "permission_scope": descriptor.scope, "aws_operation": descriptor.operation, "aws_service": descriptor.service}), nil
	}
	return marshal(map[string]any{"ok": true, "permission_scope": descriptor.scope, "aws_operation": descriptor.operation, "aws_service": descriptor.service, "data": normalizeAWSData(action, params, value)}), nil
}

func normalizeAWSData(action string, params map[string]any, value any) any {
	root, _ := value.(map[string]any)
	items := func(key string, convert func(map[string]any) map[string]any) []any {
		raw, _ := root[key].([]any)
		out := make([]any, 0, len(raw))
		for _, item := range raw {
			if object, ok := item.(map[string]any); ok {
				out = append(out, convert(object))
			}
		}
		return out
	}
	fields := func(source map[string]any, mapping map[string]string) map[string]any {
		out := map[string]any{}
		for target, original := range mapping {
			out[target] = source[original]
		}
		return out
	}
	switch action {
	case "s3.list_buckets":
		return map[string]any{"buckets": items("Buckets", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"name": "Name", "creation_date": "CreationDate"})
		})}
	case "s3.list_objects":
		return map[string]any{"bucket": str(params, "bucket"), "objects": items("Contents", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"key": "Key", "size": "Size", "last_modified": "LastModified", "etag": "ETag"})
		})}
	case "s3.create_bucket":
		return map[string]any{"bucket": str(params, "bucket"), "location": root["Location"]}
	case "s3.delete_bucket":
		return map[string]any{"bucket": str(params, "bucket")}
	case "iam.list_users":
		return map[string]any{"users": items("Users", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"user_name": "UserName", "arn": "Arn", "user_id": "UserId", "created": "CreateDate"})
		})}
	case "iam.get_user":
		user, _ := root["User"].(map[string]any)
		return map[string]any{"user": fields(user, map[string]string{"user_name": "UserName", "arn": "Arn", "user_id": "UserId", "created": "CreateDate"})}
	case "ec2.describe_vpcs":
		return map[string]any{"vpcs": items("Vpcs", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"vpc_id": "VpcId", "cidr_block": "CidrBlock", "state": "State", "is_default": "IsDefault"})
		})}
	case "dynamodb.list_tables":
		return map[string]any{"table_names": root["TableNames"]}
	case "dynamodb.describe_table", "dynamodb.create_table", "dynamodb.delete_table":
		key := "Table"
		if action != "dynamodb.describe_table" {
			key = "TableDescription"
		}
		table, _ := root[key].(map[string]any)
		mapping := map[string]string{"table_name": "TableName", "table_status": "TableStatus", "table_arn": "TableArn"}
		if action == "dynamodb.describe_table" {
			mapping["item_count"] = "ItemCount"
		}
		return map[string]any{"table": fields(table, mapping)}
	case "cloudformation.list_stacks":
		return map[string]any{"stacks": items("StackSummaries", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"stack_name": "StackName", "stack_id": "StackId", "status": "StackStatus", "creation_time": "CreationTime"})
		})}
	case "cloudformation.describe_stacks":
		return map[string]any{"stacks": items("Stacks", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"stack_name": "StackName", "stack_id": "StackId", "status": "StackStatus", "creation_time": "CreationTime", "description": "Description"})
		})}
	case "cloudformation.create_stack":
		return map[string]any{"stack_id": root["StackId"]}
	case "cloudformation.delete_stack":
		return map[string]any{"stack_name": str(params, "stack_name")}
	case "route53.list_hosted_zones":
		return map[string]any{"hosted_zones": items("HostedZones", func(v map[string]any) map[string]any {
			out := fields(v, map[string]string{"id": "Id", "name": "Name", "resource_record_set_count": "ResourceRecordSetCount"})
			if config, ok := v["Config"].(map[string]any); ok {
				out["private_zone"] = config["PrivateZone"]
			}
			return out
		})}
	case "account.list_regions":
		return map[string]any{"regions": items("Regions", func(v map[string]any) map[string]any {
			return fields(v, map[string]string{"region_name": "RegionName", "opt_status": "RegionOptStatus"})
		})}
	default:
		return value
	}
}

func (e *Executor) awsSignedRequest(ctx context.Context, p map[string]any) (string, error) {
	body := ""
	switch v := p["body"].(type) {
	case string:
		body = v
	case nil:
	default:
		b, _ := json.Marshal(v)
		body = string(b)
	}
	headers := map[string]string{}
	if raw, ok := p["headers"].(map[string]any); ok {
		for k, v := range raw {
			headers[k] = fmt.Sprint(v)
		}
	}
	result, err := provider.SignedAWSRequest(ctx, e.Config, str(p, "service"), str(p, "method"), str(p, "url"), body, headers, str(p, "region"), nil)
	if err != nil {
		return "", err
	}
	return marshal(result), nil
}

func (e *Executor) awsCLI(ctx context.Context, a map[string]any) (string, error) {
	if strings.TrimSpace(str(a, "use_case")) == "" {
		return "", fmt.Errorf("missing required string argument: use_case")
	}
	rawArgs, ok := a["args"].([]any)
	if !ok || len(rawArgs) == 0 {
		return "", fmt.Errorf("`args` must be a non-empty array of strings")
	}
	args := make([]string, 0, len(rawArgs))
	for _, v := range rawArgs {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("`args` must be an array of strings")
		}
		args = append(args, s)
	}
	if reason := awsCLIArgsDenialReason(args); reason != "" {
		return "", fmt.Errorf("%s", reason)
	}
	if err := e.validateAWSCLIFilesystemArgs(args); err != nil {
		return "", err
	}
	if e.Policy.PermissionMode != security.PermissionYolo {
		if e.AutoReview == nil {
			return "", fmt.Errorf("aws_cli requires internal automatic arbitration")
		}
		decision, err := e.AutoReview(ctx, ApprovalRequest{Kind: "shell command", Command: "aws " + strings.Join(args, " "), Workdir: e.Policy.WorkspaceRoot, Reason: "aws_cli fallback requested. Use case: " + str(a, "use_case")})
		if err != nil {
			return "", err
		}
		if decision == Deny {
			return fail("aws_cli denied by internal arbitrage"), nil
		}
	}
	aws, err := exec.LookPath("aws")
	if err != nil {
		return "", err
	}
	env := provider.ToolEnvironment(e.Config)
	timeoutSec := integer(a, "timeout_sec", 120)
	if timeoutSec < 1 {
		timeoutSec = 1
	} else if timeoutSec > 600 {
		timeoutSec = 600
	}
	maxTokens := integer(a, "max_output_tokens", 10000)
	if maxTokens < 1 {
		maxTokens = 1
	} else if maxTokens > 30000 {
		maxTokens = 30000
	}
	timeout := time.Duration(timeoutSec) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, aws, args...)
	cmd.Env = env
	cmd.Dir = e.Policy.WorkspaceRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return marshal(map[string]any{"ok": false, "error": fmt.Sprintf("aws cli timed out after %ds", timeoutSec), "args": args}), nil
	}
	code := 0
	if err != nil {
		if x, ok := err.(*exec.ExitError); ok {
			code = x.ExitCode()
		} else {
			return "", err
		}
	}
	return marshal(map[string]any{
		"ok": code == 0, "program": "aws", "args": args, "status": code,
		"stdout": limitText(stdout.String(), maxTokens*4), "stderr": limitText(stderr.String(), maxTokens*4),
		"credential_source": awsCredentialSource(e.Config), "region": appconfig.Region(e.Config), "use_case": str(a, "use_case"),
		"arbiter": map[string]any{"allow": true, "rationale": map[bool]string{true: "internal arbitrage skipped because Yolo mode is active", false: "approved by internal automatic arbitrage"}[e.Policy.PermissionMode == security.PermissionYolo]},
	}), nil
}

func awsCredentialSource(cfg *model.Config) string {
	if cfg.AWSProfile != nil && strings.TrimSpace(*cfg.AWSProfile) != "" {
		return "profile"
	}
	if cfg.AWSAccessKeyID != nil && strings.TrimSpace(*cfg.AWSAccessKeyID) != "" {
		return "access_keys"
	}
	return "ambient"
}

type awsDescriptor struct{ operation, service, scope string }

func awsDescriptorForAction(action string) awsDescriptor {
	mapping := map[string]awsDescriptor{"sts.get_caller_identity": {"sts:GetCallerIdentity", "sts", "read"}, "s3.list_buckets": {"s3:ListBuckets", "s3", "read"}, "s3.list_objects": {"s3:ListObjectsV2", "s3", "read"}, "s3.create_bucket": {"s3:CreateBucket", "s3", "write"}, "s3.delete_bucket": {"s3:DeleteBucket", "s3", "destructive"}, "iam.list_users": {"iam:ListUsers", "iam", "read"}, "iam.get_user": {"iam:GetUser", "iam", "read"}, "ec2.describe_vpcs": {"ec2:DescribeVpcs", "ec2", "read"}, "dynamodb.list_tables": {"dynamodb:ListTables", "dynamodb", "read"}, "dynamodb.describe_table": {"dynamodb:DescribeTable", "dynamodb", "read"}, "dynamodb.create_table": {"dynamodb:CreateTable", "dynamodb", "write"}, "dynamodb.delete_table": {"dynamodb:DeleteTable", "dynamodb", "destructive"}, "cloudformation.list_stacks": {"cloudformation:ListStacks", "cloudformation", "read"}, "cloudformation.describe_stacks": {"cloudformation:DescribeStacks", "cloudformation", "read"}, "cloudformation.create_stack": {"cloudformation:CreateStack", "cloudformation", "write"}, "cloudformation.delete_stack": {"cloudformation:DeleteStack", "cloudformation", "destructive"}, "route53.list_hosted_zones": {"route53:ListHostedZones", "route53", "read"}, "account.list_regions": {"account:ListRegions", "account", "read"}, "request": {"aws:SignedRequest", "aws", "unknown"}}
	if v, ok := mapping[action]; ok {
		return v
	}
	return awsDescriptor{"aws:Unknown", "aws", "unknown"}
}

func awsCLIArgsDenialReason(args []string) string {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return "aws_cli args must start with an AWS service or command, not a global option"
	}
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return "aws_cli args must not contain NUL bytes"
		}
		if arg == "--profile" || arg == "--debug" || arg == "--no-sign-request" {
			return "aws_cli does not allow profile, debug, or unsigned-request overrides"
		}
	}
	first := args[0]
	second := ""
	if len(args) > 1 {
		second = args[1]
	}
	if first == "configure" {
		return "aws_cli does not allow `aws configure` because it can read or change local credential configuration"
	}
	if first == "sts" && (second == "assume-role" || second == "get-session-token" || second == "get-federation-token") {
		return "aws_cli does not allow `aws sts " + second + "` because it returns credential material"
	}
	if first == "iam" && second == "create-access-key" {
		return "aws_cli does not allow `aws iam create-access-key` because it returns credential material"
	}
	return ""
}

func (e *Executor) validateAWSCLIFilesystemArgs(args []string) error {
	fileOption := func(v string) bool {
		switch v {
		case "--cli-input-json", "--cli-input-yaml", "--generate-cli-skeleton", "--template-body", "--template-file", "--output-template-file", "--parameters", "--tags", "--policy-document", "--assume-role-policy-document", "--role-policy-document", "--zip-file", "--body", "--key-material", "--payload":
			return true
		}
		return false
	}
	for i, arg := range args {
		paths := []string{}
		for _, prefix := range []string{"file://", "fileb://"} {
			if strings.HasPrefix(arg, prefix) {
				paths = append(paths, strings.TrimPrefix(arg, prefix))
			}
		}
		if key, value, ok := strings.Cut(arg, "="); ok && fileOption(key) {
			paths = append(paths, strings.TrimPrefix(strings.TrimPrefix(value, "file://"), "fileb://"))
		}
		if i > 0 && fileOption(args[i-1]) {
			paths = append(paths, strings.TrimPrefix(strings.TrimPrefix(arg, "file://"), "fileb://"))
		}
		if len(args) > 1 && args[0] == "s3" && (args[1] == "cp" || args[1] == "sync") && i >= 2 && !strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "s3://") {
			paths = append(paths, arg)
		}
		for _, path := range paths {
			if path == "-" {
				continue
			}
			if path == "~" || strings.HasPrefix(path, "~/") {
				return fmt.Errorf("aws_cli local path `%s` is outside the workspace", path)
			}
			if _, err := security.ResolvePath(path, e.Policy, true); err != nil {
				return fmt.Errorf("aws_cli local path `%s` is not allowed by the workspace sandbox: %w", path, err)
			}
		}
	}
	return nil
}

func awsCLIArgs(action string, p map[string]any) ([]string, error) {
	req := func(k string) (string, error) {
		v := str(p, k)
		if v == "" {
			return "", fmt.Errorf("missing required string argument: %s", k)
		}
		return v, nil
	}
	switch action {
	case "sts.get_caller_identity":
		return []string{"sts", "get-caller-identity", "--output", "json"}, nil
	case "s3.list_buckets":
		return []string{"s3api", "list-buckets", "--output", "json"}, nil
	case "s3.list_objects":
		b, e := req("bucket")
		if e != nil {
			return nil, e
		}
		a := []string{"s3api", "list-objects-v2", "--bucket", b, "--output", "json"}
		if v := str(p, "prefix"); v != "" {
			a = append(a, "--prefix", v)
		}
		return a, nil
	case "s3.create_bucket":
		b, e := req("bucket")
		return []string{"s3api", "create-bucket", "--bucket", b, "--output", "json"}, e
	case "s3.delete_bucket":
		b, e := req("bucket")
		return []string{"s3api", "delete-bucket", "--bucket", b, "--output", "json"}, e
	case "iam.list_users":
		return []string{"iam", "list-users", "--output", "json"}, nil
	case "iam.get_user":
		a := []string{"iam", "get-user", "--output", "json"}
		if v := str(p, "user_name"); v != "" {
			a = append(a, "--user-name", v)
		}
		return a, nil
	case "ec2.describe_vpcs":
		return []string{"ec2", "describe-vpcs", "--output", "json"}, nil
	case "dynamodb.list_tables":
		return []string{"dynamodb", "list-tables", "--output", "json"}, nil
	case "dynamodb.describe_table":
		v, e := req("table_name")
		return []string{"dynamodb", "describe-table", "--table-name", v, "--output", "json"}, e
	case "dynamodb.delete_table":
		v, e := req("table_name")
		return []string{"dynamodb", "delete-table", "--table-name", v, "--output", "json"}, e
	case "dynamodb.create_table":
		v, e := req("table_name")
		if e != nil {
			return nil, e
		}
		key := str(p, "partition_key")
		if key == "" {
			key = "id"
		}
		return []string{"dynamodb", "create-table", "--table-name", v, "--attribute-definitions", "AttributeName=" + key + ",AttributeType=S", "--key-schema", "AttributeName=" + key + ",KeyType=HASH", "--billing-mode", "PAY_PER_REQUEST", "--output", "json"}, nil
	case "cloudformation.list_stacks":
		return []string{"cloudformation", "list-stacks", "--output", "json"}, nil
	case "cloudformation.describe_stacks":
		a := []string{"cloudformation", "describe-stacks", "--output", "json"}
		if v := str(p, "stack_name"); v != "" {
			a = append(a, "--stack-name", v)
		}
		return a, nil
	case "cloudformation.delete_stack":
		v, e := req("stack_name")
		return []string{"cloudformation", "delete-stack", "--stack-name", v, "--output", "json"}, e
	case "cloudformation.create_stack":
		name, e := req("stack_name")
		if e != nil {
			return nil, e
		}
		body, e := req("template_body")
		if e != nil {
			return nil, e
		}
		a := []string{"cloudformation", "create-stack", "--stack-name", name, "--template-body", body, "--output", "json"}
		if caps, ok := p["capabilities"].([]any); ok {
			for _, c := range caps {
				a = append(a, "--capabilities", fmt.Sprint(c))
			}
		}
		return a, nil
	case "route53.list_hosted_zones":
		return []string{"route53", "list-hosted-zones", "--output", "json"}, nil
	case "account.list_regions":
		return []string{"account", "list-regions", "--output", "json"}, nil
	case "request":
		return nil, fmt.Errorf("generic signed AWS request is not available through the AWS CLI bridge")
	default:
		return nil, fmt.Errorf("unsupported AWS action: %s", action)
	}
}

func editSummary(path string, before *string, after string) map[string]any {
	kind := "add"
	old := ""
	if before != nil {
		kind = "update"
		old = *before
	}
	diff, added, removed, truncated := compactDiff(old, after)
	return map[string]any{"kind": kind, "path": path, "added": added, "removed": removed, "diff": diff, "truncated": truncated}
}

type lineChange struct {
	kind byte
	text string
}

func compactDiff(before, after string) (string, int, int, bool) {
	if before == after {
		return "", 0, 0, false
	}
	old, next := splitDiffLines(before), splitDiffLines(after)
	changes := lineChanges(old, next)
	width := len(strconv.Itoa(maxInt(1, maxInt(len(old), len(next)))))
	lines := []string{"@@"}
	oldLine, newLine, added, removed := 1, 1, 0, 0
	for _, change := range changes {
		lineNumber, sign := newLine, " "
		switch change.kind {
		case '-':
			lineNumber, sign, removed = oldLine, "-", removed+1
			oldLine++
		case '+':
			lineNumber, sign, added = newLine, "+", added+1
			newLine++
		default:
			oldLine++
			newLine++
			if lines[len(lines)-1] != "    ⋮" {
				lines = append(lines, "    ⋮")
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("%*d %s%s", width, lineNumber, sign, strings.TrimRight(change.text, "\r\n")))
	}
	truncated := len(lines) > 120
	if truncated {
		lines = lines[:120]
	}
	return strings.Join(lines, "\n"), added, removed, truncated
}

func splitDiffLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	if value == "" {
		return nil
	}
	lines := strings.Split(value, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func lineChanges(old, next []string) []lineChange {
	if len(old)*len(next) > 250000 {
		out := make([]lineChange, 0, len(old)+len(next))
		for _, line := range old {
			out = append(out, lineChange{'-', line})
		}
		for _, line := range next {
			out = append(out, lineChange{'+', line})
		}
		return out
	}
	table := make([][]int, len(old)+1)
	for i := range table {
		table[i] = make([]int, len(next)+1)
	}
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(next) - 1; j >= 0; j-- {
			if old[i] == next[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = maxInt(table[i+1][j], table[i][j+1])
			}
		}
	}
	var out []lineChange
	i, j := 0, 0
	for i < len(old) && j < len(next) {
		if old[i] == next[j] {
			out = append(out, lineChange{' ', old[i]})
			i++
			j++
		} else if table[i+1][j] >= table[i][j+1] {
			out = append(out, lineChange{'-', old[i]})
			i++
		} else {
			out = append(out, lineChange{'+', next[j]})
			j++
		}
	}
	for ; i < len(old); i++ {
		out = append(out, lineChange{'-', old[i]})
	}
	for ; j < len(next); j++ {
		out = append(out, lineChange{'+', next[j]})
	}
	return out
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func firstString(values map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := values[key].(string); ok {
			return value, true
		}
	}
	return "", false
}
func fail(s string) string { return marshal(map[string]any{"ok": false, "error": s}) }
func toolFailure(call model.ToolCall, message string) string {
	body := map[string]any{"ok": false, "error": message}
	switch {
	case call.Name == "write_file" && strings.Contains(message, "content or text or body"):
		path, _ := call.Arguments["path"].(string)
		if path == "" {
			path = "<path>"
		}
		body["hint"] = fmt.Sprintf("Retry write_file with both required fields exactly like {\"path\":%q,\"content\":\"<complete UTF-8 file contents>\"}. Do not call write_file with only path.", path)
	case call.Name == "write_file" && strings.Contains(message, "path"):
		body["hint"] = "Retry write_file with both required fields: path and content."
	case (call.Name == "exec_command" || call.Name == "shell") && strings.Contains(message, "command"):
		body["hint"] = "Retry exec_command with the required command string."
	case call.Name == "write_stdin" && strings.Contains(message, "session_id"):
		body["hint"] = "Retry write_stdin with the session_id returned by exec_command."
	}
	return marshal(body)
}
func marshal(v any) string                  { b, _ := json.Marshal(v); return string(b) }
func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }
func boolean(m map[string]any, k string, d bool) bool {
	v, ok := m[k].(bool)
	if !ok {
		return d
	}
	return v
}
func integer(m map[string]any, k string, d int) int {
	switch v := m[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := strconv.Atoi(string(v))
		return n
	}
	return d
}
func durationMS(m map[string]any, k string, d, min, max int) time.Duration {
	n := integer(m, k, d)
	if n == 0 {
		return 0
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return time.Duration(n) * time.Millisecond
}
func positive(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
func cloneMap(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var x map[string]any
	_ = json.Unmarshal(b, &x)
	return x
}
func quoteArgs(a []string) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = "'" + strings.ReplaceAll(v, "'", "'\\''") + "'"
	}
	return out
}
func appendUniqueString(v []string, x string) []string {
	for _, s := range v {
		if s == x {
			return v
		}
	}
	return append(v, x)
}
func limitText(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	half := n / 2
	return s[:half] + "\n... output truncated ...\n" + s[len(s)-half:]
}
