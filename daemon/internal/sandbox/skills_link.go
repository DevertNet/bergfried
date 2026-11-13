package sandbox

import (
	"context"
	"fmt"
	"strings"
)

// shellQuote wraps s in single quotes so it can be used as one word in a shell script
// (embedded single quotes are closed, escaped and reopened).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// skillsLinkScript returns the script that links the shared skills folder into
// ~/.claude/skills in the VM (Claude Code and OpenCode both read that folder).
// Every direct subfolder of the skills folder that contains a SKILL.md becomes a
// link; local skills of the VM and foreign links are left alone. The script is
// idempotent, prints nothing on success and does not fail when the mount is missing.
func skillsLinkScript(skillsDir string) string {
	return fmt.Sprintf(`set -u
SK=%s
D="$HOME/.claude/skills"
mkdir -p "$D"
# Links of skills that were deleted on the Mac or no longer have a SKILL.md.
for p in "$D"/*; do
  [ -L "$p" ] || continue
  t=$(readlink "$p")
  case "$t" in
    "$SK"/*) [ -f "$t/SKILL.md" ] || rm -f "$p" ;;
  esac
done
# Without the mount there is nothing to link.
[ -d "$SK" ] || exit 0
for d in "$SK"/*/; do
  [ -d "$d" ] || continue
  n=${d%%/}
  n=${n##*/}
  case "$n" in
    [!a-z0-9]*|*[!a-z0-9_-]*) continue ;;
  esac
  [ "$n" = synced ] && continue
  [ -f "$SK/$n/SKILL.md" ] || continue
  p="$D/$n"
  if [ -L "$p" ]; then
    t=$(readlink "$p")
    case "$t" in
      "$SK"/*) ln -sfn "$SK/$n" "$p" ;;
    esac
  elif [ -e "$p" ]; then
    : # a local skill of the VM: leave it alone
  else
    ln -s "$SK/$n" "$p"
  fi
done`, shellQuote(skillsDir))
}

// RelinkSkills links the skills of the shared folder into ~/.claude/skills in the VM.
func (m *Manager) RelinkSkills(ctx context.Context, vm string) error {
	_, err := m.L.ShellStdin(ctx, vm, strings.NewReader(skillsLinkScript(m.SkillsDir())), "bash", "-s")
	return err
}
