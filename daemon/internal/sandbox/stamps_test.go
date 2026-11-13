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
