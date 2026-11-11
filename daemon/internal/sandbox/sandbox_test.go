package sandbox

import (
	"context"
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

func TestClaudeCmd(t *testing.T) {
	c := claudeCmd("shop", "api", "work/api")
	if !strings.Contains(c, "COMPOSE_PROJECT_NAME=shop-api") || !strings.Contains(c, "BERGFRIED_FOLDER=shop/work/api") || !strings.Contains(c, "--remote-control 'shop/api'") || !strings.Contains(c, `cd "$HOME/work/api"`) {
		t.Fatal(c)
	}
	if strings.Contains(c, ".opencode/bin/opencode") {
		t.Fatalf("OpenCode is a sandbox-level service and must not run per worktree: %s", c)
	}
}

func TestLoginCommand(t *testing.T) {
	for in, want := range map[string]string{"": "claude", "claude": "claude", "opencode": ".opencode/bin/opencode"} {
		got, err := LoginCommand(in)
		if err != nil || !strings.Contains(got, want) {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := LoginCommand("codex"); err == nil {
		t.Fatal("unknown runtime must be rejected")
	}
}

func TestOpenCodeInstallScript(t *testing.T) {
	s := openCodeInstallScript()
	for _, want := range []string{
		"set -Eeuo pipefail",
		`FAILED at line $LINENO`,
		"/tmp/opencode-install.log",
		"sudo chown -R",
		"https://opencode.ai/v2/install",
		`bash "$SCRIPT"`,
		"opencode in PATH",
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		`"$OC" --version`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q missing in\n%s", want, s)
		}
	}
	if strings.Contains(s, "--version 2.2.6") {
		t.Fatal("the installer must use the latest version")
	}
	c := openCodeCheckScript()
	if !strings.Contains(c, `[ "$major" -ge 2 ]`) || !strings.Contains(c, "echo yes") {
		t.Fatalf("bad check script:\n%s", c)
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
	_ = st.AddWorktree(store.Worktree{Sandbox: "shop", Name: "api", Path: "~/work/api", ComposeProject: "shop-api", Created: time.Now()})
	_ = st.AddWorktree(store.Worktree{Sandbox: "shop", Name: "main", Path: "~/work/main", ComposeProject: "shop-main", Claude: true, Created: time.Now()})
	f := &shellFake{out: out}
	return &Manager{L: lima.Client{R: f}, S: st, Actor: "t"}, f
}

func TestClaudeRemoveKeepsFolder(t *testing.T) {
	m, f := mgr(t, "")
	if err := m.ClaudeRemove(context.Background(), "shop", "work/api"); err != nil {
		t.Fatal(err)
	}
	if wts, _ := m.S.ListWorktrees("shop"); len(wts) != 1 {
		t.Fatalf("%+v", wts)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "kill-window") {
		t.Fatal("Claude window must be stopped")
	}
	if strings.Contains(joined, "worktree remove") || strings.Contains(joined, "rm -rf") {
		t.Fatalf("the folder must not be deleted: %s", joined)
	}
	if err := m.ClaudeRemove(context.Background(), "shop", "work/nope"); err == nil {
		t.Fatal("unknown folder must fail")
	}
}

func TestGuidanceMentionsToolsAndOutDir(t *testing.T) {
	g := guidance("/Users/x/.bergfried/sandboxes/shop/shared/out")
	for _, want := range []string{"list_files", "search_files", "request_file", "request_folder", "request_ssh", "Prefer `request_folder`", "mein Computer", "/shared/out", "chromium --headless", "gh pr create", "git lfs", "sudo apt-get install", "--dump-dom", "--screenshot", "Never say \"I have no access"} {
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

func TestOpenCodePortIsStableAndInRange(t *testing.T) {
	a, err := openCodePort("shop")
	if err != nil {
		t.Fatal(err)
	}
	if a < openCodePortFirst || a > openCodePortLast {
		t.Fatalf("port %d outside range", a)
	}
	if !portFree(a) {
		t.Fatalf("port %d is not free", a)
	}
}

func TestPairLinkPrefersLoopback(t *testing.T) {
	out := "\x1b[0m\n  http://192.168.5.15:42123/auth/connect/abc\n  http://127.0.0.1:42123/auth/connect/abc\n"
	if got := pairLink(out); got != "http://127.0.0.1:42123/auth/connect/abc" {
		t.Fatalf("pairLink() = %q", got)
	}
	if pairLink("no link here") != "" {
		t.Fatal("no link expected")
	}
}

func TestWindowScriptStartsClaude(t *testing.T) {
	s := windowScript("shop", "api", "work/api")
	for _, want := range []string{
		"tmux new-session -d -s bf -n api",
		"tmux new-window -t bf -n api",
		"tmux send-keys -t bf:api ",
		"COMPOSE_PROJECT_NAME=shop-api",
		"BERGFRIED_FOLDER=shop/work/api",
		`$HOME/work/api`,
		"ps -t \"${tty#/dev/}\" -o comm=",
		"grep -qx claude",
		"hasTrustDialogAccepted",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q missing in\n%s", want, s)
		}
	}
	if strings.Contains(s, ".opencode/bin/opencode") {
		t.Fatalf("no per-worktree OpenCode TUI:\n%s", s)
	}
	if strings.Contains(s, "-n \n") {
		t.Fatalf("empty window name:\n%s", s)
	}
}

func TestNormalizeFolder(t *testing.T) {
	for in, want := range map[string]string{
		"work/api":              "work/api",
		"~/work/api":            "work/api",
		"work/shopware-plugins": "work/shopware-plugins",
		"work/api/sub":          "work/api/sub",
	} {
		got, err := normalizeFolder(in)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "work", "~/Documents", "work/../x", "work/a b", "/etc"} {
		if _, err := normalizeFolder(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestWindowName(t *testing.T) {
	existing := []store.Worktree{{Name: "api"}}
	if got := windowName("work/api", existing); got != "api-2" {
		t.Fatalf("got %q", got)
	}
	if got := windowName("work/shopware-plugins", nil); got != "shopware-plugins" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenCodePairSetupAndLinkValidation(t *testing.T) {
	s := openCodePairSetupScript(42123)
	for _, want := range []string{"service set port 42123", "service set hostname 0.0.0.0", "service restart", "service start"} {
		if !strings.Contains(s, want) {
			t.Fatalf("%q missing in\n%s", want, s)
		}
	}
	good := "http://127.0.0.1:42123/auth/connect/abc"
	if !linkUsesPort(good, 42123) {
		t.Fatal("own port must be accepted")
	}
	if linkUsesPort("http://127.0.0.1:49374/auth/connect/abc", 42123) {
		t.Fatal("default port must be rejected")
	}
}
