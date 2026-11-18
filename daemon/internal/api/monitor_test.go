package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseProbe(t *testing.T) {
	out := `login_claude=true
disk=3221225472 63312289792
win=main rc=1 theme=0 cpid=111 idle=0 ask=0 dlg=0 url=https://claude.ai/code/session_ABC123
win=api rc=0 theme=1 cpid=0 idle=0 ask=0 dlg=0 url=
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
	if a := st.Worktrees["main"]; !a.Window || !a.RC || a.PID != 111 || a.URL != "https://claude.ai/code/session_ABC123" {
		t.Fatalf("%+v", a)
	}
	if a := st.Worktrees["api"]; a.RC || !a.Theme || a.PID != 0 {
		t.Fatalf("%+v", a)
	}
	st.Worktrees["main"] = WorktreeState{Window: true, RC: true, Running: true}
	st.Worktrees["api"] = WorktreeState{Window: true, RC: true, Running: true}
	if !st.stable() {
		t.Fatal("two signed-in Claude worktrees with RC are stable")
	}
	f := parseProbe("login_claude=false\nwin=main rc=1\n")
	f.Worktrees["main"] = WorktreeState{Window: true, RC: true, Running: true}
	if f.ClaudeLoggedIn == nil || *f.ClaudeLoggedIn || f.stable() {
		t.Fatal("ausgeloggt")
	}
	empty := parseProbe("")
	if empty.ClaudeLoggedIn != nil || empty.OpenCodeLoggedIn != nil {
		t.Fatal("without output the login is unknown")
	}
}

func TestParseInstalledState(t *testing.T) {
	st := parseProbe("inst_claude=true\ninst_opencode=false\noc_service=true\n")
	if !st.ClaudeInstalled || st.OpenCodeInstalled || !st.OpenCodeRunning {
		t.Fatalf("installed state: claude=%v opencode=%v running=%v", st.ClaudeInstalled, st.OpenCodeInstalled, st.OpenCodeRunning)
	}
}

func TestParseProbeOpenCodeLogin(t *testing.T) {
	st := parseProbe("login_claude=true\nlogin_opencode=false\n")
	if st.OpenCodeLoggedIn == nil || *st.OpenCodeLoggedIn {
		t.Fatal("OpenCode login false")
	}
}

func TestStableNeedsClaudeLogin(t *testing.T) {
	st := SandboxState{
		ClaudeLoggedIn: boolPtr(true),
		Worktrees: map[string]WorktreeState{
			"main": {Window: true, RC: true, Running: true},
			"api":  {Window: true, RC: true, Running: true},
		},
	}
	if !st.stable() {
		t.Fatal("Claude worktrees with RC are stable")
	}
	st.ClaudeLoggedIn = boolPtr(false)
	if st.stable() {
		t.Fatal("missing Claude login must not be stable")
	}
}

func TestDecideRC(t *testing.T) {
	idle := WorktreeState{Window: true, Running: true, Idle: true}
	busy := WorktreeState{Window: true, Running: true}
	shell := WorktreeState{Window: true, Running: false}
	cases := []struct {
		name     string
		a        WorktreeState
		attempts int
		since    time.Duration
		want     rcAction
	}{
		{"answer prompt", WorktreeState{Ask: true, Running: true}, 0, 0, rcAnswerYes},
		{"Claude idle: /remote-control", idle, 0, time.Hour, rcSlash},
		{"Claude busy: do not disturb", busy, 0, time.Hour, rcNone},
		{"shell: restart Claude", shell, 0, time.Hour, rcRestart},
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
	st := parseProbe("login_claude=true\nwin=main rc=0 theme=0 cpid=1 idle=1 ask=0 url=\nwin=x rc=0 theme=0 cpid=0 idle=0 ask=1 url=\n")
	if a := st.Worktrees["main"]; !a.Idle || a.Ask || a.PID != 1 {
		t.Fatalf("%+v", a)
	}
	if a := st.Worktrees["x"]; a.PID != 0 || a.Idle || !a.Ask {
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
	mo.ResetClaude("s", "work/a")
}

func TestStickyRCSurvivesScrolledOffBanner(t *testing.T) {
	mo := NewMonitor(nil)
	k := "s/a"
	// RC seen (PID 100) -> remember
	a := mo.applySticky(k, WorktreeState{Window: true, Running: true, PID: 100, RC: true, URL: "https://claude.ai/code/session_X"})
	if !a.RC {
		t.Fatal("RC gesehen")
	}
	// banner scrolled out of view, same process still running: stays active, link is kept
	b := mo.applySticky(k, WorktreeState{Window: true, Running: true, PID: 100})
	if !b.RC || b.URL != "https://claude.ai/code/session_X" {
		t.Fatalf("RC must be kept: %+v", b)
	}
	// Claude restarted (new PID), RC not seen yet: do not assume active any more, counters reset
	mo.norc[k], mo.attempts[k], mo.actions[k] = 9, 3, "failed"
	c := mo.applySticky(k, WorktreeState{Window: true, Running: true, PID: 200})
	if c.RC {
		t.Fatal("do not assume active after a restart")
	}
	if mo.attempts[k] != 0 || mo.actions[k] != "" || mo.norc[k] != 0 {
		t.Fatalf("counters must be reset on a new PID: %v %v %v", mo.norc[k], mo.attempts[k], mo.actions[k])
	}
	// Claude exited: remembered state expires
	mo.applySticky(k, WorktreeState{Window: true, Running: true, PID: 300, RC: true})
	if d := mo.applySticky(k, WorktreeState{Window: true, Running: false}); d.RC {
		t.Fatal("an exited process must not be active")
	}
}

func TestMarkRCClearsFailed(t *testing.T) {
	mo := NewMonitor(nil)
	mo.state["s"] = &SandboxState{Worktrees: map[string]WorktreeState{"a": {Window: true, Running: true, PID: 55}}}
	mo.actions["s/a"], mo.attempts["s/a"] = "failed", 3
	if err := mo.MarkRC("s", "a"); err != nil {
		t.Fatal(err)
	}
	if mo.actions["s/a"] != "" || mo.attempts["s/a"] != 0 {
		t.Fatal("failed must be reset")
	}
	if a, _ := mo.Get("s"); !a.Worktrees["a"].RC {
		t.Fatal("RC must count as active")
	}
	if err := mo.MarkRC("s", "nix"); err == nil {
		t.Fatal("unbekannter Worktree")
	}
	mo.state["s"].Worktrees["b"] = WorktreeState{Window: true, Running: false}
	if err := mo.MarkRC("s", "b"); err == nil {
		t.Fatal("cannot be confirmed without a running Claude")
	}
}

func TestSkillsRelinkDecision(t *testing.T) {
	mo := NewMonitor(nil)
	if mo.linked == nil {
		t.Fatal("the map of linked skills hashes must be initialised")
	}
	linked := map[string]string{}
	if !skillsRelinkNeeded(linked, "shop", "h1") {
		t.Fatal("without an entry the links must be created")
	}
	linked["shop"] = "h1"
	if skillsRelinkNeeded(linked, "shop", "h1") {
		t.Fatal("an unchanged skills folder must not relink")
	}
	if !skillsRelinkNeeded(linked, "shop", "h2") {
		t.Fatal("a changed skills folder must relink")
	}
	// A failed relink stores nothing: the next check retries it.
	if !skillsRelinkNeeded(linked, "shop", "h2") {
		t.Fatal("a failed relink must be retried")
	}
	linked["shop"] = "h2"
	if skillsRelinkNeeded(linked, "shop", "h2") || !skillsRelinkNeeded(linked, "other", "h2") {
		t.Fatal("sandboxes must be tracked separately")
	}
}

func TestParseProbePID(t *testing.T) {
	st := parseProbe("login_claude=true\nwin=main rc=1 theme=0 cpid=4242 idle=1 ask=0 url=https://claude.ai/code/session_A\n")
	if a := st.Worktrees["main"]; a.PID != 4242 || !a.RC {
		t.Fatalf("%+v", a)
	}
}

func TestParseProbeDialog(t *testing.T) {
	st := parseProbe("login_claude=true\nwin=main rc=1 theme=0 cpid=7 idle=0 ask=0 dlg=1 url=https://claude.ai/code/session_Z\n")
	a := st.Worktrees["main"]
	if !a.Dialog || !a.RC || a.Idle || a.PID != 7 {
		t.Fatalf("%+v", a)
	}
}

func TestPollInterval(t *testing.T) {
	rc := WorktreeState{Window: true, RC: true, Running: true}
	noRC := WorktreeState{Window: true, Running: true}
	settled := &SandboxState{ClaudeLoggedIn: boolPtr(true), Worktrees: map[string]WorktreeState{"a": rc}}
	loggedOut := &SandboxState{ClaudeLoggedIn: boolPtr(false)}
	unsettled := &SandboxState{ClaudeLoggedIn: boolPtr(true), Worktrees: map[string]WorktreeState{"a": rc, "b": noRC}}
	none := &SandboxState{ClaudeLoggedIn: boolPtr(true), Worktrees: map[string]WorktreeState{}}
	never := func(string) bool { return false }
	gaveUpB := func(w string) bool { return w == "b" }

	cases := []struct {
		name            string
		st              *SandboxState
		active, failing bool
		gaveUp          func(string) bool
		want            time.Duration
	}{
		{"never probed", nil, false, false, never, 0},
		{"app open, settled", settled, true, false, never, intervalCalm},
		{"app open, logged out", loggedOut, true, false, never, intervalFast},
		{"app open, unsettled", unsettled, true, false, never, intervalFast},
		{"app closed, no Claude window", none, false, false, never, intervalIdle},
		{"app closed, logged out without windows", loggedOut, false, false, never, intervalIdle},
		{"app closed, settled", settled, false, false, never, intervalCalm},
		{"app closed, RC still missing", unsettled, false, false, never, intervalFast},
		{"app closed, automation gave up", unsettled, false, false, gaveUpB, intervalCalm},
		{"app closed, nil gaveUp", unsettled, false, false, nil, intervalFast},
		{"probe failing (VM booting)", none, false, true, never, intervalFast},
	}
	for _, c := range cases {
		if got := pollInterval(c.st, c.active, c.failing, c.gaveUp); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeedFullProbe(t *testing.T) {
	now := time.Now()
	st := &SandboxState{FullChecked: now.Add(-time.Minute)}
	if !needFullProbe(nil, false, now) || !needFullProbe(&SandboxState{}, false, now) {
		t.Fatal("the first probe is always full")
	}
	if needFullProbe(st, false, now) {
		t.Fatal("without the app a minute-old full probe is fresh enough")
	}
	if !needFullProbe(st, true, now) {
		t.Fatal("with the app every probe is full")
	}
	st.FullChecked = now.Add(-fullProbeIdle)
	if !needFullProbe(st, false, now) {
		t.Fatal("an old full probe is refreshed even without the app")
	}
}

func TestCheapProbeKeepsExpensiveFields(t *testing.T) {
	full := now()
	prev := SandboxState{ClaudeLoggedIn: boolPtr(true), OpenCodeLoggedIn: boolPtr(true), OpenCodeInstalled: true, FullChecked: full}
	// A cheap probe reports neither login nor the OpenCode version.
	cheap := parseProbe("inst_claude=true\noc_service=false\ndisk=1 2\nwin=main rc=1 cpid=5\n")
	got := carryExpensive(cheap, prev)
	if got.ClaudeLoggedIn == nil || !*got.ClaudeLoggedIn || got.OpenCodeLoggedIn == nil || !*got.OpenCodeLoggedIn || !got.OpenCodeInstalled {
		t.Fatalf("expensive fields lost: %+v", got)
	}
	if !got.FullChecked.Equal(full) {
		t.Fatal("FullChecked must stay at the last full probe")
	}
	if !got.ClaudeInstalled || got.GuestUsed != 1 || got.Worktrees["main"].PID != 5 {
		t.Fatalf("cheap fields must come from the new probe: %+v", got)
	}
}

func now() time.Time { return time.Now() }

func TestProbeScriptCheapPartSkipsBinaries(t *testing.T) {
	cheap := fmt.Sprintf(probeScript, 0)
	if !strings.Contains(cheap, "if [ 0 = 1 ]") {
		t.Fatal("cheap probe must gate the expensive part")
	}
	// Everything that starts claude/opencode must sit inside the gate.
	gate := strings.Index(cheap, "if [ 0 = 1 ]")
	end := strings.Index(cheap, "\nfi\n")
	for _, s := range []string{"claude auth status", "auth list", "--version"} {
		i := strings.Index(cheap, s)
		if i < gate || i > end {
			t.Fatalf("%q runs outside the full-probe gate", s)
		}
	}
}

func TestMonitorTouchAndVMCache(t *testing.T) {
	mo := NewMonitor(nil)
	if mo.activeLocked(time.Now()) {
		t.Fatal("not active before the first API call")
	}
	mo.state["a"] = &SandboxState{Checked: time.Now(), FullChecked: time.Now()}
	mo.Touch()
	if !mo.activeLocked(time.Now()) {
		t.Fatal("active after a call")
	}
	if !mo.state["a"].Checked.IsZero() || !mo.state["a"].FullChecked.IsZero() {
		t.Fatal("the first call after a quiet period makes sandboxes due with a full probe")
	}
	mo.state["a"].Checked = time.Now()
	mo.Touch() // already active: must not reset again
	if mo.state["a"].Checked.IsZero() {
		t.Fatal("calls while active must not force probes")
	}
	mo.listAt, mo.listVMs = time.Now(), nil
	mo.InvalidateVMs()
	if !mo.listAt.IsZero() {
		t.Fatal("InvalidateVMs")
	}
}
