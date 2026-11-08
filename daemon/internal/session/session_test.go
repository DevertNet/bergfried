package session

import (
	"context"
	"errors"
	"io"
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
			t.Fatalf("%q muss abgelehnt werden", bad)
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
		t.Fatal("Hinweis auf --force fehlt")
	}
	_ = context.Background()
}

func TestSessionNamesAndBaseProtection(t *testing.T) {
	if VMName("shop") != "bergfried-shop" {
		t.Fatal(VMName("shop"))
	}
	for _, ok := range []string{"shop", "a", "my-app-2", "x23456789012345678901234"} {
		if ValidSessionName(ok) != nil {
			t.Fatalf("%q muss gültig sein", ok)
		}
	}
	for _, bad := range []string{"base", "base-aa94f061", "x234567890123456789012345", "Shop", "a b", ""} {
		if ValidSessionName(bad) == nil {
			t.Fatalf("%q muss abgelehnt werden", bad)
		}
	}
	for _, yes := range []string{"bergfried-base-aa94f061", "bergfried-base-v3"} {
		if !baseVMRe.MatchString(yes) {
			t.Fatalf("%s ist eine Basis-VM", yes)
		}
	}
	for _, no := range []string{"bergfried-base-foo", "bergfried-shop", "bf-base-aa94f061", "bergfried-base-aa94f06", "bergfried-base-aa94f061x"} {
		if baseVMRe.MatchString(no) {
			t.Fatalf("%s darf nie als Basis gelöscht werden", no)
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
	_ = st.AddSession(store.Session{Name: "shop", VM: "bergfried-shop", TemplateVersion: 3, Created: time.Now()})
	_ = st.AddAgent(store.Agent{Session: "shop", Name: "api", Worktree: "~/work/api", ComposeProject: "shop-api", Created: time.Now()})
	_ = st.AddAgent(store.Agent{Session: "shop", Name: "main", Worktree: "~/work/main", ComposeProject: "shop-main", Created: time.Now()})
	f := &shellFake{out: out}
	return &Manager{L: lima.Client{R: f}, S: st, Actor: "t"}, f
}

func TestAgentRemoveRules(t *testing.T) {
	m, f := mgr(t, "")
	if err := m.AgentRemove(context.Background(), "shop", "main", true, true); err == nil {
		t.Fatal("das Verzeichnis von main darf nie gelöscht werden")
	}
	if err := m.AgentRemove(context.Background(), "shop", "main", false, false); err != nil {
		t.Fatal("main beenden (ohne Verzeichnis) ist erlaubt:", err)
	}
	if ags, _ := m.S.ListAgents("shop"); len(ags) != 1 || ags[0].Name != "api" {
		t.Fatalf("%+v", ags)
	}
	if err := m.AgentRemove(context.Background(), "shop", "../x", false, false); err == nil {
		t.Fatal("ungültiger Name")
	}
	joined := strings.Join(f.calls, "\n")
	if strings.Contains(joined, "worktree remove") {
		t.Fatal("ohne deleteDir darf nichts gelöscht werden")
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
		t.Fatal("Agent darf bei ungesicherter Arbeit nicht entfernt werden")
	}
	for _, c := range f.calls {
		if strings.Contains(c, "worktree remove") || strings.Contains(c, "kill-window") {
			t.Fatalf("vor der Bestätigung darf nichts passieren: %s", c)
		}
	}
	if err := m.AgentRemove(context.Background(), "shop", "api", true, true); err != nil {
		t.Fatal(err)
	}
	if ags, _ := m.S.ListAgents("shop"); len(ags) != 1 {
		t.Fatal("mit force muss der Agent entfernt sein")
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "worktree remove") {
		t.Fatal("Worktree muss entfernt werden")
	}
}

func TestGuidanceMentionsToolsAndOutDir(t *testing.T) {
	g := guidance("/Users/x/.bergfried/sessions/shop/shared/out")
	for _, want := range []string{"list_files", "search_files", "request_file", "request_ssh", "mein Computer", "/shared/out", "nie \"ich habe keinen Zugriff"} {
		if !strings.Contains(strings.ToLower(g), strings.ToLower(want)) {
			t.Fatalf("Hinweistext enthält %q nicht:\n%s", want, g)
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
		t.Fatalf("Token in der Befehlszeile: %s", all)
	}
	for _, want := range []string{"umask 077", "chmod 600 ~/.git-credentials", "credential.helper store"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q fehlt: %s", want, all)
		}
	}
}

func TestGuidanceGitHubWithoutPushTool(t *testing.T) {
	g := guidance("/out")
	if strings.Contains(g, "request_git_push") || !strings.Contains(g, "request_github_token") || !strings.Contains(g, "Gib das Token nie aus") {
		t.Fatal(g)
	}
}
