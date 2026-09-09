package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dennisvink/yolomancer/internal/model"
)

func TestPlanModeGuard(t *testing.T) {
	cases := map[string]string{"git status": "", "go test ./...": "", "rm -rf build": "`rm` commonly changes files", "git reset --hard": "git reset changes repository state", "sed -i s/a/b/ x": "sed -i rewrites files", "echo hi > x": "shell redirection can write files"}
	for cmd, want := range cases {
		if got := PlanMutationReason(cmd); got != want {
			t.Errorf("%q: got %q want %q", cmd, got, want)
		}
	}
}
func TestDangerousCommands(t *testing.T) {
	if DangerousReason("rm -rf foo") == "" {
		t.Fatal("rm -rf must be dangerous")
	}
	if DangerousReason("python -c 'print(1)'") != "" {
		t.Fatal("python -c was misclassified")
	}
}
func TestNetworkDetectionThroughWrappersAndSegments(t *testing.T) {
	for _, cmd := range []string{"curl https://example.com", "bash -lc 'curl https://example.com'", "echo ok; wget https://example.com"} {
		if !RequestsNetwork(cmd) {
			t.Errorf("network not detected in %q", cmd)
		}
	}
}
func TestNetworkTargetsAndRules(t *testing.T) {
	targets := NetworkTargets("curl https://api.example.com/x && git clone ssh://git.example.org/x && ssh git@code.example.net")
	if len(targets) != 3 {
		t.Fatalf("got %#v", targets)
	}
	if targets[2] != (NetworkTarget{"ssh", "code.example.net"}) {
		t.Fatalf("git SSH target missing: %#v", targets)
	}
	r := model.NetworkApprovalRule{Action: model.NetworkAllow, Protocol: "https", Host: "*.example.com"}
	if !RuleMatches(r, NetworkTarget{"https", "api.example.com"}) {
		t.Fatal("wildcard should match subdomain")
	}
	if RuleMatches(r, NetworkTarget{"https", "example.com"}) {
		t.Fatal("wildcard must not match apex")
	}
}

func TestParseNetworkRuleRequiresProtocol(t *testing.T) {
	if _, err := ParseNetworkRule("example.com"); err == nil {
		t.Fatal("bare host accepted")
	}
	got, err := ParseNetworkRule("https://*.example.com")
	if err != nil || got.Host != "*.example.com" {
		t.Fatalf("%#v %v", got, err)
	}
}
func TestApprovalPrefix(t *testing.T) {
	got := ApprovalPrefix("git status && echo ok")
	if len(got) != 2 || got[0] != "git" || got[1] != "status" {
		t.Fatalf("got %#v", got)
	}
	got = ApprovalPrefix("bash -lc 'curl https://example.com'")
	if len(got) == 0 || got[0] != "curl" {
		t.Fatalf("wrapper not unwrapped: %#v", got)
	}
	got = ApprovalPrefix("TOKEN=x git status")
	if len(got) != 2 || got[0] != "git" {
		t.Fatalf("assignment not stripped: %#v", got)
	}
}
func TestResolvePathConfinementAndProtection(t *testing.T) {
	root := t.TempDir()
	p := Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}
	if _, err := ResolvePath("../escape", p, false); err == nil {
		t.Fatal("escape allowed")
	}
	if _, err := ResolvePath(".git/config", p, true); err == nil {
		t.Fatal(".git write allowed")
	}
	want, _ := CanonicalMissing(filepath.Join(root, "a/new.txt"))
	if got, err := ResolvePath("/workspace/a/new.txt", p, true); err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}
func TestCanonicalMissingStopsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	p := Policy{WorkspaceRoot: root, ReadRoots: []string{root}, WritableRoots: []string{root}}
	if _, err := ResolvePath("link/file", p, true); err == nil {
		t.Fatal("symlink escape allowed")
	}
}
func TestTokensAndSegments(t *testing.T) {
	got := Segments(`FOO=x git status && echo "a b" | sed -n '1p'`)
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	if got[1][1] != "a b" {
		t.Fatalf("quotes lost: %#v", got)
	}
}

func TestPermissionModePolicies(t *testing.T) {
	root := t.TempDir()
	cfg := &model.Config{ProjectProfiles: map[string]model.ProjectTrustProfile{}}
	p := BuildPolicy(cfg, root)
	if p.NetworkPolicy != "allow" || p.ApprovalMode != "never" || p.SandboxMode != "workspace-write" {
		t.Fatalf("default policy %#v", p)
	}
	mode := "gapped"
	cfg.ProjectProfiles[root] = model.ProjectTrustProfile{PermissionMode: &mode}
	p = BuildPolicy(cfg, root)
	if p.PermissionMode != PermissionGapped || p.NetworkPolicy != "approve" {
		t.Fatalf("gapped policy %#v", p)
	}
	mode = "yolo"
	cfg.ProjectProfiles[root] = model.ProjectTrustProfile{PermissionMode: &mode}
	p = BuildPolicy(cfg, root)
	if len(p.WritableRoots) != 1 || p.WritableRoots[0] != "/" || p.SandboxMode != "danger-full-access" {
		t.Fatalf("yolo policy %#v", p)
	}
}

func TestPermissionEnvironmentTakesPrecedence(t *testing.T) {
	root := t.TempDir()
	mode := "yolo"
	cfg := &model.Config{ProjectProfiles: map[string]model.ProjectTrustProfile{root: {PermissionMode: &mode}}}
	t.Setenv("YOLOMANCER_PERMISSION_MODE", "gapped")
	if got := BuildPolicy(cfg, root).PermissionMode; got != PermissionGapped {
		t.Fatalf("got %s", got)
	}
}
