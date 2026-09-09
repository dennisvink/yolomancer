package security

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"github.com/dennisvink/yolomancer/internal/model"
)

type PermissionMode string

const (
	PermissionDefault    PermissionMode = "default"
	PermissionGapped     PermissionMode = "gapped"
	PermissionAutoReview PermissionMode = "automatic-arbitrage"
	PermissionYolo       PermissionMode = "yolo"
)

type Policy struct {
	WorkspaceRoot  string
	ReadRoots      []string
	WritableRoots  []string
	PermissionMode PermissionMode
	ApprovalMode   string
	NetworkPolicy  string
	SandboxMode    string
}

func BuildPolicy(cfg *model.Config, root string) Policy {
	profile, hasProfile := cfg.ProjectProfiles[root]
	modeRaw := ""
	for _, key := range []string{"yolomancer_permission_mode", "YOLOMANCER_PERMISSION_MODE", "VIBECODE_CLI_PERMISSION_MODE"} {
		if value := os.Getenv(key); value != "" {
			modeRaw = value
			break
		}
	}
	if modeRaw == "" && hasProfile && profile.PermissionMode != nil {
		modeRaw = *profile.PermissionMode
	}
	mode := parseMode(modeRaw)
	p := Policy{WorkspaceRoot: root, PermissionMode: mode, ApprovalMode: "never", NetworkPolicy: "allow", SandboxMode: "workspace-write", ReadRoots: []string{root}, WritableRoots: []string{root}}
	if mode == PermissionGapped || mode == PermissionAutoReview {
		p.NetworkPolicy = "approve"
	}
	if mode == PermissionYolo {
		p.ReadRoots = []string{"/"}
		p.WritableRoots = []string{"/"}
		p.SandboxMode = "danger-full-access"
		return p
	}
	writableConfigured := cfg.WritableRoots
	if hasProfile {
		writableConfigured = profile.WritableRoots
	}
	p.WritableRoots = appendUniqueRoots(p.WritableRoots, resolveRoots(root, writableConfigured))
	if hasProfile {
		p.ReadRoots = appendUniqueRoots(p.ReadRoots, resolveRoots(root, profile.ReadRoots))
	}
	p.ReadRoots = appendUniqueRoots(p.ReadRoots, resolveRoots(root, cfg.WritableRoots))
	if raw := firstEnv("yolomancer_writable_roots", "YOLOMANCER_WRITABLE_ROOTS", "VIBECODE_CLI_WRITABLE_ROOTS"); raw != "" {
		extra := resolveRoots(root, filepath.SplitList(raw))
		p.WritableRoots = appendUniqueRoots(p.WritableRoots, extra)
		p.ReadRoots = appendUniqueRoots(p.ReadRoots, extra)
	}
	return p
}

func parseMode(v string) PermissionMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yolo", "full", "full-access":
		return PermissionYolo
	case "gapped":
		return PermissionGapped
	case "automatic-arbitrage", "automatic_arbitrage", "arbitrage":
		return PermissionAutoReview
	default:
		return PermissionDefault
	}
}
func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
func appendUniqueRoots(dst, values []string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}

func resolveRoots(root string, values []string) []string {
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if v == "/workspace" {
			v = root
		} else if strings.HasPrefix(v, "/workspace/") {
			v = filepath.Join(root, strings.TrimPrefix(v, "/workspace/"))
		} else if !filepath.IsAbs(v) {
			v = filepath.Join(root, v)
		}
		if x, err := CanonicalMissing(v); err == nil {
			out = append(out, x)
		}
	}
	return out
}

func ResolvePath(raw string, p Policy, write bool) (string, error) {
	resolved, err := ResolveCandidate(raw, p)
	if err != nil {
		return "", err
	}
	return EnsureAllowed(resolved, p, write)
}

func ResolveCandidate(raw string, p Policy) (string, error) {
	if strings.TrimSpace(raw) == "" || raw == "." || raw == "/workspace" {
		raw = p.WorkspaceRoot
	} else if strings.HasPrefix(raw, "/workspace/") {
		raw = filepath.Join(p.WorkspaceRoot, strings.TrimPrefix(raw, "/workspace/"))
	} else if !filepath.IsAbs(raw) {
		raw = filepath.Join(p.WorkspaceRoot, raw)
	}
	return CanonicalMissing(raw)
}

func EnsureAllowed(resolved string, p Policy, write bool) (string, error) {
	roots := p.ReadRoots
	if write {
		roots = p.WritableRoots
	}
	allowed := false
	for _, r := range roots {
		canonicalRoot, e := CanonicalMissing(r)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(canonicalRoot, resolved)
		if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("path `%s` is outside the allowed workspace roots", resolved)
	}
	if write {
		for _, r := range p.WritableRoots {
			if canonicalRoot, e := CanonicalMissing(r); e == nil {
				r = canonicalRoot
			}
			for _, name := range []string{".git", ".yolomancer"} {
				protected := filepath.Join(r, name)
				rel, e := filepath.Rel(protected, resolved)
				if e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return "", fmt.Errorf("path `%s` is inside a protected subpath and remains read-only", resolved)
				}
			}
		}
	}
	return resolved, nil
}

func CanonicalMissing(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cur := filepath.Clean(abs)
	var tail []string
	for {
		_, err = os.Lstat(cur)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("unable to resolve path `%s`", path)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
	cur, err = filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		cur = filepath.Join(cur, tail[i])
	}
	return filepath.Clean(cur), nil
}

func PlanMutationReason(command string) string {
	c := strings.TrimSpace(command)
	if strings.Contains(c, " > ") || strings.Contains(c, " >> ") || strings.HasSuffix(c, ">") {
		return "shell redirection can write files"
	}
	if strings.Contains(c, "apply_patch") {
		return "applying patches edits files"
	}
	for _, seg := range Segments(command) {
		if len(seg) == 0 {
			continue
		}
		first := seg[0]
		switch first {
		case "touch", "mkdir", "rm", "mv", "cp", "install", "tee", "truncate":
			return fmt.Sprintf("`%s` commonly changes files", first)
		case "prettier":
			if has(seg, "--write") || has(seg, "-w") {
				return "prettier --write rewrites files"
			}
		case "eslint":
			if has(seg, "--fix") {
				return "eslint --fix rewrites files"
			}
		case "sed":
			for _, s := range seg {
				if s == "-i" || strings.HasPrefix(s, "-i") {
					return "sed -i rewrites files"
				}
			}
		case "git":
			if len(seg) > 1 && has([]string{"add", "am", "apply", "checkout", "cherry-pick", "clean", "commit", "merge", "mv", "pull", "push", "rebase", "reset", "restore", "rm"}, seg[1]) {
				return "git " + seg[1] + " changes repository state"
			}
		}
	}
	return ""
}

func DangerousReason(command string) string {
	for _, s := range Segments(command) {
		if len(s) == 0 {
			continue
		}
		if s[0] == "rm" && (has(s, "-r") || has(s, "-rf") || has(s, "-fr") || has(s, "--recursive")) {
			return "recursive deletion can permanently remove files"
		}
		if s[0] == "git" && len(s) > 1 && ((s[1] == "reset" && has(s, "--hard")) || s[1] == "clean") {
			return "destructive git command can discard work"
		}
		if s[0] == "sudo" {
			return "sudo runs with elevated privileges"
		}
		if s[0] == "dd" || s[0] == "mkfs" {
			return "command can overwrite storage"
		}
	}
	return ""
}

var networkCommands = map[string]bool{"curl": true, "wget": true, "ssh": true, "scp": true, "git": true, "npm": true, "yarn": true, "pnpm": true, "pip": true, "go": true, "brew": true, "docker": true, "aws": true, "gh": true, "nc": true, "telnet": true}

type NetworkTarget struct{ Protocol, Host string }

func RequestsNetwork(command string) bool {
	if len(NetworkTargets(command)) > 0 {
		return true
	}
	for _, seg := range Segments(command) {
		seg = normalized(seg)
		if len(seg) > 0 && networkCommands[filepath.Base(seg[0])] {
			if seg[0] != "git" || len(seg) < 2 || has([]string{"clone", "fetch", "pull", "push", "ls-remote", "submodule"}, seg[1]) {
				return true
			}
		}
	}
	return false
}

var urlRE = regexp.MustCompile(`(?i)\b(https?|ssh|git)://[^\s'"<>]+`)
var gitSSHRE = regexp.MustCompile(`(?i)\bgit@([a-z0-9.-]+)`)

func NetworkTargets(command string) []NetworkTarget {
	seen := map[string]bool{}
	var out []NetworkTarget
	for _, raw := range urlRE.FindAllString(command, -1) {
		u, e := url.Parse(strings.TrimRight(raw, ",.;)"))
		if e == nil && u.Hostname() != "" {
			k := u.Scheme + "://" + strings.ToLower(u.Hostname())
			if !seen[k] {
				seen[k] = true
				out = append(out, NetworkTarget{strings.ToLower(u.Scheme), strings.ToLower(u.Hostname())})
			}
		}
	}
	for _, match := range gitSSHRE.FindAllStringSubmatch(command, -1) {
		host := strings.ToLower(match[1])
		key := "ssh://" + host
		if !seen[key] {
			seen[key] = true
			out = append(out, NetworkTarget{"ssh", host})
		}
	}
	return out
}

func ParseNetworkRule(raw string) (NetworkTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return NetworkTarget{}, fmt.Errorf("usage: /allow-net <url-or-host>")
	}
	if !strings.Contains(raw, "://") {
		return NetworkTarget{}, fmt.Errorf("network rule must look like protocol://host")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme == "" || u.Hostname() == "" {
		return NetworkTarget{}, fmt.Errorf("invalid network rule `%s`", raw)
	}
	host := strings.ToLower(u.Hostname())
	if strings.HasPrefix(u.Host, "*.") && !strings.HasPrefix(host, "*.") {
		host = "*." + host
	}
	return NetworkTarget{strings.ToLower(u.Scheme), host}, nil
}

func RuleMatches(rule model.NetworkApprovalRule, target NetworkTarget) bool {
	if !strings.EqualFold(rule.Protocol, target.Protocol) {
		return false
	}
	rh := strings.ToLower(rule.Host)
	th := strings.ToLower(target.Host)
	if strings.HasPrefix(rh, "*.") {
		suffix := strings.TrimPrefix(rh, "*")
		return strings.HasSuffix(th, suffix) && th != strings.TrimPrefix(suffix, ".")
	}
	return rh == th
}

func WildcardHost(host string) string {
	parts := strings.Split(strings.Trim(host, "."), ".")
	if len(parts) < 3 {
		return strings.ToLower(host)
	}
	return "*." + strings.Join(parts[1:], ".")
}

func ApprovalPrefix(command string) []string {
	segs := Segments(command)
	if len(segs) == 0 {
		return nil
	}
	s := normalized(segs[0])
	if len(s) == 0 {
		return nil
	}
	base := filepath.Base(s[0])
	if base == "git" && len(s) > 1 {
		return []string{s[0], s[1]}
	}
	if base == "go" && len(s) > 1 {
		return []string{s[0], s[1]}
	}
	return []string{s[0]}
}

func CommandRuleMatches(rule model.CommandApprovalRule, command string) bool {
	tokens := normalized(first(Segments(command)))
	if len(tokens) < len(rule.Prefix) {
		return false
	}
	for i, v := range rule.Prefix {
		if tokens[i] != v {
			return false
		}
	}
	return true
}

func Segments(command string) [][]string {
	tokens := Tokens(command)
	var out [][]string
	var cur []string
	for _, t := range tokens {
		if t == ";" || t == "&&" || t == "||" || t == "|" {
			if len(cur) > 0 {
				out = append(out, cur)
				cur = nil
			}
		} else {
			cur = append(cur, t)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func Tokens(s string) []string {
	var out []string
	var b strings.Builder
	quote := rune(0)
	esc := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if esc {
			b.WriteRune(ch)
			esc = false
			continue
		}
		if ch == '\\' && quote != '\'' {
			esc = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				b.WriteRune(ch)
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if unicode.IsSpace(ch) {
			flush()
			continue
		}
		if strings.ContainsRune(";|&", ch) {
			flush()
			if i+1 < len(r) && r[i+1] == ch && ch != ';' {
				out = append(out, string([]rune{ch, ch}))
				i++
			} else {
				out = append(out, string(ch))
			}
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}

func unwrap(v []string) []string {
	for len(v) >= 3 && (v[0] == "bash" || v[0] == "sh" || v[0] == "zsh") && (v[1] == "-c" || v[1] == "-lc") {
		return Tokens(v[2])
	}
	return v
}
func normalized(v []string) []string {
	v = stripAssignments(v)
	if len(v) > 0 && v[0] == "env" {
		v = stripAssignments(v[1:])
	}
	v = unwrap(v)
	return stripAssignments(v)
}
func stripAssignments(v []string) []string {
	for len(v) > 0 {
		token := v[0]
		eq := strings.IndexByte(token, '=')
		if eq <= 0 {
			break
		}
		valid := true
		for i, r := range token[:eq] {
			if !(r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r))) {
				valid = false
				break
			}
		}
		if !valid {
			break
		}
		v = v[1:]
	}
	return v
}
func first[T any](v [][]T) []T {
	if len(v) == 0 {
		return nil
	}
	return v[0]
}
func has(v []string, x string) bool {
	for _, s := range v {
		if s == x {
			return true
		}
	}
	return false
}

func SandboxCommand(command, workdir string, p Policy) (string, []string, bool) {
	if p.SandboxMode == "danger-full-access" {
		return "", nil, false
	}
	if runtime.GOOS == "darwin" {
		var rules strings.Builder
		rules.WriteString(`(version 1) (allow default) (deny file-write*)`)
		for _, root := range p.WritableRoots {
			rules.WriteString(` (allow file-write* (subpath "` + strings.ReplaceAll(root, `"`, `\"`) + `"))`)
		}
		for _, root := range []string{"/tmp", "/private/tmp", os.TempDir()} {
			rules.WriteString(` (allow file-write* (subpath "` + strings.ReplaceAll(root, `"`, `\"`) + `"))`)
		}
		for _, root := range p.WritableRoots {
			rules.WriteString(` (deny file-write* (subpath "` + strings.ReplaceAll(filepath.Join(root, ".git"), `"`, `\"`) + `"))`)
			rules.WriteString(` (deny file-write* (subpath "` + strings.ReplaceAll(filepath.Join(root, ".yolomancer"), `"`, `\"`) + `"))`)
		}
		profile := rules.String()
		return "/usr/bin/sandbox-exec", []string{"-p", profile, "/bin/sh", "-lc", command}, true
	}
	if runtime.GOOS == "linux" {
		if b, err := findExecutable("bwrap"); err == nil {
			args := []string{"--ro-bind", "/", "/"}
			for _, root := range p.WritableRoots {
				args = append(args, "--bind", root, root)
				for _, protected := range []string{filepath.Join(root, ".git"), filepath.Join(root, ".yolomancer")} {
					if _, err := os.Stat(protected); err == nil {
						args = append(args, "--ro-bind", protected, protected)
					}
				}
			}
			args = append(args, "--chdir", workdir, "/bin/sh", "-lc", command)
			return b, args, true
		}
	}
	return "", nil, false
}
func findExecutable(name string) (string, error) {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(d, name)
		if st, e := os.Stat(p); e == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found")
}
