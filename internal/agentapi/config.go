package agentapi

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dennisvink/yolomancer/internal/model"
	"github.com/dennisvink/yolomancer/internal/security"
	"github.com/dennisvink/yolomancer/internal/tools"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Version int              `yaml:"version"`
	Server  ServerConfig     `yaml:"server"`
	Agents  map[string]Agent `yaml:"agents"`
}
type ServerConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	BearerToken    string `yaml:"bearer_token"`
	BearerTokenEnv string `yaml:"bearer_token_env"`
	MaxConcurrent  int    `yaml:"max_concurrent"`
}
type Agent struct {
	Workspace   string      `yaml:"workspace"`
	Tools       ToolConfig  `yaml:"tools"`
	Permissions Permissions `yaml:"permissions"`
	Limits      Limits      `yaml:"limits"`
}
type ToolConfig struct {
	Builtin []string `yaml:"builtin"`
	Python  []string `yaml:"python"`
}
type Permissions struct {
	Mode       string   `yaml:"mode"`
	Approval   string   `yaml:"approval"`
	ReadRoots  []string `yaml:"read_roots"`
	WriteRoots []string `yaml:"write_roots"`
	Network    string   `yaml:"network"`
}
type Limits struct {
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	TokenBudget    uint64 `yaml:"token_budget"`
}
type Registered struct {
	Agent   Agent
	Policy  security.Policy
	Python  []tools.PythonDefinition
	Specs   []map[string]any
	Allowed map[string]bool
}

// Load accepts YAML or JSON with identical fields. No cwd changes or imports of
// extension module bodies: metadata inspection only evaluates literal globals.
func Load(path, host string, port int) (*Config, map[string]Registered, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(b) > 1<<20 {
		return nil, nil, fmt.Errorf("agents configuration exceeds 1 MiB")
	}
	var cfg Config
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return nil, nil, fmt.Errorf("invalid agents configuration: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, nil, fmt.Errorf("configuration must contain exactly one document")
	}
	if cfg.Version != 1 || len(cfg.Agents) == 0 || len(cfg.Agents) > 32 {
		return nil, nil, fmt.Errorf("version must be 1 and agents must contain 1–32 entries")
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "127.0.0.1"
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8081
	}
	if host != "" {
		cfg.Server.Host = host
	}
	if port != 0 {
		cfg.Server.Port = port
	}
	if cfg.Server.MaxConcurrent == 0 {
		cfg.Server.MaxConcurrent = 1
	}
	if err := cfg.Server.Validate(); err != nil {
		return nil, nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	registered := make(map[string]Registered)
	for name, a := range cfg.Agents {
		if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`).MatchString(name) {
			return nil, nil, fmt.Errorf("invalid agent name %q", name)
		}
		r, err := register(a, filepath.Dir(abs))
		if err != nil {
			return nil, nil, fmt.Errorf("agent %s: %w", name, err)
		}
		registered[name] = r
	}
	return &cfg, registered, nil
}

func (s *ServerConfig) Validate() error {
	ip := net.ParseIP(s.Host)
	if ip == nil {
		return fmt.Errorf("server.host must be a literal IP address")
	}
	if s.Port < 1 || s.Port > 65535 || s.MaxConcurrent < 1 || s.MaxConcurrent > 64 {
		return fmt.Errorf("invalid server port or max_concurrent (1–64)")
	}
	if s.BearerToken != "" && s.BearerTokenEnv != "" {
		return fmt.Errorf("configure bearer_token OR bearer_token_env, not both")
	}
	if s.BearerTokenEnv != "" {
		value, ok := os.LookupEnv(s.BearerTokenEnv)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("configured bearer token environment variable is missing or empty")
		}
		s.BearerToken = value
	}
	if s.BearerToken != "" && (len(s.BearerToken) < 32 || len(s.BearerToken) > 4096 || strings.ContainsAny(s.BearerToken, " \t\r\n")) {
		return fmt.Errorf("bearer token must be 32–4096 characters without whitespace")
	}
	if !ip.IsLoopback() && s.BearerToken == "" {
		return fmt.Errorf("non-loopback binding requires bearer_token or bearer_token_env")
	}
	return nil
}

func register(a Agent, base string) (Registered, error) {
	r := Registered{Agent: a, Allowed: map[string]bool{}}
	resolve := func(p string) (string, error) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		return filepath.EvalSymlinks(p)
	}
	if a.Workspace == "" {
		a.Workspace = "."
	}
	root, err := resolve(a.Workspace)
	if err != nil {
		return r, err
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return r, fmt.Errorf("workspace must be an existing directory")
	}
	a.Workspace = root
	if a.Permissions.Mode == "" {
		a.Permissions.Mode = "restricted"
	}
	if a.Permissions.Approval == "" {
		a.Permissions.Approval = "deny"
	}
	if a.Permissions.Approval != "deny" {
		return r, fmt.Errorf("API approval must be deny; interactive/automatic escalation is not supported")
	}
	if a.Permissions.Network == "" {
		a.Permissions.Network = "deny"
	}
	if a.Permissions.Network != "allow" && a.Permissions.Network != "deny" {
		return r, fmt.Errorf("network must be allow or deny")
	}
	r.Policy = security.Policy{WorkspaceRoot: root, ApprovalMode: "never", NetworkPolicy: a.Permissions.Network, SandboxMode: "workspace-write"}
	switch a.Permissions.Mode {
	case "yolo":
		if a.Permissions.Network != "allow" || len(a.Permissions.ReadRoots)+len(a.Permissions.WriteRoots) != 0 {
			return r, fmt.Errorf("yolo requires network: allow and no path restrictions; use restricted for file-only policy")
		}
		r.Policy.PermissionMode = security.PermissionYolo
		r.Policy.SandboxMode = "danger-full-access"
		r.Policy.ReadRoots = []string{filepath.VolumeName(root) + string(filepath.Separator)}
		r.Policy.WritableRoots = r.Policy.ReadRoots
	case "restricted":
		r.Policy.PermissionMode = security.PermissionDefault
		for _, pair := range []struct {
			in  []string
			out *[]string
		}{{a.Permissions.ReadRoots, &r.Policy.ReadRoots}, {a.Permissions.WriteRoots, &r.Policy.WritableRoots}} {
			for _, p := range pair.in {
				resolved, err := resolve(p)
				if err != nil {
					return r, err
				}
				*pair.out = append(*pair.out, resolved)
			}
		}
	default:
		return r, fmt.Errorf("mode must be yolo or restricted")
	}
	if a.Limits.TimeoutSeconds == 0 {
		a.Limits.TimeoutSeconds = 600
	}
	if a.Limits.TimeoutSeconds < 1 || a.Limits.TimeoutSeconds > 86400 {
		return r, fmt.Errorf("timeout_seconds must be 1–86400")
	}
	if a.Limits.TokenBudget == 0 {
		a.Limits.TokenBudget = 200000
	}
	paths := []string{}
	for _, p := range a.Tools.Python {
		path, err := resolve(p)
		if err != nil {
			return r, err
		}
		paths = append(paths, path)
	}
	r.Python, err = tools.LoadPythonTools(paths)
	if err != nil {
		return r, err
	}
	builtins := tools.SpecsWithPython(model.ModeDefault, &model.Config{}, nil)
	known := map[string]map[string]any{}
	for _, spec := range builtins {
		known[spec["name"].(string)] = spec
	}
	for _, name := range a.Tools.Builtin {
		spec, exists := known[name]
		if !exists {
			return r, fmt.Errorf("unknown or unavailable builtin %q", name)
		}
		if r.Allowed[name] {
			return r, fmt.Errorf("duplicate tool %q", name)
		}
		if name == "get_goal" || name == "create_goal" || name == "update_goal" {
			return r, fmt.Errorf("API requests are bounded turns; persistent goal tools are CLI-only")
		}
		if a.Permissions.Mode != "yolo" && (name == "exec_command" || name == "write_stdin" || name == "aws_cli") {
			return r, fmt.Errorf("%s requires yolo: arbitrary commands are not sandboxed by API file policy", name)
		}
		r.Specs = append(r.Specs, spec)
		r.Allowed[name] = true
	}
	if len(r.Python) > 0 && a.Permissions.Mode != "yolo" {
		return r, fmt.Errorf("Python extensions require yolo mode")
	}
	for _, def := range r.Python {
		if def.Name == "aws_tool" || def.Name == "aws_cli" {
			return r, fmt.Errorf("reserved tool name %q", def.Name)
		}
		if _, exists := known[def.Name]; exists || r.Allowed[def.Name] {
			return r, fmt.Errorf("duplicate/reserved tool name %q", def.Name)
		}
		r.Allowed[def.Name] = true
	}
	all := tools.SpecsWithPython(model.ModeDefault, &model.Config{}, r.Python)
	for _, spec := range all {
		for _, def := range r.Python {
			if spec["name"] == def.Name {
				r.Specs = append(r.Specs, spec)
			}
		}
	}
	if len(r.Specs) > 64 {
		return r, fmt.Errorf("at most 64 tools per agent")
	}
	r.Agent = a
	return r, nil
}
