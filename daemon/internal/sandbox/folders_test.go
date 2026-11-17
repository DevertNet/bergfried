package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// folderFake answers limactl calls: list, shell (by script content) and pipe.
type folderFake struct {
	status  map[string]string // vm → Running/Stopped
	shell   []string          // "<vm>: <script>" of every shell call
	pipes   [][2][]string
	stat    map[string]string // vm → output of the stat script
	pipeErr error
	failOn  string // shell scripts containing this fail
	unsaved string // output of the unpushed check
}

func (f *folderFake) Run(_ context.Context, a ...string) ([]byte, error) {
	if a[0] == "list" {
		var b strings.Builder
		for vm, st := range f.status {
			b.WriteString(`{"name":"` + vm + `","status":"` + st + `"}` + "\n")
		}
		return []byte(b.String()), nil
	}
	vm, script := a[2], strings.Join(a[4:], " ")
	f.shell = append(f.shell, vm+": "+script)
	if f.failOn != "" && strings.Contains(script, f.failOn) {
		return nil, errors.New("fake failure")
	}
	switch {
	case strings.Contains(script, "find \"$d\" -type f"):
		return []byte(f.stat[vm]), nil
	case strings.Contains(script, "log --branches --not --remotes"):
		return []byte(f.unsaved), nil
	}
	return nil, nil
}

func (f *folderFake) RunPipe(_ context.Context, from, to []string) error {
	f.pipes = append(f.pipes, [2][]string{from, to})
	if f.pipeErr != nil {
		return f.pipeErr
	}
	f.stat["bergfried-b"] = f.stat["bergfried-a"] // a faithful copy
	return nil
}

func folderMgr(t *testing.T) (*Manager, *folderFake) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		_ = st.AddSandbox(store.Sandbox{Name: n, VM: "bergfried-" + n, TemplateVersion: 3, Created: time.Now()})
	}
	_ = st.AddWorktree(store.Worktree{Sandbox: "a", Name: "shop", Path: "work/shop", ComposeProject: "a-shop", Claude: true, Created: time.Now()})
	f := &folderFake{status: map[string]string{"bergfried-a": "Running", "bergfried-b": "Running"}, stat: map[string]string{"bergfried-a": "12 3456\n", "bergfried-b": "0 0\n"}}
	return &Manager{L: lima.Client{R: f}, S: st, Actor: "t"}, f
}

func TestParseFolderList(t *testing.T) {
	got := parseFolderList("shop\t1\t1\t1700000000\tmain\nnotes\t0\t0\t5\t\nbad name\t0\t0\t1\t\n..\t0\t0\t1\t\nshort\n")
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if g := got[0]; g.Path != "work/shop" || !g.Git || !g.Dirty || g.Branch != "main" || g.Modified != 1700000000 {
		t.Fatalf("%+v", g)
	}
	if g := got[1]; g.Git || g.Dirty || g.Name != "notes" {
		t.Fatalf("%+v", g)
	}
}

func TestTopFolderRejectsNestedAndTraversal(t *testing.T) {
	for _, ok := range []string{"work/shop", "~/work/shop", "work/my.app-2"} {
		if _, _, err := topFolder(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"work", "work/a/b", "work/..", "work/a b", "/etc", "x/shop", "work/a;rm", ""} {
		if _, _, err := topFolder(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestFolderCreateValidation(t *testing.T) {
	m, f := folderMgr(t)
	ctx := context.Background()
	for _, bad := range [][2]string{{"a b", ""}, {"..", ""}, {"x", "http://github.com/o/r"}, {"x", "https://user:pw@github.com/o/r"}, {"x", "https://github.com/o/r;rm"}, {"x", "git@github.com:o/r.git"}, {"x", "https://github.com/../r"}} {
		if _, err := m.FolderCreate(ctx, "a", bad[0], bad[1]); err == nil {
			t.Fatalf("%v must be rejected", bad)
		}
	}
	if len(f.shell) != 0 {
		t.Fatalf("nothing may run for invalid input: %v", f.shell)
	}
	if p, err := m.FolderCreate(ctx, "a", "", "https://github.com/octo/shop.git"); err != nil || p != "work/shop" {
		t.Fatalf("%q %v", p, err)
	}
	if last := f.shell[len(f.shell)-1]; !strings.Contains(last, `git clone --quiet -- "https://github.com/octo/shop.git" "$HOME/work/shop"`) {
		t.Fatal(last)
	}
	if p, err := m.FolderCreate(ctx, "a", "empty", ""); err != nil || p != "work/empty" {
		t.Fatalf("%q %v", p, err)
	}
	if last := f.shell[len(f.shell)-1]; !strings.Contains(last, `mkdir "$HOME/work/empty"`) {
		t.Fatal(last)
	}
}

func TestFolderDeleteChecksUnsavedWorkAndRemovesEntry(t *testing.T) {
	m, f := folderMgr(t)
	ctx := context.Background()
	f.unsaved = "== /home/x/work/shop\n M a.go"
	err := m.FolderDelete(ctx, "a", "work/shop", false)
	var up ErrUnpushed
	if !errors.As(err, &up) {
		t.Fatalf("want ErrUnpushed, got %v", err)
	}
	for _, c := range f.shell {
		if strings.Contains(c, "rm -rf") {
			t.Fatal("nothing may be deleted before the user confirms")
		}
	}
	if err := m.FolderDelete(ctx, "a", "work/shop", true); err != nil {
		t.Fatal(err)
	}
	if wts, _ := m.S.ListWorktrees("a"); len(wts) != 0 {
		t.Fatalf("the Claude entry must go with the folder: %+v", wts)
	}
	last := f.shell[len(f.shell)-1]
	if !strings.Contains(last, `rm -rf -- "$HOME/work/shop"`) {
		t.Fatal(last)
	}
	for _, bad := range []string{"work", "work/a/b", "work/../x", "x"} {
		if err := m.FolderDelete(ctx, "a", bad, true); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestFolderCopyKeepsSourceAndEntry(t *testing.T) {
	m, f := folderMgr(t)
	if err := m.FolderTransfer(context.Background(), "a", "work/shop", "b", "", false); err != nil {
		t.Fatal(err)
	}
	if len(f.pipes) != 1 {
		t.Fatalf("%v", f.pipes)
	}
	from, to := strings.Join(f.pipes[0][0], " "), strings.Join(f.pipes[0][1], " ")
	if !strings.Contains(from, "bergfried-a") || !strings.Contains(from, "tar -C") || !strings.Contains(to, "bergfried-b") || !strings.Contains(to, "mv ") {
		t.Fatalf("%s | %s", from, to)
	}
	for _, c := range f.shell {
		if strings.Contains(c, "rm -rf -- \"$HOME/work/shop\"") || strings.Contains(c, "kill-window") {
			t.Fatalf("a copy must not touch the source: %s", c)
		}
	}
	if wts, _ := m.S.ListWorktrees("a"); len(wts) != 1 {
		t.Fatal("the source Claude entry must stay on a copy")
	}
}

func TestFolderMoveVerifiesThenDeletesSource(t *testing.T) {
	m, f := folderMgr(t)
	if err := m.FolderTransfer(context.Background(), "a", "work/shop", "b", "shop2", true); err != nil {
		t.Fatal(err)
	}
	if wts, _ := m.S.ListWorktrees("a"); len(wts) != 0 {
		t.Fatal("moving removes the source Claude entry")
	}
	var killed, pipedAt, removedAt = -1, len(f.shell), -1
	for i, c := range f.shell {
		if strings.Contains(c, "kill-window") && killed < 0 {
			killed = i
		}
		if strings.HasPrefix(c, "bergfried-a:") && strings.Contains(c, `rm -rf -- "$HOME/work/shop"`) {
			removedAt = i
		}
	}
	if killed < 0 || removedAt < 0 || killed > removedAt {
		t.Fatalf("Claude must stop before the source is removed: %v", f.shell)
	}
	_ = pipedAt
	if !strings.Contains(strings.Join(f.pipes[0][1], " "), `"$w/shop2"`) {
		t.Fatal("the target name must be used")
	}
}

func TestFolderMoveKeepsSourceWhenVerificationFails(t *testing.T) {
	m, f := folderMgr(t)
	f.stat["bergfried-a"] = "12 3456\n"
	// The copy ends up smaller than the source.
	m.L = lima.Client{R: &shrinkingFake{folderFake: f}}
	err := m.FolderTransfer(context.Background(), "a", "work/shop", "b", "", true)
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("%v", err)
	}
	removedSource, removedCopy := false, false
	for _, c := range f.shell {
		if strings.HasPrefix(c, "bergfried-a:") && strings.Contains(c, `rm -rf -- "$HOME/work/shop"`) {
			removedSource = true
		}
		if strings.HasPrefix(c, "bergfried-b:") && strings.Contains(c, `rm -rf -- "$HOME/work/shop"`) {
			removedCopy = true
		}
	}
	if removedSource || !removedCopy {
		t.Fatalf("source removed=%v, incomplete copy removed=%v", removedSource, removedCopy)
	}
}

type shrinkingFake struct{ *folderFake }

func (s *shrinkingFake) RunPipe(_ context.Context, from, to []string) error {
	s.stat["bergfried-b"] = "11 3000\n"
	return nil
}

func TestFolderTransferErrors(t *testing.T) {
	ctx := context.Background()
	m, f := folderMgr(t)
	if err := m.FolderTransfer(ctx, "a", "work/shop", "a", "", false); err == nil {
		t.Fatal("same sandbox and same name must be rejected")
	}
	if err := m.FolderTransfer(ctx, "a", "work/shop", "a", "copy", true); err == nil {
		t.Fatal("move inside one sandbox is not offered")
	}
	if err := m.FolderTransfer(ctx, "a", "work/shop", "a", "copy", false); err != nil {
		t.Fatalf("copy inside one sandbox with a new name: %v", err)
	}
	for _, bad := range []string{"../x", "a b", "x;y"} {
		if err := m.FolderTransfer(ctx, "a", "work/shop", "b", bad, false); err == nil {
			t.Fatalf("name %q must be rejected", bad)
		}
	}
	if err := m.FolderTransfer(ctx, "a", "work/a/b", "b", "", false); err == nil {
		t.Fatal("nested source must be rejected")
	}
	if err := m.FolderTransfer(ctx, "a", "work/shop", "nope", "", false); err == nil {
		t.Fatal("unknown target")
	}
	f.status["bergfried-b"] = "Stopped"
	if err := m.FolderTransfer(ctx, "a", "work/shop", "b", "", false); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("%v", err)
	}
	f.status["bergfried-b"] = "Running"
	f.failOn = `[ ! -e "$HOME/work/shop" ]`
	pipes := len(f.pipes)
	if err := m.FolderTransfer(ctx, "a", "work/shop", "b", "", true); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("%v", err)
	}
	if len(f.pipes) != pipes {
		t.Fatal("an existing target must stop the transfer before any data moves")
	}
	if wts, _ := m.S.ListWorktrees("a"); len(wts) != 1 {
		t.Fatal("a refused move must not stop Claude in the source")
	}
}

func TestFolderTransferStreamFailureKeepsSource(t *testing.T) {
	m, f := folderMgr(t)
	f.pipeErr = errors.New("broken pipe")
	err := m.FolderTransfer(context.Background(), "a", "work/shop", "b", "", true)
	if err == nil || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("%v", err)
	}
	for _, c := range f.shell {
		if strings.HasPrefix(c, "bergfried-a:") && strings.Contains(c, `rm -rf -- "$HOME/work/shop"`) {
			t.Fatal("source must stay")
		}
	}
}

func TestTarScripts(t *testing.T) {
	if s := tarOutScript("shop"); !strings.Contains(s, `tar -C "$HOME/work" -cpf - -- shop`) || !strings.Contains(s, `-le 1`) {
		t.Fatal(s)
	}
	in := tarInScript("shop", "shop2")
	for _, want := range []string{"mktemp -d", ".bf-incoming", "trap 'rm -rf", "tar -C \"$t\" -xpf -", `mv "$t/shop" "$w/shop2"`, `[ ! -e "$w/shop2" ]`} {
		if !strings.Contains(in, want) {
			t.Fatalf("%q missing:\n%s", want, in)
		}
	}
}

// The real scripts, run with bash and tar against two temporary homes.
func TestTransferScriptsRoundTrip(t *testing.T) {
	for _, bin := range []string{"bash", "tar", "find", "awk"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not available")
		}
	}
	a, b := t.TempDir(), t.TempDir()
	src := filepath.Join(a, "work", "shop")
	if err := os.MkdirAll(filepath.Join(src, "sub", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "sub", "f.txt"), []byte("hello"), 0o640)
	_ = os.WriteFile(filepath.Join(src, ".env"), []byte("K=1"), 0o600)
	_ = os.Symlink("f.txt", filepath.Join(src, "sub", "link"))
	run := func(home, script string, stdin io.Reader) ([]byte, error) {
		c := exec.Command("bash", "-c", script)
		c.Env = append(os.Environ(), "HOME="+home)
		c.Stdin = stdin
		var se bytes.Buffer
		c.Stderr = &se
		out, err := c.Output()
		if err != nil {
			err = fmt.Errorf("%w: %s", err, se.String())
		}
		return out, err
	}
	tarball, err := run(a, tarOutScript("shop"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(b, tarInScript("shop", "shop2"), bytes.NewReader(tarball)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(b, "work", "shop2", "sub", "f.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	if l, err := os.Readlink(filepath.Join(b, "work", "shop2", "sub", "link")); err != nil || l != "f.txt" {
		t.Fatalf("symlink: %q %v", l, err)
	}
	if fi, err := os.Stat(filepath.Join(b, "work", "shop2", ".env")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dotfile and mode must survive: %v %v", fi, err)
	}
	sa, _ := run(a, statScript("shop"), nil)
	sb, _ := run(b, statScript("shop2"), nil)
	if string(sa) != string(sb) || strings.TrimSpace(string(sa)) != "2 8" {
		// 2 regular files (f.txt 5 bytes, .env 3 bytes); symlinks are not regular files.
		t.Fatalf("stat: %q vs %q", sa, sb)
	}
	// An existing target is refused and nothing is left behind.
	if _, err := run(b, tarInScript("shop", "shop2"), bytes.NewReader(tarball)); err == nil {
		t.Fatal("must refuse to overwrite")
	}
	entries, _ := os.ReadDir(filepath.Join(b, "work"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bf-incoming") {
			t.Fatalf("temporary folder left behind: %s", e.Name())
		}
	}
	// Parsing of the real stat output.
	if f, n, err := parseStat(sa); err != nil || f != 2 || n != 8 {
		t.Fatalf("%d %d %v", f, n, err)
	}
}
