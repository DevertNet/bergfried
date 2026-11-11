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
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// probeScript runs in the VM and reports logins, disk usage and, per tmux window, the Claude
// process and its Remote Control status. The OpenCode service is sandbox-wide: only its
// installed/running state is reported here.
const probeScript = `export PATH="$HOME/.local/bin:$HOME/.opencode/bin:$PATH"
echo "inst_claude=$([ -x "$HOME/.local/bin/claude" ] && echo true || echo false)"
echo "inst_opencode=$([ -x "$HOME/.opencode/bin/opencode" ] && "$HOME/.opencode/bin/opencode" --version >/dev/null 2>&1 && echo true || echo false)"
echo "oc_service=$(pgrep -x opencode >/dev/null 2>&1 && echo true || echo false)"
if [ %[1]d = 1 ]; then echo "login_claude=$(claude auth status 2>/dev/null | jq -r '.loggedIn // false' 2>/dev/null || echo false)"; fi
OC="$HOME/.opencode/bin/opencode"
if [ -x "$OC" ]; then
  echo "login_opencode=$("$OC" auth list --format json 2>/dev/null | jq -r '[.[]?.connections[]?] | length > 0' 2>/dev/null || echo false)"
else echo "login_opencode=false"; fi
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
    idle=0; echo "$p" | grep -q '❯' && ! echo "$p" | grep -q 'esc to interrupt' && [ "$dlg" = 0 ] && idle=1
    ask=0; echo "$p" | grep -q 'Enable Remote Control' && ask=1
    url=$(echo "$p" | grep -oE 'https://claude\.ai/code/session_[A-Za-z0-9_]+' | tail -1)
    echo "win=$w rc=$rc theme=$th idle=$idle ask=$ask dlg=$dlg cpid=$cpid url=$url"
  done
fi`

// WorktreeState is the Claude state of one worktree.
type WorktreeState struct {
	Folder  string
	Window  bool
	RC      bool
	Theme   bool
	Idle    bool
	Ask     bool
	Running bool // Claude runs in the window
	PID     int
	Dialog  bool // RC dialog ("Disconnect this session") is open
	URL     string
	Action  string // "", "activating", "failed", "waiting" – display state
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

// decideRC decides how a Claude without active Remote Control is brought back on track.
// Order: answer an open prompt, restart if Claude is not running (shell only),
// type /remote-control if the input field is empty; give up after rcMaxAttempts.
func decideRC(a WorktreeState, attempts int, sinceLast time.Duration) rcAction {
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
	ClaudeLoggedIn    *bool // nil = not checked
	OpenCodeLoggedIn  *bool // nil = not checked
	ClaudeInstalled   bool
	OpenCodeInstalled bool
	OpenCodeRunning   bool
	GuestUsed         int64
	GuestSize         int64
	Worktrees         map[string]WorktreeState
	Checked           time.Time
	Err               string
}

type Monitor struct {
	M *sandbox.Manager

	mu        sync.Mutex
	state     map[string]*SandboxState
	inflight  map[string]bool
	hold      map[string]int       // sandboxes with a running job: do not poll
	failures  map[string]int       // consecutive probe errors
	norc      map[string]int       // checks without RC per worktree
	attempts  map[string]int       // activation attempts per worktree
	last      map[string]time.Time // last attempt per worktree
	actions   map[string]string    // display state per worktree
	installed map[string]bool      // helpers in this VM refreshed since daemon start
	sticky    map[string]stickyRC  // active RC seen once per worktree, bound to the Claude PID
	pids      map[string]int       // last seen Claude PID per worktree
}

// stickyRC: the banner "Remote Control active" is at the start of the output and scrolls out of view during longer work.
// So RC counts as active as long as the same Claude process runs in which it was seen once.
type stickyRC struct {
	pid int
	url string
}

// applySticky evaluates the remembered state (caller holds mo.mu). Returns the worktree state,
// supplemented if needed.
func (mo *Monitor) applySticky(key string, a WorktreeState) WorktreeState {
	if a.PID > 0 {
		// New Claude process (restart, manual or automatic): reset counters and "failed".
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
		delete(mo.sticky, key) // Claude exited or was restarted
	}
	return a
}

// MarkRC confirms manually that Remote Control runs in this worktree (escape hatch if detection does not see it).
func (mo *Monitor) MarkRC(name, worktree string) error {
	key := name + "/" + worktree
	mo.mu.Lock()
	defer mo.mu.Unlock()
	st := mo.state[name]
	if st == nil {
		return fmt.Errorf("sandbox has not been polled yet")
	}
	a, ok := st.Worktrees[worktree]
	if !ok || !a.Running || a.PID == 0 {
		return fmt.Errorf("Claude is not running in this worktree right now")
	}
	mo.sticky[key] = stickyRC{pid: a.PID, url: a.URL}
	delete(mo.norc, key)
	delete(mo.attempts, key)
	delete(mo.last, key)
	delete(mo.actions, key)
	a.RC = true
	st.Worktrees[worktree] = a
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

// ResetClaude resets a Claude folder's automatic RC activation (e.g. after a manual restart).
func (mo *Monitor) ResetClaude(sandbox, folder string) {
	mo.mu.Lock()
	st := mo.state[sandbox]
	if st == nil {
		mo.mu.Unlock()
		return
	}
	for k, a := range st.Worktrees {
		if a.Folder == folder {
			key := sandbox + "/" + k
			delete(mo.norc, key)
			delete(mo.attempts, key)
			delete(mo.last, key)
			delete(mo.actions, key)
		}
	}
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
	cp.Worktrees = map[string]WorktreeState{}
	for k, v := range s.Worktrees {
		if !v.RC {
			v.Action = mo.actions[name+"/"+k]
			// A running Claude that is neither idle nor active is probably waiting for input.
			if v.Action == "" && v.Running && !v.Idle && mo.norc[name+"/"+k] >= 8 {
				v.Action = "waiting"
			}
		}
		cp.Worktrees[k] = v
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
	if s.ClaudeLoggedIn == nil || !*s.ClaudeLoggedIn {
		return false
	}
	for _, a := range s.Worktrees {
		if !a.RC {
			return false
		}
	}
	return true
}

func parseProbe(out string) SandboxState {
	st := SandboxState{Worktrees: map[string]WorktreeState{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "inst_claude="):
			st.ClaudeInstalled = strings.TrimPrefix(line, "inst_claude=") == "true"
		case strings.HasPrefix(line, "inst_opencode="):
			st.OpenCodeInstalled = strings.TrimPrefix(line, "inst_opencode=") == "true"
		case strings.HasPrefix(line, "oc_service="):
			st.OpenCodeRunning = strings.TrimPrefix(line, "oc_service=") == "true"
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
		case strings.HasPrefix(line, "win="):
			a := WorktreeState{Window: true}
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
					a.PID, _ = strconv.Atoi(v)
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
				st.Worktrees[name] = a
			}
		}
	}
	return st
}

func (mo *Monitor) check(ctx context.Context, name, vm string) {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	registered, _ := mo.M.S.ListWorktrees(name)
	byName := map[string]store.Worktree{}
	for _, w := range registered {
		byName[w.Name] = w
	}
	script := fmt.Sprintf(probeScript, 1)
	out, err := mo.M.L.Shell(cctx, vm, "bash", "-c", script)
	mo.mu.Lock()
	prev := mo.state[name]
	if err != nil {
		// While booting the poll necessarily fails: report only after several failures.
		mo.failures[name]++
		if prev == nil {
			prev = &SandboxState{Worktrees: map[string]WorktreeState{}}
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
	st := parseProbe(string(out))
	st.Checked = time.Now()
	// Only registered worktrees count (no tmux default window "bash"/"shell").
	for w := range st.Worktrees {
		if _, ok := byName[w]; !ok {
			delete(st.Worktrees, w)
		}
	}
	for w, a := range st.Worktrees {
		a.Folder = sandbox.FolderOf(byName[w].Path)
		a.Running = a.PID > 0
		a = mo.applySticky(name+"/"+w, a)
		st.Worktrees[w] = a
	}
	needInstall := !mo.installed[name]
	if needInstall {
		mo.installed[name] = true
	}
	var wasClaude, wasOpenCode *bool
	if prev != nil {
		wasClaude, wasOpenCode = prev.ClaudeLoggedIn, prev.OpenCodeLoggedIn
	}
	mo.state[name] = &st
	mo.mu.Unlock()

	// Worktrees where Claude is not wanted must not have a window: this also terminates the
	// old OpenCode TUI windows of migrated sandboxes.
	for w, a := range st.Worktrees {
		if a.Window && !byName[w].Claude {
			if err := mo.M.ClaudeStop(cctx, name, sandbox.FolderOf(byName[w].Path)); err != nil {
				fmt.Fprintln(os.Stderr, "stop stale window", w, ":", err)
			}
		}
	}

	if needInstall {
		// Running VMs get the current version once per daemon start (MCP server, ssh hook, hint text).
		go func() {
			ictx, icancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer icancel()
			if err := mo.M.InstallHelpers(ictx, vm, name, st.OpenCodeInstalled); err != nil {
				fmt.Fprintln(os.Stderr, "updating VM", name, ":", err)
				mo.mu.Lock()
				delete(mo.installed, name)
				mo.mu.Unlock()
				return
			}
			_ = mo.M.S.Audit("daemon", "vm.refresh", name, "", "MCP, ssh hook, hint text")
		}()
	}

	// Older sandboxes without a worktree entry get "main" once (Claude is not started automatically).
	if !mo.M.S.Seeded(name) {
		if len(registered) == 0 && (st.ClaudeLoggedIn != nil || st.OpenCodeLoggedIn != nil) {
			if err := mo.M.S.AddWorktree(store.Worktree{Sandbox: name, Name: "main", Path: "~/work/main", ComposeProject: name + "-main", Created: time.Now()}); err != nil {
				fmt.Fprintln(os.Stderr, "first worktree:", err)
			} else {
				_ = mo.M.S.Audit("daemon", "claude.auto_main", name, "main", "sandbox had no worktree")
			}
		}
		_ = mo.M.S.SetSeeded(name)
	}

	// A Claude login that has just completed: restart the Claude worktrees so they pick it up.
	if loginJustCompleted(st.ClaudeLoggedIn, wasClaude) {
		n := 0
		for _, w := range registered {
			if !w.Claude {
				continue
			}
			if _, running := st.Worktrees[w.Name]; !running {
				continue
			}
			if err := mo.M.ClaudeRestart(cctx, name, sandbox.FolderOf(w.Path)); err != nil {
				fmt.Fprintln(os.Stderr, "restart after Claude login:", err)
			} else {
				n++
			}
		}
		_ = mo.M.S.Audit("daemon", "claude.restart_after_login", name, "", fmt.Sprintf("runtime=claude worktrees=%d", n))
		mo.Poke(name)
		return
	}
	// A new OpenCode login does not need any worktree restart: the service reloads its credentials.
	if loginJustCompleted(st.OpenCodeLoggedIn, wasOpenCode) {
		mo.M.ReloadOpenCode(cctx, vm)
		_ = mo.M.S.Audit("daemon", "opencode.reload_after_login", name, "", "")
		mo.Poke(name)
		return
	}

	for w, a := range st.Worktrees {
		if !byName[w].Claude {
			continue // Claude is not enabled for this worktree
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
			err = mo.M.ClaudeRestart(cctx, name, sandbox.FolderOf(byName[w].Path))
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
			_ = mo.M.S.Audit("daemon", "claude.rc_enable", name, w, detail)
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
