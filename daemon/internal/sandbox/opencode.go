package sandbox

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"os"
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

// PairOpenCode prepares the local OpenCode server for the desktop app and returns a one-time
// pairing link. It never exposes the server to the internet (no --remote).
func (m *Manager) PairOpenCode(ctx context.Context, sandbox string) (string, error) {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return "", err
	}
	// The desktop connection is a property of the sandbox, not of an agent: OpenCode can be
	// started and used here without adding an agent or a new worktree.
	if err := m.InstallHelpers(ctx, s.VM, sandbox, true); err != nil {
		return "", err
	}
	port := s.OpenCodePort
	if port == 0 || !portFree(port) {
		port, err = openCodePort(sandbox)
		if err != nil {
			return "", err
		}
	}
	// Make the server reachable for Lima and keep the port stable across restarts.
	setup := openCodePairSetupScript(port)
	out, err := m.L.ShellStdin(ctx, s.VM, strings.NewReader(setup), "bash", "-s")
	if err != nil {
		return "", fmt.Errorf("OpenCode server: %w", err)
	}
	if err := m.S.SetOpenCodePort(sandbox, port); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err = m.L.Shell(cctx, s.VM, "bash", "-lc", `"$HOME/.opencode/bin/opencode" pair`)
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
	m.audit("sandbox.pair", sandbox, "", fmt.Sprintf("port=%d", port))
	// The service restart dropped the TUI connections. Reopen the windows in the background so
	// the one-time link is returned immediately; sessions continue on the server either way.
	go func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer rcancel()
		if _, err := m.RestartRuntimeAgents(rctx, sandbox, RuntimeOpenCode); err != nil {
			fmt.Fprintln(os.Stderr, "OpenCode pairing: restart agents:", err)
		}
	}()
	return link, nil
}

// openCodePairSetupScript configures the shared server before pairing. It always restarts the
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

// RestartRuntimeAgents restarts all agents of one runtime, e.g. after its login was completed.
func (m *Manager) RestartRuntimeAgents(ctx context.Context, sandbox string, runtime Runtime) (int, error) {
	ags, err := m.S.ListAgents(sandbox)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range ags {
		rt, err := ParseRuntime(a.Runtime)
		if err != nil || rt != runtime {
			continue
		}
		if err := m.RestartAgent(ctx, sandbox, a.Name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ReloadOpenCode makes a running OpenCode server pick up new credentials/config.
func (m *Manager) ReloadOpenCode(ctx context.Context, vm string) {
	_, _ = m.L.Shell(ctx, vm, "bash", "-lc", `OC="$HOME/.opencode/bin/opencode"; [ -x "$OC" ] && "$OC" reload >/dev/null 2>&1 || true`)
}
