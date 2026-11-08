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
	TemplateVersion = 1
	BaseName        = "bergfried-base-v1"
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
	m.audit("session.start", name, "", "")
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
func AgentCmd(session, agent string) string {
	return fmt.Sprintf(`cd ~/work/%[2]s && COMPOSE_PROJECT_NAME=%[1]s-%[2]s exec ~/.local/bin/claude --remote-control '%[1]s/%[2]s'`, session, agent)
}

func (m *Manager) AgentAdd(ctx context.Context, session, agent string) error {
	if err := ValidName(agent); err != nil {
		return err
	}
	s, err := m.S.GetSession(session)
	if err != nil {
		return err
	}
	wt := "~/work/" + agent
	setup := fmt.Sprintf(`set -e
if [ -d ~/work/main/.git ] && [ %[1]q != main ]; then git -C ~/work/main worktree add ~/work/%[1]s -b agent/%[1]s; else mkdir -p ~/work/%[1]s; fi
[ -e ~/work/%[1]s/NOTES.md ] || printf '# Notizen %[1]s\n\nArbeitsstand:\n' > ~/work/%[1]s/NOTES.md
tmux has-session -t %[2]s 2>/dev/null || { tmux new-session -d -s %[2]s -n %[1]s; first=1; }
[ -n "$first" ] || tmux new-window -t %[2]s -n %[1]s
tmux send-keys -t %[2]s:%[1]s %[3]q Enter`, agent, tmuxSession, AgentCmd(session, agent))
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-c", setup); err != nil {
		return err
	}
	if err := m.S.AddAgent(store.Agent{Session: session, Name: agent, Worktree: wt, ComposeProject: session + "-" + agent, Created: time.Now()}); err != nil {
		return err
	}
	m.audit("agent.add", session, agent, "")
	return nil
}
