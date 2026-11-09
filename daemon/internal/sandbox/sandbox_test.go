package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

func TestValidName(t *testing.T) {
	for _, ok := range []string{"shop", "a", "my-app-2"} {
		if ValidName(ok) != nil {
			t.Fatal(ok)
		}
	}
	for _, bad := range []string{"", "Shop", "1a", "a b", "a;rm -rf", "a'b", "../x", strings.Repeat("a", 40)} {
		if ValidName(bad) == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestAgentCmdIsolatesComposeProject(t *testing.T) {
	c := AgentCmd("shop", "api")
	if !strings.Contains(c, "COMPOSE_PROJECT_NAME=shop-api") || !strings.Contains(c, "--remote-control 'shop/api'") {
		t.Fatal(c)
	}
}

func TestErrUnpushed(t *testing.T) {
	var err error = ErrUnpushed{Report: "x"}
	if !strings.Contains(err.Error(), "--force") {
		t.Fatal("hint about --force is missing")
	}
	_ = context.Background()
}

func TestSandboxNamesAndBaseProtection(t *testing.T) {
	if VMName("shop") != "bergfried-shop" {
		t.Fatal(VMName("shop"))
	}
	for _, ok := range []string{"shop", "a", "my-app-2", "x23456789012345678901234"} {
		if ValidSandboxName(ok) != nil {
			t.Fatalf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"base", "base-aa94f061", "x234567890123456789012345", "Shop", "a b", ""} {
		if ValidSandboxName(bad) == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	for _, yes := range []string{"bergfried-base-aa94f061", "bergfried-base-v3"} {
		if !baseVMRe.MatchString(yes) {
			t.Fatalf("%s is a base VM", yes)
		}
	}
	for _, no := range []string{"bergfried-base-foo", "bergfried-shop", "bf-base-aa94f061", "bergfried-base-aa94f06", "bergfried-base-aa94f061x"} {
		if baseVMRe.MatchString(no) {
			t.Fatalf("%s must never be deleted as a base", no)
		}
	}
}

type shellFake struct {
	out   string
	calls []string
}

func (f *shellFake) Run(_ context.Context, a ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(a, " "))
	return []byte(f.out), nil
}

func mgr(t *testing.T, out string) (*Manager, *shellFake) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddSandbox(store.Sandbox{Name: "shop", VM: "bergfried-shop", TemplateVersion: 3, Created: time.Now()})
	_ = st.AddAgent(store.Agent{Sandbox: "shop", Name: "api", Worktree: "~/work/api", ComposeProject: "shop-api", Created: time.Now()})
	_ = st.AddAgent(store.Agent{Sandbox: "shop", Name: "main", Worktree: "~/work/main", ComposeProject: "shop-main", Created: time.Now()})
	f := &shellFake{out: out}
	return &Manager{L: lima.Client{R: f}, S: st, Actor: "t"}, f
}

func TestAgentRemoveRules(t *testing.T) {
	m, f := mgr(t, "")
	if err := m.AgentRemove(context.Background(), "shop", "main", true, true); err == nil {
		t.Fatal("main's directory must never be deleted")
	}
	if err := m.AgentRemove(context.Background(), "shop", "main", false, false); err != nil {
		t.Fatal("terminating main (without the directory) is allowed:", err)
	}
	if ags, _ := m.S.ListAgents("shop"); len(ags) != 1 || ags[0].Name != "api" {
		t.Fatalf("%+v", ags)
	}
	if err := m.AgentRemove(context.Background(), "shop", "../x", false, false); err == nil {
		t.Fatal("invalid name")
	}
	joined := strings.Join(f.calls, "\n")
	if strings.Contains(joined, "worktree remove") {
		t.Fatal("without deleteDir nothing may be deleted")
	}
}

func TestAgentRemoveDirtyNeedsForce(t *testing.T) {
	m, f := mgr(t, " M file.go\nabc123 unpushed")
	err := m.AgentRemove(context.Background(), "shop", "api", true, false)
	var d ErrAgentDirty
	if !errors.As(err, &d) {
		t.Fatalf("erwartet ErrAgentDirty, bekam %v", err)
	}
	if ags, _ := m.S.ListAgents("shop"); len(ags) != 2 {
		t.Fatal("the agent must not be removed while work is unsaved")
	}
	for _, c := range f.calls {
		if strings.Contains(c, "worktree remove") || strings.Contains(c, "kill-window") {
			t.Fatalf("nothing may happen before the confirmation: %s", c)
		}
	}
	if err := m.AgentRemove(context.Background(), "shop", "api", true, true); err != nil {
		t.Fatal(err)
	}
	if ags, _ := m.S.ListAgents("shop"); len(ags) != 1 {
		t.Fatal("with force the agent must be removed")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "worktree remove") {
		t.Fatal("worktree must be removed")
	}
}

func TestGuidanceMentionsToolsAndOutDir(t *testing.T) {
	g := guidance("/Users/x/.bergfried/sandboxes/shop/shared/out")
	for _, want := range []string{"list_files", "search_files", "request_file", "request_ssh", "mein Computer", "/shared/out", "chromium --headless", "gh pr create", "git lfs", "--dump-dom", "--screenshot", "Never say \"I have no access"} {
		if !strings.Contains(strings.ToLower(g), strings.ToLower(want)) {
			t.Fatalf("guidance text does not contain %q:\n%s", want, g)
		}
	}
}

type stdinRec struct {
	args  [][]string
	stdin []string
}

func (r *stdinRec) Run(_ context.Context, args ...string) ([]byte, error) {
	r.args = append(r.args, args)
	return nil, nil
}
func (r *stdinRec) RunStdin(_ context.Context, in io.Reader, args ...string) ([]byte, error) {
	b, _ := io.ReadAll(in)
	r.args = append(r.args, args)
	r.stdin = append(r.stdin, string(b))
	return nil, nil
}

func TestInstallGitTokenUsesStdinNotArgs(t *testing.T) {
	const tok = "github_pat_11ABCDEFG0abcdefghijklmnop"
	rec := &stdinRec{}
	m := &Manager{L: lima.Client{R: rec}}
	if err := m.InstallGitToken(context.Background(), "bergfried-shop", " "+tok+"\n"); err != nil {
		t.Fatal(err)
	}
	if len(rec.stdin) != 1 || rec.stdin[0] != "https://x-access-token:"+tok+"@github.com\n" {
		t.Fatalf("stdin: %q", rec.stdin)
	}
	all := strings.Join(rec.args[0], " ")
	if strings.Contains(all, tok) {
		t.Fatalf("token on the command line: %s", all)
	}
	for _, want := range []string{"umask 077", "chmod 600 ~/.git-credentials", "credential.helper store"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q fehlt: %s", want, all)
		}
	}
}

func TestGuidanceGitHubWithoutPushTool(t *testing.T) {
	g := guidance("/out")
	if strings.Contains(g, "request_git_push") || strings.Contains(g, "ci_status") || !strings.Contains(g, "request_github_token") || !strings.Contains(g, "Never print the token") {
		t.Fatal(g)
	}
}

func TestMigrateHomeRenamesAndLeavesSymlink(t *testing.T) {
	home := t.TempDir()
	if err := MigrateHome(home); err != nil { // nothing there: no error, nothing created
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, "sandboxes")); err == nil {
		t.Fatal("without the old folder nothing may be created")
	}
	if err := os.MkdirAll(filepath.Join(home, "sessions", "shop", "shared", "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, "sessions", "shop", "shared", "out", "x.txt"), []byte("hi"), 0o600)
	if err := MigrateHome(home); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "sandboxes", "shop", "shared", "out", "x.txt")); err != nil || string(b) != "hi" {
		t.Fatal(string(b), err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "sessions", "shop", "shared", "out", "x.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("old path (Lima mounts) must keep working: %v", err)
	}
	if fi, _ := os.Lstat(filepath.Join(home, "sessions")); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("old name must be a symlink")
	}
	if err := MigrateHome(home); err != nil { // idempotent
		t.Fatal(err)
	}
	// both real folders: do not guess
	h2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(h2, "sessions"), 0o700)
	_ = os.MkdirAll(filepath.Join(h2, "sandboxes"), 0o700)
	if err := MigrateHome(h2); err == nil {
		t.Fatal("both present: error expected")
	}
}

func TestUpgradeToolsScriptInstallsTools(t *testing.T) {
	for _, want := range []string{"make", "chromium", "gh", "git-lfs", "git lfs install --system", "gh auth login"} {
		if !strings.Contains(upgradeToolsScript, want) {
			t.Errorf("upgrade script lacks %q", want)
		}
	}
	if strings.Contains(upgradeToolsScript, "echo ") {
		t.Error("the script must not echo anything (token)")
	}
}
