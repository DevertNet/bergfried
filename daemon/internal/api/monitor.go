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
	"github.com/DevertNet/bergfried/daemon/internal/session"
)

// probeScript läuft in der VM und meldet Login, Plattenbelegung sowie je Agent-Fenster den RC-Status.
const probeScript = `export PATH="$HOME/.local/bin:$PATH"
echo "login=$(claude auth status 2>/dev/null | jq -r '.loggedIn // false' 2>/dev/null || echo false)"
df -B1 --output=used,size / | tail -1 | awk '{print "disk="$1" "$2}'
if tmux has-session -t bf 2>/dev/null; then
  for w in $(tmux list-windows -t bf -F '#W'); do
    p=$(tmux capture-pane -p -J -t "bf:$w" -S -60 2>/dev/null)
    rc=0; echo "$p" | grep -qE 'remote-control is active|/rc active' && rc=1
    th=0; echo "$p" | grep -q 'Choose the text style' && th=1
    url=$(echo "$p" | grep -oE 'https://claude\.ai/code/session_[A-Za-z0-9_]+' | tail -1)
    echo "win=$w rc=$rc theme=$th url=$url"
  done
fi`

type AgentState struct {
	Window, RC, Theme bool
	URL               string
}

type SessionState struct {
	LoggedIn             *bool
	GuestUsed, GuestSize int64
	Agents               map[string]AgentState
	Checked              time.Time
	Err                  string
}

type Monitor struct {
	M *session.Manager

	mu       sync.Mutex
	state    map[string]*SessionState
	inflight map[string]bool
}

func NewMonitor(m *session.Manager) *Monitor {
	return &Monitor{M: m, state: map[string]*SessionState{}, inflight: map[string]bool{}}
}

func (mo *Monitor) Get(name string) (SessionState, bool) {
	mo.mu.Lock()
	defer mo.mu.Unlock()
	s, ok := mo.state[name]
	if !ok {
		return SessionState{}, false
	}
	cp := *s
	cp.Agents = map[string]AgentState{}
	for k, v := range s.Agents {
		cp.Agents[k] = v
	}
	return cp, true
}

// Poke erzwingt eine baldige Neuprüfung.
func (mo *Monitor) Poke(name string) {
	mo.mu.Lock()
	if s, ok := mo.state[name]; ok {
		s.Checked = time.Time{}
	}
	mo.mu.Unlock()
}

func (s *SessionState) stable() bool {
	if s.LoggedIn == nil || !*s.LoggedIn {
		return false
	}
	for _, a := range s.Agents {
		if !a.RC {
			return false
		}
	}
	return true
}

func parseProbe(out string) SessionState {
	st := SessionState{Agents: map[string]AgentState{}}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "login="):
			v := strings.TrimPrefix(line, "login=") == "true"
			st.LoggedIn = &v
		case strings.HasPrefix(line, "disk="):
			f := strings.Fields(strings.TrimPrefix(line, "disk="))
			if len(f) == 2 {
				st.GuestUsed, _ = strconv.ParseInt(f[0], 10, 64)
				st.GuestSize, _ = strconv.ParseInt(f[1], 10, 64)
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
	out, err := mo.M.L.Shell(cctx, vm, "bash", "-c", probeScript)
	mo.mu.Lock()
	prev := mo.state[name]
	if err != nil {
		if prev == nil {
			prev = &SessionState{Agents: map[string]AgentState{}}
			mo.state[name] = prev
		}
		prev.Err, prev.Checked = err.Error(), time.Now()
		mo.mu.Unlock()
		return
	}
	st := parseProbe(string(out))
	st.Checked = time.Now()
	var wasLoggedIn *bool
	if prev != nil {
		wasLoggedIn = prev.LoggedIn
	}
	mo.state[name] = &st
	mo.mu.Unlock()

	if st.LoggedIn == nil || !*st.LoggedIn {
		return
	}
	// Login gerade erst erkannt: Agents neu starten, damit sie mit Login und Remote Control laufen.
	if wasLoggedIn != nil && !*wasLoggedIn {
		ags, _ := mo.M.S.ListAgents(name)
		for _, a := range ags {
			if err := mo.M.RestartAgent(cctx, name, a.Name); err != nil {
				fmt.Fprintln(os.Stderr, "Neustart nach Login:", err)
			}
		}
		_ = mo.M.S.Audit("daemon", "agent.restart_after_login", name, "", fmt.Sprintf("agents=%d", len(ags)))
		mo.Poke(name)
		return
	}
	// Erststart-Frage (Theme) mit der Vorauswahl bestätigen.
	for w, a := range st.Agents {
		if a.Theme {
			_ = mo.M.PressEnter(cctx, vm, w)
		}
	}
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
	ss, err := mo.M.S.ListSessions()
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

// hostDiskUsed: tatsächlich belegte Bytes der VM-Dateien auf dem Mac (Blöcke, nicht scheinbare Größe).
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
