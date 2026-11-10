package sandbox

import (
	"fmt"
	"strings"
)

// Runtime is the agent CLI that runs in a tmux window.
type Runtime string

const (
	RuntimeClaude   Runtime = "claude"
	RuntimeOpenCode Runtime = "opencode"
)

// OpenCodeVersion is pinned so that a sandbox keeps a tested version. Users can
// still change the template or install a newer OpenCode by hand.
const OpenCodeVersion = "2.2.6"

// ParseRuntime accepts the stored/API value. Empty means Claude (old rows, old app).
func ParseRuntime(s string) (Runtime, error) {
	switch Runtime(strings.ToLower(strings.TrimSpace(s))) {
	case "", RuntimeClaude:
		return RuntimeClaude, nil
	case RuntimeOpenCode:
		return RuntimeOpenCode, nil
	}
	return "", fmt.Errorf("unknown agent runtime %q (allowed: claude, opencode)", s)
}

func (r Runtime) Valid() bool { return r == RuntimeClaude || r == RuntimeOpenCode }

func (r Runtime) DisplayName() string {
	if r == RuntimeOpenCode {
		return "OpenCode"
	}
	return "Claude Code"
}

// ProcessName is the comm/argv0 of the foreground process in the agent window.
func (r Runtime) ProcessName() string {
	if r == RuntimeOpenCode {
		return "opencode"
	}
	return "claude"
}

// LoginCommand runs in an interactive terminal in the VM.
func (r Runtime) LoginCommand() string {
	if r == RuntimeOpenCode {
		return `"$HOME/.opencode/bin/opencode" auth login`
	}
	return `"$HOME/.local/bin/claude" auth login`
}

// AgentCmd builds the shell line for a tmux window. Names are validated (only [a-z0-9-]).
// --continue restores the history; if there is none (new agent) the fallback starts fresh.
// Claude gets its agent identity through BERGFRIED_AGENT; OpenCode does not, because its
// shared server starts the MCP processes and the environment of the first client would leak
// to all other agents. The MCP server derives the agent from its working directory there.
func (r Runtime) AgentCmd(sandbox, agent string) string {
	env := fmt.Sprintf("export COMPOSE_PROJECT_NAME=%s-%s", sandbox, agent)
	if r == RuntimeOpenCode {
		return fmt.Sprintf(`cd ~/work/%[2]s && %[3]s && (~/.opencode/bin/opencode --continue || exec ~/.opencode/bin/opencode)`, sandbox, agent, env)
	}
	rc := fmt.Sprintf("--remote-control '%s/%s'", sandbox, agent)
	return fmt.Sprintf(`cd ~/work/%[2]s && %[3]s BERGFRIED_AGENT=%[1]s/%[2]s && (~/.local/bin/claude --continue %[4]s || exec ~/.local/bin/claude %[4]s)`, sandbox, agent, env, rc)
}

// installOpenCode installs the pinned OpenCode CLI as the VM user (no root, no apt).
// It downloads the pinned npm package directly instead of piping the vendor installer into a
// shell: the failure is then visible, the version is fixed and no extra code runs.
func installOpenCode(version string) string {
	return fmt.Sprintf(`set -e
OC="$HOME/.opencode/bin/opencode"
if [ -x "$OC" ] && "$OC" --version 2>/dev/null | grep -qE '(^| )v?2[.]'; then exit 0; fi
case "$(uname -m)" in
  aarch64|arm64) arch=arm64 ;;
  x86_64|amd64) if grep -qwi avx2 /proc/cpuinfo 2>/dev/null; then arch=x64; else arch=x64-baseline; fi ;;
  *) echo "OpenCode: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
meta_url="https://registry.npmjs.org/@opencode%%2fcli-linux-$arch/%s"
meta=$(curl -fsSL --connect-timeout 20 "$meta_url") || { echo "OpenCode: could not fetch package metadata from npm (network?)" >&2; exit 1; }
url=$(echo "$meta" | jq -r '.dist.tarball // empty')
want=$(echo "$meta" | jq -r '.dist.integrity // empty')
[ -n "$url" ] || { echo "OpenCode: npm metadata has no tarball URL" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fL --retry 3 --connect-timeout 20 -o "$tmp/opencode.tgz" "$url" || { echo "OpenCode: download failed: $url" >&2; exit 1; }
if [ -n "$want" ] && command -v openssl >/dev/null 2>&1; then
  got="sha512-$(openssl dgst -sha512 -binary "$tmp/opencode.tgz" | base64 | tr -d '\n')"
  [ "$got" = "$want" ] || { echo "OpenCode: checksum mismatch" >&2; exit 1; }
fi
tar -xzf "$tmp/opencode.tgz" -C "$tmp" || { echo "OpenCode: could not extract the download" >&2; exit 1; }
mkdir -p "$HOME/.opencode/bin"
install -m755 "$tmp/package/bin/opencode" "$OC" || { echo "OpenCode: binary not found in the package" >&2; exit 1; }
for f in "$HOME/.profile" "$HOME/.bashrc"; do
  if [ -f "$f" ] && ! grep -qF '.opencode/bin' "$f"; then echo 'export PATH="$HOME/.opencode/bin:$PATH"' >> "$f"; fi
done
[ -f "$HOME/.profile" ] || echo 'export PATH="$HOME/.opencode/bin:$PATH"' > "$HOME/.profile"`, version)
}
