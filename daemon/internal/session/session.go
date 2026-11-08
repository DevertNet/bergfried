// Package session steuert den Lebenszyklus: Basis-VM, Klon je Session, Agents in tmux.
package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const (
	TemplateVersion = 3 // Start-Revision der mitgelieferten Vorlage
	tmuxSession     = "bf"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

type progressKey struct{}

// WithProgress hängt einen Fortschritts-Callback an den Kontext (für Job-Anzeige in der App).
func WithProgress(ctx context.Context, f func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, f)
}

func progress(ctx context.Context, msg string) {
	if f, ok := ctx.Value(progressKey{}).(func(string)); ok {
		f(msg)
	}
}

// ErrUnpushed wird von Delete geliefert, wenn ungesicherte Arbeit in der VM liegt.
type ErrUnpushed struct{ Report string }

func (e ErrUnpushed) Error() string {
	return "ungepushte Änderungen in der VM:\n" + e.Report + "\n(mit --force trotzdem löschen)"
}

// Importer kopiert einen Mac-Ordner als tar-Strom (siehe package importer).
type Importer interface {
	Preview(path string) (files, bytes int64, err error)
	Stream(path string, w io.Writer) (files, bytes int64, err error)
}

type Manager struct {
	Imp      Importer
	L        lima.Client
	S        *store.Store
	Home     string // Datenverzeichnis, z. B. ~/.bergfried
	Template string // Pfad zur Vorlage (bei Tpl != nil: Tpl.Path())
	Tpl      *TemplateStore
	Actor    string
}

// Rev: Vorlagen-Revision (Zähler der Änderungen, nur zur Anzeige).
func (m *Manager) Rev() int {
	if m.Tpl != nil {
		return m.Tpl.Rev()
	}
	return TemplateVersion
}

func (m *Manager) BaseName() string {
	if m.Tpl != nil {
		return "bergfried-base-" + m.Tpl.Hash()
	}
	return fmt.Sprintf("bergfried-base-v%d", TemplateVersion)
}

func (m *Manager) templatePath() string {
	if m.Tpl != nil {
		return m.Tpl.Path()
	}
	return m.Template
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
	base := m.BaseName()
	ok, err := m.exists(ctx, base)
	if err != nil {
		return err
	}
	if ok {
		if !rebuild {
			return nil
		}
		if err := m.L.Delete(ctx, base); err != nil {
			return err
		}
	}
	progress(ctx, "Basis-VM wird erstellt (Image laden, Docker und Claude Code installieren – mehrere Minuten)")
	if _, err := m.L.R.Run(ctx, "create", "--name="+base, "--tty=false", m.templatePath()); err != nil {
		return err
	}
	progress(ctx, "Basis-VM startet und wird konfiguriert (mehrere Minuten)")
	if err := m.L.Start(ctx, base); err != nil {
		return err
	}
	progress(ctx, "Basis-VM wird gestoppt")
	if err := m.L.Stop(ctx, base); err != nil {
		return err
	}
	m.audit("base.build", "", "", base)
	// Ältere Basis-VMs aufräumen (Sessions sind unabhängige Klone).
	if vms, err := m.L.List(ctx); err == nil {
		for _, v := range vms {
			if strings.HasPrefix(v.Name, "bergfried-base-") && v.Name != base {
				if err := m.L.Delete(ctx, v.Name); err == nil {
					m.audit("base.remove_old", "", "", v.Name)
				}
			}
		}
	}
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

// importFolder streamt den Ordner in ~/work/main der VM.
func (m *Manager) importFolder(ctx context.Context, vm, path string) (files, bytes int64, err error) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var serr error
		files, bytes, serr = m.Imp.Stream(path, pw)
		pw.CloseWithError(serr)
	}()
	_, err = m.L.ShellStdin(ctx, vm, pr, "bash", "-c", "mkdir -p ~/work/main && tar -xf - -C ~/work/main")
	pr.Close()
	<-done
	return files, bytes, err
}

func (m *Manager) Create(ctx context.Context, name, repo, importPath string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if repo != "" && importPath != "" {
		return fmt.Errorf("Git-Repo und Ordner-Import schließen sich aus")
	}
	if importPath != "" {
		if m.Imp == nil {
			return fmt.Errorf("Import nicht verfügbar")
		}
		if _, _, err := m.Imp.Preview(importPath); err != nil {
			return fmt.Errorf("Import: %w", err)
		}
	}
	vm := VMName(name)
	if ok, err := m.exists(ctx, vm); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("VM %s existiert bereits", vm)
	}
	progress(ctx, "Basis-VM prüfen")
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
	progress(ctx, "VM wird aus der Basis geklont")
	if _, err := m.L.R.Run(ctx, "clone", m.BaseName(), vm, "--tty=false",
		"--mount-only", in, "--mount-only", out+":w"); err != nil {
		return err
	}
	progress(ctx, "VM startet (ca. 10–15 Sekunden)")
	if err := m.L.Start(ctx, vm); err != nil {
		return err
	}
	progress(ctx, "Erstkonfiguration (neue Maschinen-ID, SSH-Host-Keys)")
	if _, err := m.L.Shell(ctx, vm, "bash", "-c", firstBoot); err != nil {
		return fmt.Errorf("First-Boot-Schritt: %w", err)
	}
	// Host-Keys wurden neu erzeugt: Lima muss den Key beim nächsten Start neu pinnen.
	if err := lima.RepinHostKey(vm); err != nil {
		return err
	}
	progress(ctx, "Datei-/SSH-Hilfsprogramm installieren")
	if err := m.InstallMCP(ctx, vm); err != nil {
		return err
	}
	// Basis-SSH-Config (Include) anlegen, noch ohne Freigaben.
	if err := (Hooks{M: m, Render: func([]store.SSHGrant) string { return "# Von Bergfried verwaltet\n" }}).applyDirect(ctx, vm); err != nil {
		return fmt.Errorf("SSH-Config: %w", err)
	}
	if repo != "" {
		progress(ctx, "Git-Repo wird geklont")
		if _, err := m.L.Shell(ctx, vm, "git", "clone", repo, "work/main"); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	}
	if importPath != "" {
		progress(ctx, "Ordner wird in die VM kopiert")
		files, bytes, err := m.importFolder(ctx, vm, importPath)
		if err != nil {
			return fmt.Errorf("Import: %w", err)
		}
		m.audit("session.import", name, "", fmt.Sprintf("%s files=%d bytes=%d", importPath, files, bytes))
	}
	if err := m.S.AddSession(store.Session{Name: name, VM: vm, Repo: repo, TemplateVersion: m.Rev(), Created: time.Now()}); err != nil {
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
	progress(ctx, "VM startet")
	if err := m.L.Start(ctx, s.VM); err != nil {
		return err
	}
	progress(ctx, "Hilfsprogramme und Agents werden gestartet")
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

// Hooks: VM-seitige Wirkung der SSH-Freigaben (für broker.VMHooks).
type Hooks struct {
	M *Manager
	// Render erzeugt den Konfig-Text aus aktiven Freigaben.
	Render func([]store.SSHGrant) string
}

const applyScript = `set -e
umask 077
mkdir -p ~/.ssh && chmod 700 ~/.ssh
printf %%s '%s' | base64 -d > ~/.ssh/bergfried.conf
if ! grep -qs '^Include ~/.ssh/bergfried.conf' ~/.ssh/config; then
  { printf 'Include ~/.ssh/bergfried.conf\n'; cat ~/.ssh/config 2>/dev/null || true; } > ~/.ssh/config.new
  mv ~/.ssh/config.new ~/.ssh/config
fi
chmod 600 ~/.ssh/config ~/.ssh/bergfried.conf`

func (h Hooks) vm(session string) (string, error) {
	s, err := h.M.S.GetSession(session)
	return s.VM, err
}

func (h Hooks) applyDirect(ctx context.Context, vm string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(h.Render(nil)))
	_, err := h.M.L.Shell(ctx, vm, "bash", "-c", fmt.Sprintf(applyScript, b64))
	return err
}

func (h Hooks) Apply(ctx context.Context, session string, active []store.SSHGrant) error {
	vm, err := h.vm(session)
	if err != nil {
		return err
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(h.Render(active)))
	_, err = h.M.L.Shell(ctx, vm, "bash", "-c", fmt.Sprintf(applyScript, b64))
	return err
}

// KillSSH beendet laufende ssh-Prozesse in der VM (Ablauf/Entzug).
func (h Hooks) KillSSH(ctx context.Context, session string) error {
	vm, err := h.vm(session)
	if err != nil {
		return err
	}
	_, err = h.M.L.Shell(ctx, vm, "bash", "-c", "pkill -x ssh || true")
	return err
}

// RestartAgent beendet das tmux-Fenster des Agents und startet ihn neu (mit --continue und Remote Control).
func (m *Manager) RestartAgent(ctx context.Context, session, agent string) error {
	if err := ValidName(agent); err != nil {
		return err
	}
	s, err := m.S.GetSession(session)
	if err != nil {
		return err
	}
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-c", fmt.Sprintf("tmux kill-window -t %s:%s 2>/dev/null || true", tmuxSession, agent)); err != nil {
		return err
	}
	if err := m.ensureWindow(ctx, s.VM, session, agent); err != nil {
		return err
	}
	m.audit("agent.restart", session, agent, "")
	return nil
}

// PressEnter bestätigt eine Standardauswahl im Agent-Fenster (z. B. Theme-Frage beim ersten Start).
func (m *Manager) PressEnter(ctx context.Context, vm, agent string) error {
	if err := ValidName(agent); err != nil {
		return err
	}
	_, err := m.L.Shell(ctx, vm, "bash", "-c", fmt.Sprintf("tmux send-keys -t %s:%s Enter", tmuxSession, agent))
	return err
}
