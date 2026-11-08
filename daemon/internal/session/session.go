// Package session steuert den Lebenszyklus: Basis-VM, Klon je Session, Agents in tmux.
package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const (
	TemplateVersion = 2
	BaseName        = "bergfried-base-v2"
	tmuxSession     = "bf"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// ErrUnpushed wird von Delete geliefert, wenn ungesicherte Arbeit in der VM liegt.
type ErrUnpushed struct{ Report string }

func (e ErrUnpushed) Error() string {
	return "ungepushte Änderungen in der VM:\n" + e.Report + "\n(mit --force trotzdem löschen)"
}

type Manager struct {
	L        lima.Client
	S        *store.Store
	Home     string // Datenverzeichnis, z. B. ~/.bergfried
	Template string // Pfad zu vm/bergfried.yaml
	Actor    string
}

func ValidName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("ungültiger Name %q (erlaubt: a-z, 0-9, -; max. 31 Zeichen, mit Buchstabe beginnen)", n)
	}
	return nil
}

func VMName(session string) string { return "bf-" + session }

func (m *Manager) audit(event, session, agent, detail string) {
	if err := m.S.Audit(m.Actor, event, session, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
	}
}

func (m *Manager) exists(ctx context.Context, vm string) (bool, error) {
	l, err := m.L.List(ctx)
	if err != nil {
		return false, err
	}
	for _, i := range l {
		if i.Name == vm {
			return true, nil
		}
	}
	return false, nil
}

// BuildBase baut die Basis-VM (ohne Login!) aus der Vorlage und stoppt sie.
func (m *Manager) BuildBase(ctx context.Context, rebuild bool) error {
	ok, err := m.exists(ctx, BaseName)
	if err != nil {
		return err
	}
	if ok {
		if !rebuild {
			return nil
		}
		if err := m.L.Delete(ctx, BaseName); err != nil {
			return err
		}
	}
	if _, err := m.L.R.Run(ctx, "create", "--name="+BaseName, "--tty=false", m.Template); err != nil {
		return err
	}
	if err := m.L.Start(ctx, BaseName); err != nil {
		return err
	}
	if err := m.L.Stop(ctx, BaseName); err != nil {
		return err
	}
	m.audit("base.build", "", "", BaseName)
	return nil
}

const firstBoot = `set -e
sudo rm -f /etc/machine-id /var/lib/dbus/machine-id
sudo systemd-machine-id-setup
sudo rm -f /etc/ssh/ssh_host_*
sudo dpkg-reconfigure -f noninteractive openssh-server
sudo systemctl restart ssh
mkdir -p ~/work`

// McpBin: Pfad zum bergfried-mcp-Binary (Linux arm64), Standard neben bergfriedd.
func McpBin() string {
	if v := os.Getenv("BERGFRIED_MCP_BIN"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return "bin/bergfried-mcp"
	}
	return filepath.Join(filepath.Dir(exe), "bergfried-mcp")
}

// InstallMCP kopiert bergfried-mcp in die VM und trägt es in Claude Code ein (idempotent).
func (m *Manager) InstallMCP(ctx context.Context, vm string) error {
	bin := McpBin()
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("bergfried-mcp nicht gefunden (%s) – `make build` ausführen: %w", bin, err)
	}
	if _, err := m.L.R.Run(ctx, "cp", bin, vm+":/tmp/bergfried-mcp"); err != nil {
		return err
	}
	_, err := m.L.Shell(ctx, vm, "bash", "-lc", `set -e
mkdir -p ~/.local/bin && install -m755 /tmp/bergfried-mcp ~/.local/bin/bergfried-mcp && rm /tmp/bergfried-mcp
export PATH="$HOME/.local/bin:$PATH"
claude mcp remove bergfried --scope user >/dev/null 2>&1 || true
claude mcp add --transport stdio --scope user bergfried -- "$HOME/.local/bin/bergfried-mcp"`)
	return err
}

func (m *Manager) Create(ctx context.Context, name, repo string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	vm := VMName(name)
	if ok, err := m.exists(ctx, vm); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("VM %s existiert bereits", vm)
	}
	if err := m.BuildBase(ctx, false); err != nil {
		return err
	}
	dir := filepath.Join(m.Home, "sessions", name)
	in, out := filepath.Join(dir, "shared", "in"), filepath.Join(dir, "shared", "out")
	for _, d := range []string{in, out} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if _, err := m.L.R.Run(ctx, "clone", BaseName, vm, "--tty=false",
		"--mount-only", in, "--mount-only", out+":w"); err != nil {
		return err
	}
	if err := m.L.Start(ctx, vm); err != nil {
		return err
	}
	if _, err := m.L.Shell(ctx, vm, "bash", "-c", firstBoot); err != nil {
		return fmt.Errorf("First-Boot-Schritt: %w", err)
	}
	// Host-Keys wurden neu erzeugt: Lima muss den Key beim nächsten Start neu pinnen.
	if err := lima.RepinHostKey(vm); err != nil {
		return err
	}
	if err := m.InstallMCP(ctx, vm); err != nil {
		return err
	}
	if repo != "" {
		if _, err := m.L.Shell(ctx, vm, "git", "clone", repo, "work/main"); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	}
	if err := m.S.AddSession(store.Session{Name: name, VM: vm, Repo: repo, TemplateVersion: TemplateVersion, Created: time.Now()}); err != nil {
		return err
	}
	m.audit("session.create", name, "", repo)
	return nil
}

func (m *Manager) Start(ctx context.Context, name string) error {
	s, err := m.S.GetSession(name)
	if err != nil {
		return err
	}
	if err := m.L.Start(ctx, s.VM); err != nil {
		return err
	}
	if err := m.InstallMCP(ctx, s.VM); err != nil {
		return err
	}
	// tmux überlebt keinen VM-Stopp: Agents mit --continue wieder hochfahren.
	ags, err := m.S.ListAgents(name)
	if err != nil {
		return err
	}
	for _, a := range ags {
		if err := m.ensureWindow(ctx, s.VM, name, a.Name); err != nil {
			return fmt.Errorf("Agent %s: %w", a.Name, err)
		}
	}
	m.audit("session.start", name, "", fmt.Sprintf("agents=%d", len(ags)))
	return nil
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	s, err := m.S.GetSession(name)
	if err != nil {
		return err
	}
	if err := m.L.Stop(ctx, s.VM); err != nil {
		return err
	}
	m.audit("session.stop", name, "", "")
	return nil
}

const unpushedCheck = `for d in ~/work/*/; do [ -d "$d/.git" ] || [ -f "$d/.git" ] || continue
  s=$(git -C "$d" status --porcelain); u=$(git -C "$d" log --branches --not --remotes --oneline)
  [ -z "$s$u" ] || { echo "== $d"; echo "$s"; echo "$u"; }
done`

func (m *Manager) Delete(ctx context.Context, name string, force bool) error {
	s, err := m.S.GetSession(name)
	if err != nil {
		return err
	}
	if !force {
		if err := m.L.Start(ctx, s.VM); err != nil {
			return err
		}
		out, err := m.L.Shell(ctx, s.VM, "bash", "-c", unpushedCheck)
		if err != nil {
			return fmt.Errorf("Prüfung auf ungepushte Commits fehlgeschlagen (nichts gelöscht): %w", err)
		}
		if r := strings.TrimSpace(string(out)); r != "" {
			return ErrUnpushed{Report: r}
		}
	}
	if err := m.L.Delete(ctx, s.VM); err != nil {
		return err
	}
	if err := m.S.DeleteSession(name); err != nil {
		return err
	}
	m.audit("session.delete", name, "", fmt.Sprintf("force=%v", force))
	return nil
}

// AgentCmd baut die Shell-Zeile für ein tmux-Fenster. Namen sind validiert (nur [a-z0-9-]).
// --continue holt den Verlauf; gibt es keinen (neuer Agent), startet der Fallback frisch.
func AgentCmd(session, agent string) string {
	rc := fmt.Sprintf("--remote-control '%s/%s'", session, agent)
	return fmt.Sprintf(`cd ~/work/%[2]s && export COMPOSE_PROJECT_NAME=%[1]s-%[2]s BERGFRIED_AGENT=%[1]s/%[2]s && (~/.local/bin/claude --continue %[3]s || exec ~/.local/bin/claude %[3]s)`, session, agent, rc)
}

// ensureWindow legt tmux-Session/-Fenster an, falls nicht vorhanden, und startet den Agent darin.
func (m *Manager) ensureWindow(ctx context.Context, vm, session, agent string) error {
	script := fmt.Sprintf(`set -e
f=~/.claude.json; [ -f "$f" ] || echo '{}' > "$f"
jq --arg d "$HOME/work/%[1]s" '.projects[$d].hasTrustDialogAccepted=true' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
if ! tmux has-session -t %[2]s 2>/dev/null; then tmux new-session -d -s %[2]s -n %[1]s
elif ! tmux list-windows -t %[2]s -F '#W' | grep -qx %[1]s; then tmux new-window -t %[2]s -n %[1]s
else exit 0; fi
tmux send-keys -t %[2]s:%[1]s %[3]q Enter`, agent, tmuxSession, AgentCmd(session, agent))
	_, err := m.L.Shell(ctx, vm, "bash", "-c", script)
	return err
}

func (m *Manager) AgentAdd(ctx context.Context, session, agent string) error {
	if err := ValidName(agent); err != nil {
		return err
	}
	s, err := m.S.GetSession(session)
	if err != nil {
		return err
	}
	setup := fmt.Sprintf(`set -e
if [ -d ~/work/main/.git ] && [ %[1]q != main ]; then git -C ~/work/main worktree add ~/work/%[1]s -b agent/%[1]s; else mkdir -p ~/work/%[1]s; fi
[ -e ~/work/%[1]s/NOTES.md ] || printf '# Notizen %[1]s\n\nArbeitsstand:\n' > ~/work/%[1]s/NOTES.md`, agent)
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-c", setup); err != nil {
		return err
	}
	if err := m.ensureWindow(ctx, s.VM, session, agent); err != nil {
		return err
	}
	if err := m.S.AddAgent(store.Agent{Session: session, Name: agent, Worktree: "~/work/" + agent, ComposeProject: session + "-" + agent, Created: time.Now()}); err != nil {
		return err
	}
	m.audit("agent.add", session, agent, "")
	return nil
}
