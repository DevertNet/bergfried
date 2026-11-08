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
    th=0; echo "$p" | grep -qE 'Choose the text style|Press Enter to continue' && th=1
    tty=$(tmux display-message -p -t "bf:$w" '#{pane_tty}' 2>/dev/null)
    run=0; ps -t "${tty#/dev/}" -o comm= 2>/dev/null | grep -qx claude && run=1
    idle=0; echo "$p" | grep -q '❯' && ! echo "$p" | grep -q 'esc to interrupt' && idle=1
    ask=0; echo "$p" | grep -q 'Enable Remote Control' && ask=1
    url=$(echo "$p" | grep -oE 'https://claude\.ai/code/session_[A-Za-z0-9_]+' | tail -1)
    echo "win=$w rc=$rc theme=$th run=$run idle=$idle ask=$ask url=$url"
  done
fi`

type AgentState struct {
	Window, RC, Theme bool
	Idle, Ask         bool
	Running           bool // Prozess "claude" läuft im Fenster
	URL               string
	Action            string // "", "activating", "failed", "waiting" – Stand der automatischen RC-Aktivierung
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
	rcMissTicks   = 4 // so viele Prüfungen in Folge ohne RC, bevor aktiviert wird (Schonfrist fürs Hochfahren)
	rcRetryAfter  = 20 * time.Second
)

// decideRC entscheidet, wie ein Agent ohne aktive Remote Control wieder in Gang gebracht wird.
// Reihenfolge: offene Rückfrage beantworten, Claude läuft nicht (nur Shell) neu starten,
// bei leerem Eingabefeld /remote-control tippen; nach rcMaxAttempts aufgeben.
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

type SessionState struct {
	LoggedIn             *bool
	GuestUsed, GuestSize int64
	Agents               map[string]AgentState
	Checked              time.Time
	Err                  string
}

type Monitor struct {
	M *session.Manager

	mu        sync.Mutex
	state     map[string]*SessionState
	inflight  map[string]bool
	hold      map[string]int       // Sessions mit laufendem Job: nicht abfragen
	failures  map[string]int       // aufeinanderfolgende Abfragefehler
	norc      map[string]int       // Prüfungen ohne RC je Agent
	attempts  map[string]int       // Aktivierungsversuche je Agent
	last      map[string]time.Time // letzter Versuch je Agent
	actions   map[string]string    // Anzeige-Stand je Agent
	installed map[string]bool      // MCP/Hook/Hinweise in dieser VM seit Daemon-Start aktualisiert
}

func NewMonitor(m *session.Manager) *Monitor {
	return &Monitor{M: m, state: map[string]*SessionState{}, inflight: map[string]bool{}, hold: map[string]int{},
		failures: map[string]int{}, norc: map[string]int{}, attempts: map[string]int{}, last: map[string]time.Time{}, actions: map[string]string{}, installed: map[string]bool{}}
}

// Hold/Release: während eines Jobs (Start, Stopp, Create …) wird die Session nicht abgefragt.
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

// ResetAgent setzt die automatische RC-Aktivierung eines Agents zurück (z. B. nach manuellem Neustart).
func (mo *Monitor) ResetAgent(session, agent string) {
	k := session + "/" + agent
	mo.mu.Lock()
	delete(mo.norc, k)
	delete(mo.attempts, k)
	delete(mo.last, k)
	delete(mo.actions, k)
	mo.mu.Unlock()
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
		if !v.RC {
			v.Action = mo.actions[name+"/"+k]
			// Läuft, ist aber weder im Leerlauf noch aktiv: vermutlich wartet Claude auf eine Eingabe.
			if v.Action == "" && v.Running && !v.Idle && mo.norc[name+"/"+k] >= 8 {
				v.Action = "waiting"
			}
		}
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
				case "run":
					a.Running = v == "1"
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
	out, err := mo.M.L.Shell(cctx, vm, "bash", "-c", probeScript)
	mo.mu.Lock()
	prev := mo.state[name]
	if err != nil {
		// Beim Hochfahren schlägt die Abfrage zwangsläufig fehl: erst nach mehreren Fehlern melden.
		mo.failures[name]++
		if prev == nil {
			prev = &SessionState{Agents: map[string]AgentState{}}
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
		// Laufende VMs bekommen einmal je Daemon-Start die aktuelle Fassung (MCP-Server, ssh-Hook, Hinweistext).
		go func() {
			ictx, icancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer icancel()
			if err := mo.M.InstallMCP(ictx, vm, name); err != nil {
				fmt.Fprintln(os.Stderr, "Aktualisierung der VM", name, ":", err)
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
	// Nur registrierte Agents zählen (kein tmux-Standardfenster "bash"/"shell"); fehlende Fenster gelten als "kein Fenster".
	registered, _ := mo.M.S.ListAgents(name)
	regSet := map[string]bool{}
	for _, a := range registered {
		regSet[a.Name] = true
	}
	for w := range st.Agents {
		if !regSet[w] {
			delete(st.Agents, w)
		}
	}
	// Ältere Sessions ohne Agent bekommen einmalig den ersten Agent "main".
	if !mo.M.S.Seeded(name) {
		if len(registered) == 0 && st.LoggedIn != nil {
			if err := mo.M.AgentAdd(cctx, name, "main"); err != nil {
				fmt.Fprintln(os.Stderr, "Erster Agent:", err)
			} else {
				_ = mo.M.S.Audit("daemon", "agent.auto_main", name, "main", "Session hatte keinen Agent")
			}
		}
		_ = mo.M.S.SetSeeded(name)
	}
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
	for w, a := range st.Agents {
		key := name + "/" + w
		// Onboarding-Fragen (Theme, "Press Enter to continue") mit Enter bestätigen.
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
			detail := map[rcAction]string{rcSlash: "/remote-control", rcRestart: "neu gestartet", rcAnswerYes: "Rückfrage mit y beantwortet"}[act]
			if err != nil {
				detail += ": " + err.Error()
			}
			_ = mo.M.S.Audit("daemon", "agent.rc_enable", name, w, detail)
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
