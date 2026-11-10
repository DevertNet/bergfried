package api

import (
	"testing"
	"time"
)

func TestParseProbe(t *testing.T) {
	out := `login_claude=true
disk=3221225472 63312289792
win=main rc=1 theme=0 cpid=111 opid=0 url=https://claude.ai/code/session_ABC123
win=api rc=0 theme=1 cpid=0 opid=222 url=
`
	st := parseProbe(out)
	if st.ClaudeLoggedIn == nil || !*st.ClaudeLoggedIn {
		t.Fatal("login")
	}
	if st.OpenCodeLoggedIn != nil {
		t.Fatal("OpenCode login must stay unknown when it was not checked")
	}
	if st.GuestUsed != 3221225472 || st.GuestSize != 63312289792 {
		t.Fatalf("%d %d", st.GuestUsed, st.GuestSize)
	}
	if a := st.Agents["main"]; !a.Window || !a.RC || a.ClaudePID != 111 || a.OpenCodePID != 0 || a.URL != "https://claude.ai/code/session_ABC123" {
		t.Fatalf("%+v", a)
	}
	if a := st.Agents["api"]; a.RC || !a.Theme || a.OpenCodePID != 222 {
		t.Fatalf("%+v", a)
	}
	st.Agents["main"] = AgentState{Window: true, RC: true, Runtime: "claude"}
	st.Agents["api"] = AgentState{Window: true, RC: true, Runtime: "claude"}
	if !st.stable() {
		t.Fatal("two signed-in Claude agents with RC are stable")
	}
	f := parseProbe("login_claude=false\nwin=main rc=1 runtime=claude\n")
	f.Agents["main"] = AgentState{Window: true, RC: true, Runtime: "claude"}
	if f.ClaudeLoggedIn == nil || *f.ClaudeLoggedIn || f.stable() {
		t.Fatal("ausgeloggt")
	}
	empty := parseProbe("")
	if empty.ClaudeLoggedIn != nil || empty.OpenCodeLoggedIn != nil {
		t.Fatal("without output the login is unknown")
	}
}

func TestParseProbeOpenCode(t *testing.T) {
	st := parseProbe(`login_claude=true
login_opencode=false
oc dir=/home/x/work/api busy=200 idle=100 updated=200 id=ses_2
oc dir=/home/x/work/api busy=10 idle=100 updated=50 id=ses_1
`)
	if st.OpenCodeLoggedIn == nil || *st.OpenCodeLoggedIn {
		t.Fatal("OpenCode login false")
	}
	oc := st.OpenCode["/home/x/work/api"]
	if oc.Busy != 200 || oc.Idle != 100 || oc.Updated != 200 {
		t.Fatalf("newest session must win: %+v", oc)
	}
}

func TestStableMixedRuntimes(t *testing.T) {
	st := SandboxState{
		ClaudeLoggedIn:   boolPtr(true),
		OpenCodeLoggedIn: boolPtr(true),
		Agents: map[string]AgentState{
			"main": {Window: true, RC: true, Runtime: "claude"},
			"api":  {Window: true, Runtime: "opencode"},
		},
	}
	if !st.stable() {
		t.Fatal("mixed sandbox with both logins is stable")
	}
	st.OpenCodeLoggedIn = boolPtr(false)
	if st.stable() {
		t.Fatal("missing OpenCode login must not be stable")
	}
}

func TestDecideRC(t *testing.T) {
	idle := AgentState{Window: true, Running: true, Idle: true}
	busy := AgentState{Window: true, Running: true}
	shell := AgentState{Window: true, Running: false}
	cases := []struct {
		name     string
		a        AgentState
		attempts int
		since    time.Duration
		want     rcAction
	}{
		{"answer prompt", AgentState{Ask: true, Running: true}, 0, 0, rcAnswerYes},
		{"Claude idle: /remote-control", idle, 0, time.Hour, rcSlash},
		{"Claude busy: do not disturb", busy, 0, time.Hour, rcNone},
		{"shell: restart agent", shell, 0, time.Hour, rcRestart},
		{"too soon after last attempt", idle, 1, 5 * time.Second, rcNone},
		{"again after waiting", idle, 1, time.Minute, rcSlash},
		{"give up after three attempts", idle, rcMaxAttempts, time.Hour, rcGiveUp},
		{"shell: give up after three attempts", shell, rcMaxAttempts, time.Hour, rcGiveUp},
	}
	for _, c := range cases {
		if got := decideRC(c.a, c.attempts, c.since); got != c.want {
			t.Errorf("%s: %v != %v", c.name, got, c.want)
		}
	}
}

func TestParseProbeNewFields(t *testing.T) {
	st := parseProbe("login_claude=true\nwin=main rc=0 theme=0 cpid=1 opid=0 idle=1 ask=0 url=\nwin=x rc=0 theme=0 cpid=0 opid=2 idle=0 ask=1 url=\n")
	if a := st.Agents["main"]; !a.Idle || a.Ask || a.ClaudePID != 1 {
		t.Fatalf("%+v", a)
	}
	if a := st.Agents["x"]; a.ClaudePID != 0 || a.OpenCodePID != 2 || a.Idle || !a.Ask {
		t.Fatalf("%+v", a)
	}
}

func TestMonitorHoldAndErrorGrace(t *testing.T) {
	mo := NewMonitor(nil)
	mo.Hold("s")
	if mo.hold["s"] != 1 {
		t.Fatal("Hold")
	}
	mo.Release("s")
	if mo.hold["s"] != 0 {
		t.Fatal("Release")
	}
	mo.ResetAgent("s", "a")
}

func TestStickyRCSurvivesScrolledOffBanner(t *testing.T) {
	mo := NewMonitor(nil)
	k := "s/a"
	// RC gesehen (PID 100) -> merken
	a := mo.applySticky(k, AgentState{Window: true, Running: true, PID: 100, RC: true, URL: "https://claude.ai/code/session_X"})
	if !a.RC {
		t.Fatal("RC gesehen")
	}
	// banner scrolled out of view, same process still running: stays active, link is kept
	b := mo.applySticky(k, AgentState{Window: true, Running: true, PID: 100})
	if !b.RC || b.URL != "https://claude.ai/code/session_X" {
		t.Fatalf("RC must be kept: %+v", b)
	}
	// Claude restarted (new PID), RC not seen yet: do not assume active any more, counters reset
	mo.norc[k], mo.attempts[k], mo.actions[k] = 9, 3, "failed"
	c := mo.applySticky(k, AgentState{Window: true, Running: true, PID: 200})
	if c.RC {
		t.Fatal("do not assume active after a restart")
	}
	if mo.attempts[k] != 0 || mo.actions[k] != "" || mo.norc[k] != 0 {
		t.Fatalf("counters must be reset on a new PID: %v %v %v", mo.norc[k], mo.attempts[k], mo.actions[k])
	}
	// Claude exited: remembered state expires
	mo.applySticky(k, AgentState{Window: true, Running: true, PID: 300, RC: true})
	if d := mo.applySticky(k, AgentState{Window: true, Running: false}); d.RC {
		t.Fatal("an exited process must not be active")
	}
}

func TestMarkRCClearsFailed(t *testing.T) {
	mo := NewMonitor(nil)
	mo.state["s"] = &SandboxState{Agents: map[string]AgentState{"a": {Window: true, Running: true, PID: 55}}}
	mo.actions["s/a"], mo.attempts["s/a"] = "failed", 3
	if err := mo.MarkRC("s", "a"); err != nil {
		t.Fatal(err)
	}
	if mo.actions["s/a"] != "" || mo.attempts["s/a"] != 0 {
		t.Fatal("failed must be reset")
	}
	if a, _ := mo.Get("s"); !a.Agents["a"].RC {
		t.Fatal("RC must count as active")
	}
	if err := mo.MarkRC("s", "nix"); err == nil {
		t.Fatal("unbekannter Agent")
	}
	mo.state["s"].Agents["b"] = AgentState{Window: true, Running: false}
	if err := mo.MarkRC("s", "b"); err == nil {
		t.Fatal("cannot be confirmed without a running Claude")
	}
}

func TestParseProbePID(t *testing.T) {
	st := parseProbe("login_claude=true\nwin=main rc=1 theme=0 cpid=4242 opid=0 idle=1 ask=0 url=https://claude.ai/code/session_A\n")
	if a := st.Agents["main"]; a.ClaudePID != 4242 || !a.RC {
		t.Fatalf("%+v", a)
	}
}

func TestParseProbeDialog(t *testing.T) {
	st := parseProbe("login_claude=true\nwin=main rc=1 theme=0 cpid=7 opid=0 idle=0 ask=0 dlg=1 url=https://claude.ai/code/session_Z\n")
	a := st.Agents["main"]
	if !a.Dialog || !a.RC || a.Idle || a.ClaudePID != 7 {
		t.Fatalf("%+v", a)
	}
}

func TestMarkRCOpenCodeRejected(t *testing.T) {
	mo := NewMonitor(nil)
	mo.state["s"] = &SandboxState{Agents: map[string]AgentState{
		"api": {Window: true, Running: true, PID: 5, Runtime: "opencode"},
	}}
	if err := mo.MarkRC("s", "api"); err == nil {
		t.Fatal("Remote Control must be rejected for OpenCode")
	}
}
