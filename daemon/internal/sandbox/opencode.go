package sandbox

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"strings"
	"time"
)

// OpenCode listens on a stable, per-sandbox port inside the VM. Lima forwards ports bound to
// 0.0.0.0 to 127.0.0.1 on the Mac with the same number, so the one-time pairing link works in
// the OpenCode desktop app without exposing anything to the network.
const (
	openCodePortFirst = 42000
	openCodePortLast  = 42999
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// portFree reports whether a local TCP port can be bound on the Mac.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// openCodePort picks a deterministic but free port for a sandbox.
func openCodePort(name string) (int, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	start := openCodePortFirst + int(h.Sum32()%uint32(openCodePortLast-openCodePortFirst+1))
	for i := 0; i <= openCodePortLast-openCodePortFirst; i++ {
		p := openCodePortFirst + (start-openCodePortFirst+i)%(openCodePortLast-openCodePortFirst+1)
		if portFree(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free local port for the OpenCode connection")
}

// ensureOpenCodePort returns the sandbox's stable port, assigning one on first use.
func (m *Manager) ensureOpenCodePort(sandbox string, current int) (int, error) {
	if current != 0 {
		return current, nil
	}
	port, err := openCodePort(sandbox)
	if err != nil {
		return 0, err
	}
	if err := m.S.SetOpenCodePort(sandbox, port); err != nil {
		return 0, err
	}
	return port, nil
}

// OpenCodeStart installs (if needed) and starts the sandbox-level OpenCode service. There is one
// service per sandbox, not one per worktree: the desktop app uses the worktrees as projects.
func (m *Manager) OpenCodeStart(ctx context.Context, sandbox string) error {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return err
	}
	// Configures (or updates) the MCP server for OpenCode and installs the CLI if needed.
	if err := m.InstallHelpers(ctx, s.VM, sandbox, true); err != nil {
		return err
	}
	port, err := m.ensureOpenCodePort(sandbox, s.OpenCodePort)
	if err != nil {
		return err
	}
	if err := m.startOpenCodeService(ctx, s.VM, port); err != nil {
		return err
	}
	if err := m.S.SetOpenCode(sandbox, true); err != nil {
		return err
	}
	m.audit("opencode.start", sandbox, "", fmt.Sprintf("port=%d", port))
	return nil
}

// startOpenCodeService runs the setup script that pins the port and (re)starts the service.
func (m *Manager) startOpenCodeService(ctx context.Context, vm string, port int) error {
	out, err := m.L.ShellStdin(ctx, vm, strings.NewReader(openCodePairSetupScript(port)), "bash", "-s")
	if err != nil {
		return fmt.Errorf("OpenCode service: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resumeOpenCode starts the service after a sandbox start if the user had started it before.
func (m *Manager) resumeOpenCode(ctx context.Context, vm, sandbox string) error {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return err
	}
	if !s.OpenCode {
		return nil
	}
	if err := m.InstallOpenCode(ctx, vm); err != nil {
		return err
	}
	port, err := m.ensureOpenCodePort(sandbox, s.OpenCodePort)
	if err != nil {
		return err
	}
	return m.startOpenCodeService(ctx, vm, port)
}

// OpenCodeStop stops the sandbox-level OpenCode service. Installed versions and sessions remain.
func (m *Manager) OpenCodeStop(ctx context.Context, sandbox string) error {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return err
	}
	script := `OC="$HOME/.opencode/bin/opencode"; [ -x "$OC" ] || exit 0; "$OC" service stop >/dev/null 2>&1 || true`
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-lc", script); err != nil {
		return err
	}
	if err := m.S.SetOpenCode(sandbox, false); err != nil {
		return err
	}
	m.audit("opencode.stop", sandbox, "", "")
	return nil
}

// PairOpenCode starts the local OpenCode server if needed and returns a one-time pairing link for
// the desktop app. It never exposes the server to the internet (no --remote).
func (m *Manager) PairOpenCode(ctx context.Context, sandbox string) (string, error) {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return "", err
	}
	if err := m.OpenCodeStart(ctx, sandbox); err != nil {
		return "", err
	}
	s, err = m.S.GetSandbox(sandbox)
	if err != nil {
		return "", err
	}
	port := s.OpenCodePort
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := m.L.Shell(cctx, s.VM, "bash", "-lc", `"$HOME/.opencode/bin/opencode" pair`)
	if err != nil {
		return "", fmt.Errorf("opencode pair: %w", err)
	}
	link := pairLink(string(out))
	if link == "" {
		return "", fmt.Errorf("opencode pair returned no link")
	}
	if !linkUsesPort(link, port) {
		return "", fmt.Errorf("opencode pair returned a link on the wrong port (expected %d): %s", port, link)
	}
	m.audit("opencode.pair", sandbox, "", fmt.Sprintf("port=%d", port))
	return link, nil
}

// openCodePairSetupScript configures and restarts the shared server. It always restarts the
// service: the config may already name the right port while the running service still sits on
// the old default port (that was exactly how a 49374 link appeared).
func openCodePairSetupScript(port int) string {
	return fmt.Sprintf(`set -e
OC="$HOME/.opencode/bin/opencode"
curh=$("$OC" service get hostname 2>/dev/null || true)
curp=$("$OC" service get port 2>/dev/null || true)
[ "$curh" = 0.0.0.0 ] || "$OC" service set hostname 0.0.0.0
[ "$curp" = %[1]d ] || "$OC" service set port %[1]d
"$OC" service restart >/dev/null 2>&1 || "$OC" service start
echo "service=$("$OC" service status 2>/dev/null || true)"`, port)
}

// linkUsesPort checks that the pairing link points at the sandbox's dedicated port.
func linkUsesPort(link string, port int) bool {
	return strings.Contains(link, fmt.Sprintf(":%d/", port))
}

// pairLink picks the loopback link from `opencode pair` output (it also prints the VM's LAN address).
func pairLink(out string) string {
	first := ""
	for _, line := range strings.Split(ansiRe.ReplaceAllString(strings.TrimSpace(out), ""), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			continue
		}
		if first == "" {
			first = line
		}
		// Lima forwards the loopback port to 127.0.0.1 on the Mac; the LAN address is not usable there.
		if strings.Contains(line, "127.0.0.1") || strings.Contains(line, "localhost") {
			return line
		}
	}
	return first
}

// ReloadOpenCode makes a running OpenCode server pick up new credentials/config.
func (m *Manager) ReloadOpenCode(ctx context.Context, vm string) {
	_, _ = m.L.Shell(ctx, vm, "bash", "-lc", `OC="$HOME/.opencode/bin/opencode"; [ -x "$OC" ] && "$OC" reload >/dev/null 2>&1 || true`)
}
