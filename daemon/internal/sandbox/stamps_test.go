package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/store"
)

func TestRestartReasons(t *testing.T) {
	base := store.HelperStamp{Guidance: "g", MCP: "m"}
	for _, c := range []struct {
		name        string
		have        bool
		stored, cur store.HelperStamp
		want        []string
	}{
		{name: "no stamp", have: false, stored: store.HelperStamp{}, cur: base, want: []string{"unknown"}},
		{name: "equal", have: true, stored: base, cur: base, want: []string{}},
		{name: "guidance differs", have: true, stored: base, cur: store.HelperStamp{Guidance: "x", MCP: "m"}, want: []string{"guidance"}},
		{name: "mcp differs", have: true, stored: base, cur: store.HelperStamp{Guidance: "g", MCP: "x"}, want: []string{"mcp"}},
		{name: "both differ", have: true, stored: base, cur: store.HelperStamp{Guidance: "x", MCP: "y"}, want: []string{"guidance", "mcp"}},
		{name: "skills differ", have: true, stored: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s1"}, cur: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s2"}, want: []string{"skills"}},
		{name: "old row without skills", have: true, stored: base, cur: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s"}, want: []string{"skills"}},
		{name: "all differ", have: true, stored: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s1"}, cur: store.HelperStamp{Guidance: "x", MCP: "y", Skills: "s2"}, want: []string{"guidance", "mcp", "skills"}},
		{name: "all equal", have: true, stored: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s"}, cur: store.HelperStamp{Guidance: "g", MCP: "m", Skills: "s"}, want: []string{}},
	} {
		got := RestartReasons(c.have, c.stored, c.cur)
		if got == nil {
			t.Fatalf("%s: result must be non-nil", c.name)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") || len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCurrentStamp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bergfried-mcp")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERGFRIED_MCP_BIN", bin)
	m := &Manager{Home: t.TempDir()}
	a, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	if a.Guidance == "" || a.MCP == "" {
		t.Fatalf("empty stamp: %+v", a)
	}
	b, err := m.CurrentStamp("shop")
	if err != nil || a != b {
		t.Fatalf("two calls must give the same stamp: %+v %+v %v", a, b, err)
	}
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	if c.MCP == a.MCP {
		t.Fatal("a changed binary must change the MCP hash")
	}
	if c.Guidance != a.Guidance {
		t.Fatal("the guidance hash must not depend on the binary")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CurrentStamp("shop"); err == nil {
		t.Fatal("a missing binary must return an error")
	}
}

func TestCurrentStampSkills(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bergfried-mcp")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERGFRIED_MCP_BIN", bin)
	m := &Manager{Home: t.TempDir()}
	empty, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(m.SkillsDir(), "demo")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(skill, "SKILL.md")
	if err := os.WriteFile(file, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	if a.Skills == empty.Skills {
		t.Fatal("a new skill must change the skills hash")
	}
	if a.Guidance != empty.Guidance || a.MCP != empty.MCP {
		t.Fatal("the skills hash must not depend on guidance or the binary")
	}
	if err := os.WriteFile(file, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	if b.Skills == a.Skills {
		t.Fatal("a changed skill file must change the skills hash")
	}
	if b.Guidance != a.Guidance || b.MCP != a.MCP {
		t.Fatal("only the skills hash may change")
	}
}

func TestWindowScriptMarkersAndEnsureWindow(t *testing.T) {
	s := windowScript("shop", "api", "work/api")
	if !strings.Contains(s, "echo kept") || !strings.Contains(s, "echo started") {
		t.Fatalf("markers missing:\n%s", s)
	}
	if !strings.Contains(s, "echo kept; exit 0") {
		t.Fatalf("the kept marker must be printed before the early exit:\n%s", s)
	}
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "echo started" {
		t.Fatalf("started must be the last command, got %q:\n%s", got, s)
	}

	m, f := mgr(t, "started\n")
	started, err := m.ensureWindow(context.Background(), "bergfried-shop", "shop", "api", "work/api")
	if err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}
	f.out = "kept\n"
	started, err = m.ensureWindow(context.Background(), "bergfried-shop", "shop", "api", "work/api")
	if err != nil || started {
		t.Fatalf("kept: started=%v err=%v", started, err)
	}
}

func TestRecordStamp(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bergfried-mcp")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERGFRIED_MCP_BIN", bin)
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{S: st, Home: t.TempDir(), Actor: "t"}
	want, err := m.CurrentStamp("shop")
	if err != nil {
		t.Fatal(err)
	}
	m.recordStamp("shop", ClaudeTarget("api"))
	got, err := st.ListHelperStamps("shop")
	if err != nil {
		t.Fatal(err)
	}
	if got[ClaudeTarget("api")] != want || len(got) != 1 {
		t.Fatalf("stored stamps: %v, want %+v", got, want)
	}
}

func TestClaudeRestartInstallsHelpersBeforeRestart(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bergfried-mcp")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERGFRIED_MCP_BIN", bin)
	m, f := mgr(t, "started\n")
	if err := m.ClaudeRestart(context.Background(), "shop", "work/main"); err != nil {
		t.Fatal(err)
	}
	copyIdx, installIdx, killIdx := -1, -1, -1
	for i, c := range f.calls {
		if strings.Contains(c, "cp "+bin+" bergfried-shop:/tmp/bergfried-mcp") {
			copyIdx = i
		}
		if strings.HasSuffix(c, "bash -s") {
			installIdx = i
		}
		if strings.HasPrefix(c, "shell --tty=false bergfried-shop -- bash -c tmux kill-window") {
			killIdx = i
		}
	}
	if copyIdx < 0 {
		t.Fatalf("the binary was not copied into the VM: %v", f.calls)
	}
	if installIdx < 0 {
		t.Fatalf("the helper setup script was not run: %v", f.calls)
	}
	if killIdx < 0 {
		t.Fatalf("the Claude window was not stopped: %v", f.calls)
	}
	if copyIdx > killIdx || installIdx > killIdx {
		t.Fatalf("helpers must be installed before the restart: %v", f.calls)
	}
	if len(f.stdin) == 0 || !strings.Contains(f.stdin[0], "install -m755 /tmp/bergfried-mcp") {
		t.Fatalf("the helper setup script must install the copied binary: %q", f.stdin)
	}
	stamps, err := m.S.ListHelperStamps("shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stamps[ClaudeTarget("main")]; !ok {
		t.Fatalf("no stamp for %s: %v", ClaudeTarget("main"), stamps)
	}
}

func TestClaudeRestartDoesNotKillWindowWhenInstallFails(t *testing.T) {
	t.Setenv("BERGFRIED_MCP_BIN", filepath.Join(t.TempDir(), "missing"))
	m, f := mgr(t, "started\n")
	if err := m.ClaudeRestart(context.Background(), "shop", "work/main"); err == nil {
		t.Fatal("a missing binary must fail the restart")
	}
	if joined := strings.Join(f.calls, "\n"); strings.Contains(joined, "kill-window") {
		t.Fatalf("the old window must stay untouched: %s", joined)
	}
}
