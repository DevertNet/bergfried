package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/sandbox"
)

// probeScript runs in the VM and reports logins, disk usage and, per agent window, its processes
// and (for Claude) the Remote Control status. need_claude/need_opencode are filled in by check.
const probeScript = `export PATH="$HOME/.local/bin:$HOME/.opencode/bin:$PATH"
echo "inst_claude=$(command -v claude >/dev/null 2>&1 && echo true || echo false)"
echo "inst_opencode=$([ -x "$HOME/.opencode/bin/opencode" ] && echo true || echo false)"
if [ %[1]d = 1 ]; then echo "login_claude=$(claude auth status 2>/dev/null | jq -r '.loggedIn // false' 2>/dev/null || echo false)"; fi
if [ %[2]d = 1 ]; then
  OC="$HOME/.opencode/bin/opencode"
  if [ -x "$OC" ]; then
    echo "login_opencode=$("$OC" auth list --format json 2>/dev/null | jq -r '[.[]?.connections[]?] | length > 0' 2>/dev/null || echo false)"
  else echo "login_opencode=false"; fi
fi
df -B1 --output=used,size / | tail -1 | awk '{print "disk="$1" "$2}'
if tmux has-session -t bf 2>/dev/null; then
  for w in $(tmux list-windows -t bf -F '#W'); do
    p=$(tmux capture-pane -p -J -t "bf:$w" -S - 2>/dev/null)
    rc=0; echo "$p" | grep -qE 'remote-control is active|/rc active|available in the Claude mobile app' && rc=1
    # The RC dialog (opened by /remote-control, offers "Disconnect") means: RC is active; it blocks input.
    dlg=0; echo "$p" | tail -15 | grep -q 'Disconnect this session' && { dlg=1; rc=1; }
    th=0; echo "$p" | grep -qE 'Choose the text style|Press Enter to continue' && th=1
    tty=$(tmux display-message -p -t "bf:$w" '#{pane_tty}' 2>/dev/null)
    cpid=$(ps -t "${tty#/dev/}" -o pid=,comm= 2>/dev/null | awk '$2=="claude"{print $1; exit}')
    opid=$(ps -t "${tty#/dev/}" -o pid=,comm= 2>/dev/null | awk '$2=="opencode"{print $1; exit}')
    idle=0; echo "$p" | grep -q '❯' && ! echo "$p" | grep -q 'esc to interrupt' && [ "$dlg" = 0 ] && idle=1
    ask=0; echo "$p" | grep -q 'Enable Remote Control' && ask=1
    url=$(echo "$p" | grep -oE 'https://claude\.ai/code/session_[A-Za-z0-9_]+' | tail -1)
    echo "win=$w rc=$rc theme=$th idle=$idle ask=$ask dlg=$dlg cpid=$cpid opid=$opid url=$url"
  done
fi
if [ %[2]d = 1 ] && [ -x "$HOME/.opencode/bin/opencode" ] && pgrep -x opencode >/dev/null 2>&1; then
  "$HOME/.opencode/bin/opencode" api GET /api/session 2>/dev/null | jq -r '.data[]? | "oc dir=\(.location.directory) busy=\(.time.busy // 0) idle=\(.time.idle // 0) updated=\(.time.updated // 0) id=\(.id)"' 2>/dev/null || true
fi`

type AgentState struct {
	Window, RC, Theme bool
	Idle, Ask         bool
	Running           bool   // process of the agent's runtime runs in the window
	PID               int    // PID of that process (0 = none)
	ClaudePID         int    // parsed from the probe, before the runtime is known
	OpenCodePID       int    // parsed from the probe, before the runtime is known
	Runtime           string // "claude" or "opencode"
	Dialog            bool   // RC dialog ("Disconnect this session") is open
	URL               string
	Action            string // "", "activating", "failed", "waiting", "working" – display state
}

type rcAction int

const (
	rcNone rcAction = iota
	rcAnswerYes
	rcSlash
	rcRestart
	rcGiveUp
)

const (
	rcMaxAttempts = 3
	rcMissTicks   = 4 // this many checks in a row without RC before activating (grace period for booting)
	rcRetryAfter  = 20 * time.Second
)

// decideRC decides how an agent without active Remote Control is brought back on track.
// Order: answer an open prompt, restart if Claude is not running (shell only),
// type /remote-control if the input field is empty; give up after rcMaxAttempts.
func decideRC(a AgentState, attempts int, sinceLast time.Duration) rcAction {
	if a.Ask {
		return rcAnswerYes
	}
	if attempts >= rcMaxAttempts {
		return rcGiveUp
	}
	if sinceLast < rcRetryAfter {
		return rcNone
	}
	if !a.Running {
		return rcRestart
	}
	if a.Idle {
		return rcSlash
	}
	return rcNone
}

type SandboxState struct {
	ClaudeLoggedIn       *bool // nil = not checked (no Claude agent in this sandbox)
	OpenCodeLoggedIn     *bool // nil = not checked (no OpenCode agent in this sandbox)
	ClaudeInstalled      bool
	OpenCodeInstalled    bool
	GuestUsed, GuestSize int64
	Agents               map[string]AgentState
	OpenCode             map[string]openCodeSession // by directory
	Checked              time.Time
	Err                  string
}

// openCodeSession is one session from the local OpenCode server, used for the busy indicator.
type openCodeSession struct {
	Dir                 string
	Busy, Idle, Updated int64
}

// LoggedIn reports the login state of a runtime (nil when not used).
func (s SandboxState) LoggedIn(runtime sandbox.Runtime) *bool {
	if runtime == sandbox.RuntimeOpenCode {
		return s.OpenCodeLoggedIn
	}
	return s.ClaudeLoggedIn
}

type Monitor struct {
	M *sandbox.Manager

	mu        sync.Mutex
	state     map[string]*SandboxState
	inflight  map[string]bool
	hold      map[string]int       // sandboxes with a running job: do not poll
	failures  map[string]int       // aufeinanderfolgende Abfragefehler
	norc      map[string]int       // checks without RC per agent
	attempts  map[string]int       // Aktivierungsversuche je Agent
	last      map[string]time.Time // letzter Versuch je Agent
	actions   map[string]string    // Anzeige-Stand je Agent
	installed map[string]bool      // MCP/Hook/Hinweise in dieser VM seit Daemon-Start aktualisiert
	sticky    map[string]stickyRC  // active RC seen once per agent, bound to the claude PID
	pids      map[string]int       // zuletzt gesehene claude-PID je Agent
}

// stickyRC: the banner "Remote Control active" is at the start of the output and scrolls out of view during longer work.
// So RC counts as active as long as the same claude process runs in which it was seen once.
type stickyRC struct {
	pid int
	url string
}

// applySticky evaluates the remembered state (caller holds mo.mu). Returns the agent state, supplemented if needed.
func (mo *Monitor) applySticky(key string, a AgentState) AgentState {
	if a.PID > 0 {
		// New claude process (restart, manual or automatic): reset counters and "failed".
		if old, ok := mo.pids[key]; ok && old != a.PID {
			delete(mo.norc, key)
			delete(mo.attempts, key)
			delete(mo.last, key)
			delete(mo.actions, key)
		}
		mo.pids[key] = a.PID
	}
	if a.RC && a.PID > 0 {
		mo.sticky[key] = stickyRC{pid: a.PID, url: a.URL}
		return a
	}
	if s, ok := mo.sticky[key]; ok {
		if a.Running && a.PID == s.pid {
			a.RC = true
			if a.URL == "" {
				a.URL = s.url
			}
			return a
		}
		delete(mo.sticky, key) // claude exited or was restarted
	}
	return a
}

// MarkRC confirms manually that Remote Control runs in this agent (escape hatch if detection does not see it).
func (mo *Monitor) MarkRC(name, agent string) error {
	key := name + "/" + agent
	mo.mu.Lock()
	defer mo.mu.Unlock()
	st := mo.state[name]
	if st == nil {
		return fmt.Errorf("sandbox has not been polled yet")
	}
	a, ok := st.Agents[agent]
	if !ok || !a.Running || a.PID == 0 {
		return fmt.Errorf("the agent is not running right now")
	}
	if a.Runtime == string(sandbox.RuntimeOpenCode) {
		return fmt.Errorf("Remote Control is only available for Claude agents")
	}
	mo.sticky[key] = stickyRC{pid: a.PID, url: a.URL}
	delete(mo.norc, key)
	delete(mo.attempts, key)
	delete(mo.last, key)
	delete(mo.actions, key)
	a.RC = true
	st.Agents[agent] = a
	return nil
}

func NewMonitor(m *sandbox.Manager) *Monitor {
	return &Monitor{M: m, state: map[string]*SandboxState{}, inflight: map[string]bool{}, hold: map[string]int{},
		failures: map[string]int{}, norc: map[string]int{}, attempts: map[string]int{}, last: map[string]time.Time{}, actions: map[string]string{}, installed: map[string]bool{}, sticky: map[string]stickyRC{}, pids: map[string]int{}}
}

// Hold/Release: during a job (start, stop, create …) the sandbox is not polled.
func (mo *Monitor) Hold(name string) { mo.mu.Lock(); mo.hold[name]++; mo.mu.Unlock() }
func (mo *Monitor) Release(name string) {
	mo.mu.Lock()
	mo.hold[name]--
	mo.Poke2(name)
	mo.mu.Unlock()
}
func (mo *Monitor) Poke2(name string) {
	if s, ok := mo.state[name]; ok {
		s.Checked = time.Time{}
	}
}

// ResetAgent resets an agent's automatic RC activation (e.g. after a manual restart).
func (mo *Monitor) ResetAgent(sandbox, agent string) {
	k := sandbox + "/" + agent
	mo.mu.Lock()
	delete(mo.norc, k)
	delete(mo.attempts, k)
	delete(mo.last, k)
	delete(mo.actions, k)
	mo.mu.Unlock()
}

func (mo *Monitor) Get(name string) (SandboxState, bool) {
	mo.mu.Lock()
	defer mo.mu.Unlock()
	s, ok := mo.state[name]
	if !ok {
		return SandboxState{}, false
	}
	cp := *s
	cp.Agents = map[string]AgentState{}
	for k, v := range s.Agents {
		if v.Runtime == string(sandbox.RuntimeClaude) && !v.RC {
			v.Action = mo.actions[name+"/"+k]
			// A running Claude agent that is neither idle nor active is probably waiting for input.
			if v.Action == "" && v.Running && !v.Idle && mo.norc[name+"/"+k] >= 8 {
				v.Action = "waiting"
			}
		}
		cp.Agents[k] = v
	}
	return cp, true
}

// Poke forces a prompt re-check.
func (mo *Monitor) Poke(name string) {
	mo.mu.Lock()
	if s, ok := mo.state[name]; ok {
		s.Checked = time.Time{}
	}
	mo.mu.Unlock()
}

func (s *SandboxState) stable() bool {
	needClaude, needOpenCode := false, false
	for _, a := range s.Agents {
		if a.Runtime == string(sandbox.RuntimeOpenCode) {
			needOpenCode = true
		} else {
			needClaude = true
		}
	}
	if needClaude && (s.ClaudeLoggedIn == nil || !*s.ClaudeLoggedIn) {
		return false
	}
	if needOpenCode && (s.OpenCodeLoggedIn == nil || !*s.OpenCodeLoggedIn) {
		return false
	}
	for _, a := range s.Agents {
		if a.Runtime == string(sandbox.RuntimeClaude) && !a.RC {
			return false
		}
	}
	return true
}

func parseProbe(out string) SandboxState {
	st := SandboxState{Agents: map[string]AgentState{}, OpenCode: map[string]openCodeSession{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "inst_claude="):
			st.ClaudeInstalled = strings.TrimPrefix(line, "inst_claude=") == "true"
		case strings.HasPrefix(line, "inst_opencode="):
			st.OpenCodeInstalled = strings.TrimPrefix(line, "inst_opencode=") == "true"
		case strings.HasPrefix(line, "login_claude="):
			v := strings.TrimPrefix(line, "login_claude=") == "true"
			st.ClaudeLoggedIn = &v
		case strings.HasPrefix(line, "login_opencode="):
			v := strings.TrimPrefix(line, "login_opencode=") == "true"
			st.OpenCodeLoggedIn = &v
		case strings.HasPrefix(line, "disk="):
			f := strings.Fields(strings.TrimPrefix(line, "disk="))
			if len(f) == 2 {
				st.GuestUsed, _ = strconv.ParseInt(f[0], 10, 64)
				st.GuestSize, _ = strconv.ParseInt(f[1], 10, 64)
			}
		case strings.HasPrefix(line, "oc "):
			oc := openCodeSession{}
			for _, kv := range strings.Fields(strings.TrimPrefix(line, "oc ")) {
				k, v, _ := strings.Cut(kv, "=")
				switch k {
				case "dir":
					oc.Dir = v
				case "busy":
					oc.Busy, _ = strconv.ParseInt(v, 10, 64)
				case "idle":
					oc.Idle, _ = strconv.ParseInt(v, 10, 64)
				case "updated":
					oc.Updated, _ = strconv.ParseInt(v, 10, 64)
				}
			}
			if oc.Dir != "" {
				if old, ok := st.OpenCode[oc.Dir]; !ok || oc.Updated > old.Updated {
					st.OpenCode[oc.Dir] = oc
				}
			}
		case strings.HasPrefix(line, "win="):
			a := AgentState{Window: true}
			name := ""
			for _, kv := range strings.Fields(line) {
				k, v, _ := strings.Cut(kv, "=")
				switch k {
				case "win":
					name = v
				case "rc":
					a.RC = v == "1"
				case "theme":
					a.Theme = v == "1"
				case "cpid":
					a.ClaudePID, _ = strconv.Atoi(v)
				case "opid":
					a.OpenCodePID, _ = strconv.Atoi(v)
				case "dlg":
					a.Dialog = v == "1"
				case "idle":
					a.Idle = v == "1"
				case "ask":
					a.Ask = v == "1"
				case "url":
					a.URL = v
				}
			}
			if name != "" {
				st.Agents[name] = a
			}
		}
	}
	return st
}

func (mo *Monitor) check(ctx context.Context, name, vm string) {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	registered, _ := mo.M.S.ListAgents(name)
	hasClaude, hasOpenCode := false, false
	runtimes := map[string]sandbox.Runtime{}
	for _, a := range registered {
		rt, _ := sandbox.ParseRuntime(a.Runtime)
		runtimes[a.Name] = rt
		if rt == sandbox.RuntimeOpenCode {
			hasOpenCode = true
		} else {
			hasClaude = true
		}
	}
	// Before the first agent exists we still need the Claude login state to decide about seeding "main".
	script := fmt.Sprintf(probeScript, boolInt(hasClaude || len(registered) == 0), boolInt(hasOpenCode))
	out, err := mo.M.L.Shell(cctx, vm, "bash", "-c", script)
	mo.mu.Lock()
	prev := mo.state[name]
	if err != nil {
		// While booting the poll necessarily fails: report only after several failures.
		mo.failures[name]++
		if prev == nil {
			prev = &SandboxState{Agents: map[string]AgentState{}, OpenCode: map[string]openCodeSession{}}
			mo.state[name] = prev
		}
		prev.Checked = time.Now()
		if mo.failures[name] >= 6 {
			prev.Err = err.Error()
		}
		mo.mu.Unlock()
		return
	}
	mo.failures[name] = 0
	needInstall := !mo.installed[name]
	if needInstall {
		mo.installed[name] = true
	}
	if needInstall {
		// Running VMs get the current version once per daemon start (MCP server, ssh hook, hint text).
		go func() {
			ictx, icancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer icancel()
			if err := mo.M.InstallHelpers(ictx, vm, name, hasOpenCode); err != nil {
				fmt.Fprintln(os.Stderr, "updating VM", name, ":", err)
				mo.mu.Lock()
				delete(mo.installed, name)
				mo.mu.Unlock()
				return
			}
			_ = mo.M.S.Audit("daemon", "vm.refresh", name, "", "MCP, ssh-Hook, Hinweistext")
		}()
	}
	st := parseProbe(string(out))
	st.Checked = time.Now()
	// Only registered agents count (no tmux default window "bash"/"shell"); missing windows count as "no window".
	for w := range st.Agents {
		if _, ok := runtimes[w]; !ok {
			delete(st.Agents, w)
		}
	}
	// The probe reports both processes; pick the one that belongs to the agent's runtime.
	for w, a := range st.Agents {
		rt := runtimes[w]
		a.Runtime = string(rt)
		if rt == sandbox.RuntimeOpenCode {
			a.Running, a.PID = a.OpenCodePID > 0, a.OpenCodePID
			// Remote Control and its screen markers are Claude-only.
			a.RC, a.Theme, a.Ask, a.Dialog, a.Idle, a.URL = false, false, false, false, false, ""
		} else {
			a.Running, a.PID = a.ClaudePID > 0, a.ClaudePID
			a = mo.applySticky(name+"/"+w, a)
		}
		st.Agents[w] = a
	}
	// OpenCode activity: the local server knows busy/idle per session directory.
	for dir, oc := range st.OpenCode {
		for w := range st.Agents {
			if !strings.HasSuffix(dir, "/work/"+w) {
				continue
			}
			a := st.Agents[w]
			if a.Runtime == string(sandbox.RuntimeOpenCode) && oc.Busy > oc.Idle {
				a.Action = "working"
				st.Agents[w] = a
			}
		}
	}
	// Older sandboxes without an agent get the first agent "main" once (Claude, as before).
	if !mo.M.S.Seeded(name) {
		if len(registered) == 0 && (st.ClaudeLoggedIn != nil || st.OpenCodeLoggedIn != nil) {
			if err := mo.M.AgentAdd(cctx, name, "main", sandbox.RuntimeClaude); err != nil {
				fmt.Fprintln(os.Stderr, "Erster Agent:", err)
			} else {
				_ = mo.M.S.Audit("daemon", "agent.auto_main", name, "main", "sandbox had no agent")
			}
		}
		_ = mo.M.S.SetSeeded(name)
	}
	var wasClaude, wasOpenCode *bool
	if prev != nil {
		wasClaude, wasOpenCode = prev.ClaudeLoggedIn, prev.OpenCodeLoggedIn
	}
	mo.state[name] = &st
	mo.mu.Unlock()

	// A login that has just completed: restart only the agents of that runtime.
	restarted := false
	if loginJustCompleted(st.ClaudeLoggedIn, wasClaude) {
		if n, err := mo.M.RestartRuntimeAgents(cctx, name, sandbox.RuntimeClaude); err != nil {
			fmt.Fprintln(os.Stderr, "restart after Claude login:", err)
		} else {
			_ = mo.M.S.Audit("daemon", "agent.restart_after_login", name, "", fmt.Sprintf("runtime=claude agents=%d", n))
			restarted = true
		}
	}
	if loginJustCompleted(st.OpenCodeLoggedIn, wasOpenCode) {
		mo.M.ReloadOpenCode(cctx, vm)
		if n, err := mo.M.RestartRuntimeAgents(cctx, name, sandbox.RuntimeOpenCode); err != nil {
			fmt.Fprintln(os.Stderr, "restart after OpenCode login:", err)
		} else {
			_ = mo.M.S.Audit("daemon", "agent.restart_after_login", name, "", fmt.Sprintf("runtime=opencode agents=%d", n))
			restarted = true
		}
	}
	if restarted {
		mo.Poke(name)
		return
	}
	for w, a := range st.Agents {
		if a.Runtime != string(sandbox.RuntimeClaude) {
			continue // Remote Control automation is Claude-only
		}
		key := name + "/" + w
		// Open RC dialog (e.g. from an earlier /remote-control): close with Escape – never Enter (the preselection could be "Disconnect").
		if a.Dialog {
			_ = mo.M.SendKey(cctx, vm, w, "Escape")
			continue
		}
		// Onboarding questions (theme, "Press Enter to continue"): confirm with Enter.
		if a.Theme {
			_ = mo.M.PressEnter(cctx, vm, w)
			continue
		}
		mo.mu.Lock()
		if a.RC {
			delete(mo.norc, key)
			delete(mo.attempts, key)
			delete(mo.actions, key)
			mo.mu.Unlock()
			continue
		}
		mo.norc[key]++
		ready := mo.norc[key] >= rcMissTicks
		attempts, since := mo.attempts[key], time.Since(mo.last[key])
		mo.mu.Unlock()
		if !ready {
			continue
		}
		act := decideRC(a, attempts, since)
		var err error
		switch act {
		case rcAnswerYes:
			err = mo.M.TypeLine(cctx, vm, w, "y")
		case rcSlash:
			err = mo.M.TypeLine(cctx, vm, w, "/remote-control")
		case rcRestart:
			err = mo.M.RestartAgent(cctx, name, w)
		case rcGiveUp:
			mo.mu.Lock()
			mo.actions[key] = "failed"
			mo.mu.Unlock()
		}
		if act == rcSlash || act == rcRestart || act == rcAnswerYes {
			mo.mu.Lock()
			if act != rcAnswerYes {
				mo.attempts[key]++
			}
			mo.last[key] = time.Now()
			mo.actions[key] = "activating"
			mo.mu.Unlock()
			detail := map[rcAction]string{rcSlash: "/remote-control", rcRestart: "restarted", rcAnswerYes: "prompt answered with y"}[act]
			if err != nil {
				detail += ": " + err.Error()
			}
			_ = mo.M.S.Audit("daemon", "agent.rc_enable", name, w, detail)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func loginJustCompleted(now, was *bool) bool {
	return now != nil && *now && was != nil && !*was
}

func (mo *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		mo.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (mo *Monitor) tick(ctx context.Context) {
	ss, err := mo.M.S.ListSandboxes()
	if err != nil {
		return
	}
	vms, err := mo.M.L.List(ctx)
	if err != nil {
		return
	}
	running := map[string]bool{}
	for _, v := range vms {
		running[v.Name] = v.Status == "Running"
	}
	for _, s := range ss {
		mo.mu.Lock()
		held := mo.hold[s.Name] > 0
		mo.mu.Unlock()
		if held {
			continue
		}
		if !running[s.VM] {
			mo.mu.Lock()
			delete(mo.state, s.Name)
			mo.mu.Unlock()
			continue
		}
		mo.mu.Lock()
		st := mo.state[s.Name]
		due := st == nil || time.Since(st.Checked) > map[bool]time.Duration{true: 20 * time.Second, false: 5 * time.Second}[st.stable()]
		busy := mo.inflight[s.Name]
		if due && !busy {
			mo.inflight[s.Name] = true
		}
		mo.mu.Unlock()
		if due && !busy {
			name, vm := s.Name, s.VM
			go func() {
				defer func() { mo.mu.Lock(); delete(mo.inflight, name); mo.mu.Unlock() }()
				mo.check(ctx, name, vm)
			}()
		}
	}
}

// hostDiskUsed: bytes of the VM files actually used on the Mac (blocks, not apparent size).
func hostDiskUsed(vm string) int64 {
	dir, err := lima.InstanceDir(vm)
	if err != nil {
		return 0
	}
	var total int64
	for _, f := range []string{"disk", "diffdisk", "basedisk"} {
		var st syscall.Stat_t
		if err := syscall.Stat(filepath.Join(dir, f), &st); err == nil {
			total += st.Blocks * 512
		}
	}
	return total
}
