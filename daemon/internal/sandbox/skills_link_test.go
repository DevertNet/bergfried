package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
)

// runSkillsLinkEnv runs the link script with extra environment variables for bash.
func runSkillsLinkEnv(t *testing.T, skillsDir, home string, extra ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-s")
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(append(env, "HOME="+home), extra...)
	cmd.Stdin = strings.NewReader(skillsLinkScript(skillsDir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runSkillsLink runs the link script with the real bash, HOME and the skills folder in temp dirs.
func runSkillsLink(t *testing.T, skillsDir, home string) (string, error) {
	t.Helper()
	return runSkillsLinkEnv(t, skillsDir, home)
}

func mkSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linkState snapshots the entries of the link folder: "link:<target>" for symlinks, else the type.
func linkState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = "link:" + target
		case e.IsDir():
			out[e.Name()] = "dir"
		default:
			out[e.Name()] = "file"
		}
	}
	return out
}

func linkTarget(t *testing.T, p string) string {
	t.Helper()
	target, err := os.Readlink(p)
	if err != nil {
		t.Fatalf("%s is not a symlink: %v", p, err)
	}
	return target
}

func equalStates(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

func TestSkillsLinkScriptLinksAndSkips(t *testing.T) {
	home, sk := t.TempDir(), t.TempDir()
	for _, name := range []string{"demo", "another_skill-2"} {
		mkSkill(t, sk, name, "---\nname: "+name+"\n---\n")
	}
	// Skipped: uppercase, a dot, the reserved name and a folder without SKILL.md.
	mkSkill(t, sk, "Bad", "x")
	mkSkill(t, sk, "a.b", "x")
	mkSkill(t, sk, "synced", "x")
	if err := os.MkdirAll(filepath.Join(sk, "no-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runSkillsLink(t, sk, home)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if out != "" {
		t.Fatalf("the script must print nothing: %q", out)
	}
	d := filepath.Join(home, ".claude", "skills")
	for _, name := range []string{"demo", "another_skill-2"} {
		if got := linkTarget(t, filepath.Join(d, name)); got != filepath.Join(sk, name) {
			t.Fatalf("%s: link target %q", name, got)
		}
	}
	for _, name := range []string{"Bad", "a.b", "synced", "no-skill"} {
		if _, err := os.Lstat(filepath.Join(d, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not be linked: %v", name, err)
		}
	}
}

func TestSkillsLinkScriptIsLocaleIndependent(t *testing.T) {
	home, sk := t.TempDir(), t.TempDir()
	for _, name := range []string{"good", "Bad", "UPPER"} {
		mkSkill(t, sk, name, "x")
	}
	// A UTF-8 locale may not be installed; the script must be correct either way.
	out, err := runSkillsLinkEnv(t, sk, home, "LC_ALL=en_US.UTF-8", "LANG=en_US.UTF-8")
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	d := filepath.Join(home, ".claude", "skills")
	if got := linkTarget(t, filepath.Join(d, "good")); got != filepath.Join(sk, "good") {
		t.Fatalf("good: link target %q", got)
	}
	for _, name := range []string{"Bad", "UPPER"} {
		if _, err := os.Lstat(filepath.Join(d, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must not be linked with a UTF-8 locale: %v", name, err)
		}
	}
}

func TestSkillsLinkScriptKeepsLocalSkillsAndForeignLinks(t *testing.T) {
	home, sk, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	mkSkill(t, sk, "demo", "x")
	mkSkill(t, sk, "foreign", "x")
	d := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(filepath.Join(d, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(d, "demo", "notes.md")
	if err := os.WriteFile(local, []byte("local skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(d, "foreign")); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsLink(t, sk, home); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(d, "demo")); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the local skill folder must stay a real folder: %v %v", fi, err)
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != "local skill" {
		t.Fatalf("the local skill was changed: %q %v", b, err)
	}
	if got := linkTarget(t, filepath.Join(d, "foreign")); got != elsewhere {
		t.Fatalf("the foreign link must stay untouched: %q", got)
	}
}

func TestSkillsLinkScriptRefreshesAndRemovesLinks(t *testing.T) {
	home, sk := t.TempDir(), t.TempDir()
	mkSkill(t, sk, "demo", "x")
	d := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// A managed link with an outdated target and a managed link of a deleted skill.
	if err := os.Symlink(filepath.Join(sk, "demo", "old"), filepath.Join(d, "demo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sk, "gone"), filepath.Join(d, "gone")); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsLink(t, sk, home); err != nil {
		t.Fatal(err)
	}
	if got := linkTarget(t, filepath.Join(d, "demo")); got != filepath.Join(sk, "demo") {
		t.Fatalf("the managed link was not refreshed: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(d, "gone")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the dangling managed link must be removed: %v", err)
	}
	// A skill without SKILL.md loses its link too.
	if err := os.Remove(filepath.Join(sk, "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := runSkillsLink(t, sk, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a skill without SKILL.md must not stay linked: %v", err)
	}
}

func TestSkillsLinkScriptMissingSkillsDir(t *testing.T) {
	home, sk := t.TempDir(), t.TempDir()
	mkSkill(t, sk, "demo", "x")
	if _, err := runSkillsLink(t, sk, home); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sk); err != nil {
		t.Fatal(err)
	}
	out, err := runSkillsLink(t, sk, home)
	if err != nil {
		t.Fatalf("a missing skills folder must not fail the script: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the dangling link must be removed: %v", err)
	}
}

func TestSkillsLinkScriptIsIdempotent(t *testing.T) {
	home, sk := t.TempDir(), t.TempDir()
	mkSkill(t, sk, "demo", "x")
	d := filepath.Join(home, ".claude", "skills")
	if _, err := runSkillsLink(t, sk, home); err != nil {
		t.Fatal(err)
	}
	first := linkState(t, d)
	out, err := runSkillsLink(t, sk, home)
	if err != nil || out != "" {
		t.Fatalf("second run: %v %q", err, out)
	}
	if second := linkState(t, d); !equalStates(first, second) {
		t.Fatalf("second run changed the links: %v != %v", second, first)
	}
}

func TestSkillsLinkScriptQuotesSkillsDir(t *testing.T) {
	home := t.TempDir()
	sk := filepath.Join(t.TempDir(), "sk ills'o")
	mkSkill(t, sk, "demo", "x")
	out, err := runSkillsLink(t, sk, home)
	if err != nil {
		t.Fatalf("a skills folder with a space and a single quote must work: %v\n%s", err, out)
	}
	if got := linkTarget(t, filepath.Join(home, ".claude", "skills", "demo")); got != filepath.Join(sk, "demo") {
		t.Fatalf("link target %q, want %q", got, filepath.Join(sk, "demo"))
	}
}

func TestRelinkSkillsRunsLinkScript(t *testing.T) {
	m, f := mgr(t, "")
	m.Home = t.TempDir()
	if err := m.RelinkSkills(context.Background(), "bergfried-shop"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "shell --tty=false bergfried-shop -- bash -s") {
		t.Fatalf("the link script was not run in the VM: %v", f.calls)
	}
	if len(f.stdin) == 0 || !strings.Contains(f.stdin[0], "SK="+shellQuote(m.SkillsDir())) {
		t.Fatalf("the script must use the skills folder of the manager: %q", f.stdin)
	}
}

// linkFailRunner fails the link script (recognised by its link folder line) and succeeds otherwise.
type linkFailRunner struct {
	calls []string
	stdin []string
}

func (r *linkFailRunner) Run(_ context.Context, a ...string) ([]byte, error) {
	r.calls = append(r.calls, strings.Join(a, " "))
	return nil, nil
}

func (r *linkFailRunner) RunStdin(_ context.Context, in io.Reader, a ...string) ([]byte, error) {
	b, _ := io.ReadAll(in)
	r.calls = append(r.calls, strings.Join(a, " "))
	r.stdin = append(r.stdin, string(b))
	if strings.Contains(string(b), `D="$HOME/.claude/skills"`) {
		return nil, errors.New("link step failed")
	}
	return nil, nil
}

func TestInstallHelpersContinuesWhenSkillsLinkFails(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bergfried-mcp")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERGFRIED_MCP_BIN", bin)
	m, _ := mgr(t, "")
	r := &linkFailRunner{}
	m.L = lima.Client{R: r}
	m.Home = t.TempDir()
	if err := m.InstallHelpers(context.Background(), "bergfried-shop", "shop", false); err != nil {
		t.Fatalf("a failing skills link must not fail the helper setup: %v", err)
	}
	ran := false
	for _, s := range r.stdin {
		if strings.Contains(s, `D="$HOME/.claude/skills"`) {
			ran = true
		}
	}
	if !ran {
		t.Fatalf("the link script was not run: %q", r.stdin)
	}
	entries, err := m.S.AuditList(20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Event == "helpers.skills_link_failed" && e.Sandbox == "shop" && strings.Contains(e.Detail, "link step failed") {
			return
		}
	}
	t.Fatalf("no helpers.skills_link_failed audit entry: %+v", entries)
}
