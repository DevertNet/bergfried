package sandbox

// Folder management for the sandbox overview: list every folder under ~/work (with or without a
// Claude entry), create, delete, and copy/move a folder into another sandbox. These actions are
// only reachable from the app's API; there is no MCP tool, so an assistant cannot move data
// between sandboxes by itself.

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
)

// FolderInfo describes one top-level folder under ~/work in a VM.
type FolderInfo struct {
	Path     string // relative to home, e.g. "work/shop"
	Name     string
	Git      bool
	Branch   string
	Dirty    bool  // uncommitted changes (git folders only)
	Modified int64 // unix seconds of the folder's last change
}

// topFolder validates a folder that sits directly below ~/work and returns its name.
func topFolder(in string) (folder, name string, err error) {
	folder, err = normalizeFolder(in)
	if err != nil {
		return "", "", err
	}
	name = strings.TrimPrefix(folder, "work/")
	if strings.Contains(name, "/") || !newFolderRe.MatchString(name) || name == "." || name == ".." {
		return "", "", fmt.Errorf("choose a folder directly below ~/work")
	}
	return folder, name, nil
}

func validNewName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || !newFolderRe.MatchString(name) {
		return "", fmt.Errorf("invalid folder name %q (letters, digits, . _ - and at most 64 characters)", name)
	}
	return name, nil
}

const listFoldersScript = `cd "$HOME/work" 2>/dev/null || exit 0
find . -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '%f\n' 2>/dev/null | LC_ALL=C sort | while IFS= read -r n; do
  b=""; d=0; g=0
  if [ -e "$n/.git" ]; then
    g=1
    b=$(git -C "$n" symbolic-ref --short -q HEAD 2>/dev/null || git -C "$n" rev-parse --short HEAD 2>/dev/null || true)
    [ -z "$(git -C "$n" status --porcelain 2>/dev/null | head -n 1)" ] || d=1
  fi
  m=$(stat -c %Y "$n" 2>/dev/null || echo 0)
  printf '%s\t%s\t%s\t%s\t%s\n' "$n" "$g" "$d" "$m" "$b"
done`

// parseFolderList reads the output of listFoldersScript. Names that could not be addressed later are skipped.
func parseFolderList(out string) []FolderInfo {
	var res []FolderInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 4 {
			continue
		}
		folder, name, err := topFolder("work/" + f[0])
		if err != nil {
			continue
		}
		mod, _ := strconv.ParseInt(f[3], 10, 64)
		fi := FolderInfo{Path: folder, Name: name, Git: f[1] == "1", Dirty: f[2] == "1", Modified: mod}
		if len(f) > 4 {
			fi.Branch = strings.TrimSpace(f[4])
		}
		res = append(res, fi)
	}
	return res
}

// ListFolders lists the visible top-level folders under ~/work of a running sandbox.
func (m *Manager) ListFolders(ctx context.Context, sandbox string) ([]FolderInfo, error) {
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return nil, err
	}
	out, err := m.L.Shell(ctx, s.VM, "bash", "-c", listFoldersScript)
	if err != nil {
		return nil, err
	}
	return parseFolderList(string(out)), nil
}

var gitURLRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._~/%+-]+$`)

// repoName derives a folder name from a clone URL ("https://github.com/o/shop.git" → "shop").
func repoName(url string) string {
	u := strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	return u
}

// FolderCreate creates an empty folder ~/work/<name>, or clones gitURL (https only, no credentials in the URL) into it.
// An empty name with a URL uses the repository name.
func (m *Manager) FolderCreate(ctx context.Context, sandbox, name, gitURL string) (string, error) {
	gitURL = strings.TrimSpace(gitURL)
	if gitURL != "" {
		if !gitURLRe.MatchString(gitURL) || strings.Contains(gitURL, "..") {
			return "", fmt.Errorf("the repository URL must look like https://host/owner/repo (no credentials in the URL)")
		}
		if strings.TrimSpace(name) == "" {
			name = repoName(gitURL)
		}
	}
	name, err := validNewName(name)
	if err != nil {
		return "", err
	}
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return "", err
	}
	script := fmt.Sprintf(`set -e
mkdir -p "$HOME/work"
[ ! -e "$HOME/work/%[1]s" ] || { echo "folder already exists" >&2; exit 4; }
`, name)
	if gitURL == "" {
		script += fmt.Sprintf(`mkdir "$HOME/work/%s"`, name)
	} else {
		progress(ctx, "Cloning repository …")
		script += fmt.Sprintf(`GIT_TERMINAL_PROMPT=0 git clone --quiet -- %q "$HOME/work/%s"`, gitURL, name)
	}
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-c", script); err != nil {
		return "", err
	}
	m.audit("folder.create", sandbox, "work/"+name, gitURL)
	return "work/" + name, nil
}

// unpushedOne is unpushedCheck for a single folder (relative name below ~/work).
func unpushedOne(name string) string {
	return fmt.Sprintf(`d="$HOME/work/%s"; { [ -d "$d/.git" ] || [ -f "$d/.git" ]; } || exit 0
s=$(git -C "$d" status --porcelain); u=$(git -C "$d" log --branches --not --remotes --oneline)
[ -z "$s$u" ] || { echo "== $d"; echo "$s"; echo "$u"; }`, name)
}

// removeClaudeEntry stops Claude and drops the entry for a folder if there is one.
func (m *Manager) removeClaudeEntry(ctx context.Context, sandbox, folder string) error {
	if _, ok, err := m.claudeSession(sandbox, folder); err != nil || !ok {
		return err
	}
	return m.ClaudeRemove(ctx, sandbox, folder)
}

// FolderDelete deletes ~/work/<name> in the VM. Unless force is set, unsaved git work is reported first
// (ErrUnpushed). A Claude entry for the folder is stopped and removed.
func (m *Manager) FolderDelete(ctx context.Context, sandbox, folder string, force bool) error {
	folder, name, err := topFolder(folder)
	if err != nil {
		return err
	}
	s, err := m.S.GetSandbox(sandbox)
	if err != nil {
		return err
	}
	if !force {
		out, err := m.L.Shell(ctx, s.VM, "bash", "-c", unpushedOne(name))
		if err != nil {
			return fmt.Errorf("check for unpushed commits failed (nothing deleted): %w", err)
		}
		if r := strings.TrimSpace(string(out)); r != "" {
			return ErrUnpushed{Report: r}
		}
	}
	if err := m.removeClaudeEntry(ctx, sandbox, folder); err != nil {
		return err
	}
	if _, err := m.L.Shell(ctx, s.VM, "bash", "-c", fmt.Sprintf(`rm -rf -- "$HOME/work/%s"`, name)); err != nil {
		return err
	}
	m.audit("folder.delete", sandbox, folder, fmt.Sprintf("force=%v", force))
	return nil
}

// sudoProbe sets S to "sudo -n" when something below $d belongs to another user (for example files that a root
// container or tool created inside the project, which the normal user cannot read), else to "". Folders that
// the user fully owns never use sudo.
const sudoProbe = `S=""; if [ -n "$(find "$d" ! -user "$(id -un)" -print -quit 2>/dev/null)" ] && sudo -n true 2>/dev/null; then S="sudo -n"; fi`

// statScript prints "<files> <bytes>" of the regular files below ~/work/<name>.
func statScript(name string) string {
	return fmt.Sprintf(`d="$HOME/work/%s"; [ -d "$d" ] || exit 3
%s
$S find "$d" -type f -printf '%%s\n' | awk '{s+=$1} END {print NR, s+0}'`, name, sudoProbe)
}

// rmScript deletes ~/work/<name> (with sudo when part of it belongs to another user, see sudoProbe).
func rmScript(name string) string {
	return fmt.Sprintf(`d="$HOME/work/%s"
%s
$S rm -rf -- "$HOME/work/%s"`, name, sudoProbe, name)
}

// shortErr keeps an error readable: tar can print one line per unreadable file.
func shortErr(err error) error {
	const max = 500
	msg := err.Error()
	if len(msg) > max {
		return fmt.Errorf("%s … (cut; full text in the daemon log)", msg[:max])
	}
	return err
}

func parseStat(out []byte) (files, bytes int64, err error) {
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected output %q", strings.TrimSpace(string(out)))
	}
	if files, err = strconv.ParseInt(f[0], 10, 64); err != nil {
		return 0, 0, err
	}
	bytes, err = strconv.ParseInt(f[1], 10, 64)
	return files, bytes, err
}

// tarOutScript writes ~/work/<name> as a tar stream to stdout. tar's exit status 1 ("file changed
// as we read it", for example while a process writes) is tolerated; the file count check decides.
func tarOutScript(name string) string {
	return fmt.Sprintf(`d="$HOME/work/%[1]s"
%[2]s
$S tar -C "$HOME/work" -cpf - -- %[1]s; rc=$?; [ "$rc" -le 1 ] || exit "$rc"`, name, sudoProbe)
}

// tarInScript unpacks the tar stream into a hidden temporary folder and renames it to ~/work/<newName>
// at the end, so a break never leaves a half-filled folder under the real name.
func tarInScript(name, newName string) string {
	return fmt.Sprintf(`set -e
w="$HOME/work"; mkdir -p "$w"
[ ! -e "$w/%[2]s" ] || { echo "folder %[2]s already exists in the target" >&2; exit 4; }
t=$(mktemp -d "$w/.bf-incoming.XXXXXX")
trap 'rm -rf "$t"' EXIT
tar -C "$t" -xpf -
[ -d "$t/%[1]s" ] || { echo "the archive did not contain %[1]s" >&2; exit 5; }
mv "$t/%[1]s" "$w/%[2]s"`, name, newName)
}

// requireRunning fails with a clear message if the VM of a sandbox is not running.
func (m *Manager) requireRunning(ctx context.Context, sandbox, vm string) error {
	list, err := m.L.List(ctx)
	if err != nil {
		return err
	}
	for _, i := range list {
		if i.Name == vm {
			if i.Status == "Running" {
				return nil
			}
			return fmt.Errorf("sandbox %s is not running (start it first)", sandbox)
		}
	}
	return fmt.Errorf("VM of sandbox %s not found", sandbox)
}

// FolderTransfer copies ~/work/<folder> of one sandbox to ~/work/<newName> of another (or the same) sandbox.
// With move set the source is deleted afterwards, but only after the copy was verified (same number of
// files and bytes). The source Claude entry is stopped first when moving. Nothing is ever overwritten.
func (m *Manager) FolderTransfer(ctx context.Context, src, folder, dst, newName string, move bool) error {
	folder, name, err := topFolder(folder)
	if err != nil {
		return err
	}
	if strings.TrimSpace(newName) == "" {
		newName = name
	}
	if newName, err = validNewName(newName); err != nil {
		return err
	}
	if src == dst && (move || newName == name) {
		return fmt.Errorf("choose another sandbox or another name")
	}
	ss, err := m.S.GetSandbox(src)
	if err != nil {
		return err
	}
	ds, err := m.S.GetSandbox(dst)
	if err != nil {
		return err
	}
	for _, p := range [][2]string{{src, ss.VM}, {dst, ds.VM}} {
		if err := m.requireRunning(ctx, p[0], p[1]); err != nil {
			return err
		}
	}
	if _, err := m.L.Shell(ctx, ds.VM, "bash", "-c", fmt.Sprintf(`[ ! -e "$HOME/work/%s" ] || { echo exists; exit 4; }`, newName)); err != nil {
		return fmt.Errorf("folder %s already exists in sandbox %s", newName, dst)
	}
	if move {
		progress(ctx, "Stopping Claude in the source folder")
		if err := m.removeClaudeEntry(ctx, src, folder); err != nil {
			return err
		}
	}
	out, err := m.L.Shell(ctx, ss.VM, "bash", "-c", statScript(name))
	if err != nil {
		return fmt.Errorf("source folder %s is not available: %w", folderDisplay(folder), err)
	}
	wantFiles, wantBytes, err := parseStat(out)
	if err != nil {
		return err
	}
	verb := "Copying"
	if move {
		verb = "Moving"
	}
	progress(ctx, fmt.Sprintf("%s %s (%d files) to %s", verb, name, wantFiles, dst))
	lima.Output(ctx, fmt.Sprintf("%s ~/work/%s → %s:~/work/%s (%d files, %d bytes)", verb, name, dst, newName, wantFiles, wantBytes))
	err = m.L.ShellPipe(ctx, ss.VM, []string{"bash", "-c", tarOutScript(name)}, ds.VM, []string{"bash", "-c", tarInScript(name, newName)})
	if err != nil {
		log.Printf("folder transfer %s:%s → %s:%s failed: %v", src, folder, dst, newName, err)
		return fmt.Errorf("transfer failed (nothing was removed from the source): %w", shortErr(err))
	}
	progress(ctx, "Verifying the copy")
	out, err = m.L.Shell(ctx, ds.VM, "bash", "-c", statScript(newName))
	if err == nil {
		var gotFiles, gotBytes int64
		if gotFiles, gotBytes, err = parseStat(out); err == nil && (gotFiles != wantFiles || gotBytes != wantBytes) {
			err = fmt.Errorf("the copy differs from the source (%d files/%d bytes instead of %d/%d)", gotFiles, gotBytes, wantFiles, wantBytes)
		}
	}
	if err != nil {
		// Never leave an unverified copy behind.
		_, _ = m.L.Shell(ctx, ds.VM, "bash", "-c", fmt.Sprintf(`rm -rf -- "$HOME/work/%s"`, newName))
		return fmt.Errorf("verification failed, the incomplete copy was removed and the source is untouched: %w", err)
	}
	if move {
		progress(ctx, "Removing the source folder")
		if _, err := m.L.Shell(ctx, ss.VM, "bash", "-c", rmScript(name)); err != nil {
			return fmt.Errorf("the folder was copied to %s, but the source could not be removed: %w", dst, err)
		}
		m.audit("folder.move", src, folder, fmt.Sprintf("→ %s:work/%s", dst, newName))
		return nil
	}
	m.audit("folder.copy", src, folder, fmt.Sprintf("→ %s:work/%s", dst, newName))
	return nil
}
