package sandbox

import (
	"fmt"
	"strings"
)

// claudeCmd builds the shell line for a worktree's Claude window. --continue restores the
// history; if there is none (new worktree) the fallback starts fresh. OpenCode is a
// sandbox-level service and does not use this command.
func claudeCmd(sandbox, worktree string) string {
	env := fmt.Sprintf("export COMPOSE_PROJECT_NAME=%s-%s", sandbox, worktree)
	rc := fmt.Sprintf("--remote-control '%s/%s'", sandbox, worktree)
	return fmt.Sprintf(`cd ~/work/%[2]s && %[3]s BERGFRIED_WORKTREE=%[1]s/%[2]s && (~/.local/bin/claude --continue %[4]s || exec ~/.local/bin/claude %[4]s)`, sandbox, worktree, env, rc)
}

// installOpenCode is the shell installer for the OpenCode CLI (latest version, user-local,
// no root/apt). It is used by the sandbox-level OpenCode service, not per worktree.
func installOpenCode() string {
	return `set -Eeuo pipefail
LOG=/tmp/opencode-install.log
: > "$LOG"
exec > >(tee -a "$LOG") 2>&1
trap 'echo "FAILED at line $LINENO: $BASH_COMMAND" >&2' ERR

echo "== OpenCode installation $(date -Is) =="
echo "user=$(id -un) home=$HOME arch=$(uname -m)"
echo "opencode in PATH: $(command -v opencode || echo none)"
df -h /tmp "$HOME" || true
for c in curl tar jq sha1sum openssl sudo; do printf '%s: ' "$c"; command -v "$c" || echo missing; done

OC="$HOME/.opencode/bin/opencode"
echo "-- leftovers --"
ls -ld "$HOME/.opencode" "$HOME/.opencode/bin" 2>/dev/null || true
ls -l "$OC" 2>/dev/null || true

# Repair directories left behind by a previous sudo/attempt, otherwise the installer cannot write.
for d in "$HOME/.opencode" "$HOME/.config/opencode" "$HOME/.local/share/opencode" "$HOME/.cache/opencode" "$HOME/.local/state/opencode"; do
  if [ -e "$d" ] && [ "$(stat -c %u "$d" 2>/dev/null || echo 0)" != "$(id -u)" ]; then
    echo "repairing ownership of $d"
    sudo chown -R "$(id -u):$(id -g)" "$d"
  fi
done
if [ -e "$HOME/.opencode" ] && [ ! -d "$HOME/.opencode" ]; then rm -f "$HOME/.opencode"; fi
if [ -e "$HOME/.opencode/bin" ] && [ ! -d "$HOME/.opencode/bin" ]; then rm -f "$HOME/.opencode/bin"; fi
mkdir -p "$HOME/.opencode/bin"
if [ -d "$OC" ] && [ ! -L "$OC" ]; then rm -rf "$OC"; fi
rm -f "$OC" "$OC.new"
rm -rf /tmp/opencode_install_* /tmp/opencode.tgz

# Use the normal installer, but download it first so download and execution are separate.
SCRIPT=/tmp/opencode-install.sh.download
echo "-- downloading the installer --"
curl -fsSL --connect-timeout 20 https://opencode.ai/v2/install -o "$SCRIPT"
echo "installer bytes=$(wc -c < "$SCRIPT")"
echo "-- running the installer --"
# Clean PATH: an old opencode in /usr/local/bin or ~/.local/bin must not trigger the
# installer's version check.
PATH=/usr/bin:/bin:/usr/sbin:/sbin bash "$SCRIPT"

[ -x "$OC" ] || { echo "OpenCode: binary missing after installation" >&2; exit 1; }
echo "-- installed version --"
"$OC" --version

for f in "$HOME/.profile" "$HOME/.bashrc"; do
  if [ -f "$f" ] && ! grep -qF '.opencode/bin' "$f"; then echo 'export PATH="$HOME/.opencode/bin:$PATH"' >> "$f"; fi
done
[ -f "$HOME/.profile" ] || echo 'export PATH="$HOME/.opencode/bin:$PATH"' > "$HOME/.profile"
echo "OK"`
}

// openCodeCheckScript reports whether a usable OpenCode v2+ binary is already installed.
func openCodeCheckScript() string {
	return `OC="$HOME/.opencode/bin/opencode"
if [ -x "$OC" ]; then
  v=$("$OC" --version 2>/dev/null | grep -oE '[0-9]+[.][0-9]+[.][0-9]+' | head -1 || true)
  major=${v%%.*}
  if [ -n "$major" ] && [ "$major" -ge 2 ]; then echo yes; fi
fi`
}

// openCodeInstallScript is the installer used by InstallOpenCode (same content as installOpenCode).
func openCodeInstallScript() string { return installOpenCode() }

// LoginCommand runs the interactive sign-in for an assistant runtime in the VM.
func LoginCommand(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "claude":
		return `"$HOME/.local/bin/claude" auth login`, nil
	case "opencode":
		return `"$HOME/.opencode/bin/opencode" auth login`, nil
	}
	return "", fmt.Errorf("unknown runtime %q (allowed: claude, opencode)", name)
}
