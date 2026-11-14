package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeSkillFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func mustHash(t *testing.T, dir, rel string) string {
	t.Helper()
	h, err := FileHash(dir, rel)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDirAndEnsure(t *testing.T) {
	home := t.TempDir()
	if got, want := Dir(home), filepath.Join(home, "skills"); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
	dir, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	if dir != Dir(home) {
		t.Fatalf("Ensure = %q, want %q", dir, Dir(home))
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("skills folder missing: %v", err)
	}
	if _, err := Ensure(home); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

func TestValidatePath(t *testing.T) {
	accepted := []string{
		"demo/SKILL.md",
		"a/b",
		"my-skill/sub/dir/file.txt",
		"a/b/c/d/e/f",
		"9skill/x",
		"skill_name/x",
		"demo/" + strings.Repeat("x", 100),
		"demo/file.",
		"s/größe-日本.md",
	}
	for _, p := range accepted {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	rejected := []struct{ name, path string }{
		{"empty", ""},
		{"depth one", "demo"},
		{"depth seven", "a/b/c/d/e/f/g"},
		{"empty component", "a//b"},
		{"trailing slash", "a/b/"},
		{"absolute", "/etc/passwd"},
		{"dot component", "a/./b"},
		{"dotdot", "a/../b"},
		{"dotdot escape", "../etc/passwd"},
		{"backslash", `a\b`},
		{"hidden file", "demo/.hidden"},
		{"hidden folder", "demo/.git/config"},
		{"hidden skill", ".hidden/x"},
		{"reserved skill", "synced/x"},
		{"uppercase skill", "Demo/x"},
		{"uppercase mixed skill", "mySkill/x"},
		{"leading dash", "-demo/x"},
		{"leading underscore", "_demo/x"},
		{"NUL", "demo/a\x00b"},
		{"control character", "demo/a\x01b"},
		{"DEL", "demo/a\x7fb"},
		{"zero width space", "s/a\u200bb.md"},
		{"bidi override", "s/\u202eevil.md"},
		{"invalid utf8", "s/\xff.md"},
		{"long component", "demo/" + strings.Repeat("x", 101)},
		{"long skill name", strings.Repeat("a", 101) + "/x"},
	}
	for _, c := range rejected {
		if err := ValidatePath(c.path); err == nil {
			t.Errorf("%s: ValidatePath(%q) = nil, want error", c.name, c.path)
		}
	}
}

func TestValidateChanges(t *testing.T) {
	big := strings.Repeat("a", MaxFileBytes)
	many := func(n int) []Change {
		cs := make([]Change, n)
		for i := range cs {
			cs[i] = Change{Path: "demo/f" + strings.Repeat("x", i), Content: []byte("x")}
		}
		return cs
	}
	tests := []struct {
		name    string
		changes []Change
		wantErr bool
	}{
		{"empty", nil, true},
		{"one write", []Change{{Path: "demo/SKILL.md", Content: []byte("# Demo\n")}}, false},
		{"max files", many(MaxFiles), false},
		{"too many files", many(MaxFiles + 1), true},
		{"duplicate path", []Change{
			{Path: "demo/a.txt", Content: []byte("a")},
			{Path: "demo/a.txt", Content: []byte("b")},
		}, true},
		{"invalid path", []Change{{Path: "../demo/a.txt", Content: []byte("a")}}, true},
		{"delete without content", []Change{{Path: "demo/a.txt", Delete: true}}, false},
		{"delete with content", []Change{{Path: "demo/a.txt", Delete: true, Content: []byte("a")}}, true},
		{"max file size", []Change{{Path: "demo/a.txt", Content: []byte(big)}}, false},
		{"file too large", []Change{{Path: "demo/a.txt", Content: []byte(big + "a")}}, true},
		{"max total size", []Change{
			{Path: "demo/a.txt", Content: []byte(big)},
			{Path: "demo/b.txt", Content: []byte(big)},
			{Path: "demo/c.txt", Content: []byte(big)},
			{Path: "demo/d.txt", Content: []byte(big)},
		}, false},
		{"total too large", []Change{
			{Path: "demo/a.txt", Content: []byte(big)},
			{Path: "demo/b.txt", Content: []byte(big)},
			{Path: "demo/c.txt", Content: []byte(big)},
			{Path: "demo/d.txt", Content: []byte(big)},
			{Path: "demo/e.txt", Content: []byte("x")},
		}, true},
		{"invalid utf8", []Change{{Path: "demo/a.txt", Content: []byte{0xff, 0xfe}}}, true},
		{"NUL byte", []Change{{Path: "demo/a.txt", Content: []byte("a\x00b")}}, true},
		{"plain unicode", []Change{{Path: "demo/utf8.txt", Content: []byte("ä 日本 😀\ttab\nline\n")}}, false},
		{"carriage return", []Change{{Path: "demo/cr.txt", Content: []byte("a\r\nb")}}, true},
		{"escape", []Change{{Path: "demo/esc.txt", Content: []byte("a\x1bb")}}, true},
		{"DEL", []Change{{Path: "demo/del.txt", Content: []byte("a\x7fb")}}, true},
		{"zero width space", []Change{{Path: "demo/zw.txt", Content: []byte("a\u200bb")}}, true},
		{"bidi override", []Change{{Path: "demo/bidi.txt", Content: []byte("a\u202eb")}}, true},
		{"tag character", []Change{{Path: "demo/tag.txt", Content: []byte("a\U000E0041b")}}, true},
		{"line separator", []Change{{Path: "demo/ls.txt", Content: []byte("a\u2028b")}}, true},
		{"paragraph separator", []Change{{Path: "demo/ps.txt", Content: []byte("a\u2029b")}}, true},
		{"case-insensitive duplicate", []Change{
			{Path: "demo/A.txt", Content: []byte("a")},
			{Path: "demo/a.txt", Content: []byte("b")},
		}, true},
		{"path is file and folder", []Change{
			{Path: "demo/a", Content: []byte("a")},
			{Path: "demo/a/b", Content: []byte("b")},
		}, true},
		{"path is folder and file", []Change{
			{Path: "demo/a/b", Content: []byte("b")},
			{Path: "demo/a", Content: []byte("a")},
		}, true},
		{"empty content", []Change{{Path: "demo/empty.txt"}}, false},
	}
	for _, tt := range tests {
		err := ValidateChanges(tt.changes)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: ValidateChanges = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestHashMissingDirIsEmptyTree(t *testing.T) {
	home := t.TempDir()
	dir, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := Hash(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(nil)
	if empty != hex.EncodeToString(sum[:]) {
		t.Fatalf("Hash(empty dir) = %s, want %s", empty, hex.EncodeToString(sum[:]))
	}
	missing, err := Hash(filepath.Join(home, "does-not-exist"))
	if err != nil {
		t.Fatal(err)
	}
	if missing != empty {
		t.Fatalf("Hash(missing dir) = %s, want %s", missing, empty)
	}
}

func TestHashStabilityAndSensitivity(t *testing.T) {
	dir := setup(t)
	writeSkillFile(t, dir, "demo/SKILL.md", "hello\n")
	writeSkillFile(t, dir, "demo/sub/notes.txt", "notes\n")
	base, err := Hash(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Hash(dir)
	if err != nil {
		t.Fatal(err)
	}
	if base != again {
		t.Fatalf("Hash is not stable: %s != %s", base, again)
	}

	t.Run("content", func(t *testing.T) {
		defer restoreFile(t, dir, "demo/SKILL.md", "hello\n")
		writeSkillFile(t, dir, "demo/SKILL.md", "hello world\n")
		if got := mustDirHash(t, dir); got == base {
			t.Fatal("content change did not change the hash")
		}
	})
	t.Run("executable bit", func(t *testing.T) {
		full := filepath.Join(dir, "demo/SKILL.md")
		defer os.Chmod(full, 0o644)
		if err := os.Chmod(full, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := mustDirHash(t, dir); got == base {
			t.Fatal("executable bit did not change the hash")
		}
	})
	t.Run("new file", func(t *testing.T) {
		writeSkillFile(t, dir, "demo/new.txt", "new\n")
		got := mustDirHash(t, dir)
		if err := os.Remove(filepath.Join(dir, "demo/new.txt")); err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatal("new file did not change the hash")
		}
		if back := mustDirHash(t, dir); back != base {
			t.Fatalf("hash did not return to base after removing the file: %s != %s", back, base)
		}
	})
	t.Run("empty directory is implicit", func(t *testing.T) {
		sub := filepath.Join(dir, "demo", "empty")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(sub)
		if got := mustDirHash(t, dir); got != base {
			t.Fatal("empty directory changed the hash")
		}
	})
	t.Run("symlink is not followed", func(t *testing.T) {
		outside := t.TempDir()
		writeSkillFile(t, outside, "inside.txt", "one\n")
		link := filepath.Join(dir, "demo", "outside")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(link)
		h1 := mustDirHash(t, dir)
		writeSkillFile(t, outside, "inside.txt", "two\n")
		if h2 := mustDirHash(t, dir); h2 != h1 {
			t.Fatal("Hash followed a symlink into another directory")
		}
	})
	t.Run("symlink target", func(t *testing.T) {
		link := filepath.Join(dir, "demo", "link")
		if err := os.Symlink("target-a", link); err != nil {
			t.Fatal(err)
		}
		h1 := mustDirHash(t, dir)
		if h1 == base {
			t.Fatal("symlink did not change the hash")
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target-b", link); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(link)
		h2 := mustDirHash(t, dir)
		if h2 == h1 {
			t.Fatal("symlink target change did not change the hash")
		}
	})
}

func restoreFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustDirHash(t *testing.T, dir string) string {
	t.Helper()
	h, err := Hash(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestFileHash(t *testing.T) {
	dir := setup(t)
	writeSkillFile(t, dir, "demo/a.txt", "content\n")
	sum := sha256.Sum256([]byte("content\n"))
	if got, want := mustHash(t, dir, "demo/a.txt"), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("FileHash = %s, want %s", got, want)
	}
	if got, err := FileHash(dir, "demo/missing.txt"); err != nil || got != "" {
		t.Fatalf("FileHash(missing) = %q, %v; want \"\", nil", got, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo", "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := FileHash(dir, "demo/adir"); err == nil {
		t.Fatal("FileHash(directory) = nil error, want error")
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "demo", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := FileHash(dir, "demo/link"); err == nil {
		t.Fatal("FileHash(symlink) = nil error, want error")
	}
	if _, err := FileHash(dir, "../demo/a.txt"); err == nil {
		t.Fatal("FileHash(escaping path) = nil error, want error")
	}
}

func TestPreview(t *testing.T) {
	dir := setup(t)
	writeSkillFile(t, dir, "demo/SKILL.md", "a\nb\nc\n")
	writeSkillFile(t, dir, "demo/gone.txt", "x\ny\n")

	cs := []Change{
		{Path: "demo/SKILL.md", Content: []byte("a\nB\nc\n")},
		{Path: "demo/new.txt", Content: []byte("one\ntwo\n")},
		{Path: "demo/gone.txt", Delete: true},
		{Path: "demo/same.txt", Content: []byte("same\n")},
	}
	writeSkillFile(t, dir, "demo/same.txt", "same\n")
	got, err := Preview(dir, cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cs) {
		t.Fatalf("Preview returned %d entries, want %d", len(got), len(cs))
	}
	byPath := map[string]FilePreview{}
	for _, p := range got {
		byPath[p.Path] = p
	}
	if p := byPath["demo/SKILL.md"]; p.Op != "modify" {
		t.Fatalf("SKILL.md op = %q, want modify", p.Op)
	} else if !strings.Contains(p.Diff, "-b\n") || !strings.Contains(p.Diff, "+B\n") || !strings.Contains(p.Diff, " a\n") {
		t.Fatalf("modify diff missing lines:\n%s", p.Diff)
	}
	if p := byPath["demo/new.txt"]; p.Op != "create" {
		t.Fatalf("new.txt op = %q, want create", p.Op)
	} else if !strings.HasPrefix(p.Diff, "@@ -0,0 +1,2 @@\n+one\n+two\n") {
		t.Fatalf("create diff:\n%s", p.Diff)
	}
	if p := byPath["demo/gone.txt"]; p.Op != "delete" {
		t.Fatalf("gone.txt op = %q, want delete", p.Op)
	} else if p.Diff != "@@ -1,2 +0,0 @@\n-x\n-y\n" {
		t.Fatalf("delete diff:\n%s", p.Diff)
	}
	if p := byPath["demo/same.txt"]; p.Op != "unchanged" || p.Diff != "" {
		t.Fatalf("same.txt = %+v, want unchanged with empty diff", p)
	}

	writeSkillFile(t, dir, "demo/nl.txt", "a\n")
	nl, err := Preview(dir, []Change{{Path: "demo/nl.txt", Content: []byte("a")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nl) != 1 || nl[0].Op != "modify" {
		t.Fatalf("newline-only change = %+v, want one modify", nl)
	}
	if nl[0].Diff != "(only the newline at the end of the file differs)\n" {
		t.Fatalf("newline-only diff = %q", nl[0].Diff)
	}

	if _, err := Preview(dir, []Change{{Path: "demo/missing.txt", Delete: true}}); err == nil {
		t.Fatal("deleting a missing file must fail")
	}
	if _, err := Preview(dir, []Change{{Path: "../x/y", Content: []byte("x")}}); err == nil {
		t.Fatal("Preview must validate the changes")
	}
	if _, err := Preview(dir, nil); err == nil {
		t.Fatal("Preview must reject an empty change list")
	}
}

func TestPreviewRefusesSymlinkedParent(t *testing.T) {
	dir := setup(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(dir, []Change{{Path: "demo/x.txt", Content: []byte("x")}}); err == nil {
		t.Fatal("Preview must refuse a symlinked parent directory")
	}
}

func TestApplyHappyPath(t *testing.T) {
	dir := setup(t)
	base := map[string]string{}
	cs := []Change{{Path: "demo/SKILL.md", Content: []byte("# Demo\n")}}
	if err := Apply(dir, cs, base); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, "demo", "SKILL.md")
	b, err := os.ReadFile(full)
	if err != nil || string(b) != "# Demo\n" {
		t.Fatalf("content = %q, %v", b, err)
	}
	fi, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("new file mode = %o, want 644", fi.Mode().Perm())
	}
	base["demo/SKILL.md"] = mustHash(t, dir, "demo/SKILL.md")

	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatal(err)
	}
	cs = []Change{{Path: "demo/SKILL.md", Content: []byte("# Demo\n\nChanged.\n")}}
	if err := Apply(dir, cs, base); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(full); string(b) != "# Demo\n\nChanged.\n" {
		t.Fatalf("modified content = %q", b)
	}
	fi, err = os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("overwritten file mode = %o, want 600", fi.Mode().Perm())
	}

	if err := Apply(dir, []Change{{Path: "demo/SKILL.md", Delete: true}}, map[string]string{"demo/SKILL.md": mustHash(t, dir, "demo/SKILL.md")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestApplyDeletePrunesEmptyDirectories(t *testing.T) {
	dir := setup(t)
	writeSkillFile(t, dir, "demo/deep/nested/x.txt", "x")
	writeSkillFile(t, dir, "keep/keep.txt", "k")
	hash := mustHash(t, dir, "demo/deep/nested/x.txt")
	if err := Apply(dir, []Change{{Path: "demo/deep/nested/x.txt", Delete: true}},
		map[string]string{"demo/deep/nested/x.txt": hash}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("demo folder was not pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep", "keep.txt")); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}

func TestApplyBaseConflictChangesNothing(t *testing.T) {
	dir := setup(t)
	writeSkillFile(t, dir, "demo/a.txt", "one")
	hash := mustHash(t, dir, "demo/a.txt")

	if err := Apply(dir, []Change{{Path: "demo/a.txt", Content: []byte("two")}}, map[string]string{"demo/a.txt": "deadbeef"}); err == nil {
		t.Fatal("wrong base hash must fail")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); string(b) != "one" {
		t.Fatalf("file changed on conflict: %q", b)
	}

	if err := Apply(dir, []Change{{Path: "demo/a.txt", Content: []byte("two")}}, map[string]string{}); err == nil {
		t.Fatal("existing file without a base key must fail")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); string(b) != "one" {
		t.Fatalf("file changed without base key: %q", b)
	}

	// One conflicting change must prevent all other changes.
	conflict := []Change{
		{Path: "demo/b.txt", Content: []byte("new")},
		{Path: "demo/a.txt", Content: []byte("two")},
	}
	if err := Apply(dir, conflict, map[string]string{"demo/a.txt": hash, "demo/b.txt": hash}); err == nil {
		t.Fatal("conflict must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "demo", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt was written despite the conflict: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); string(b) != "one" {
		t.Fatalf("a.txt changed despite the conflict: %q", b)
	}
}

func TestApplyRefusesSymlinkedParent(t *testing.T) {
	dir := setup(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "demo")); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, []Change{{Path: "demo/x.txt", Content: []byte("x")}}, map[string]string{}); err == nil {
		t.Fatal("Apply must refuse a symlinked parent directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was written through the symlink: %v", err)
	}
}

func TestApplyRefusesSymlinkTarget(t *testing.T) {
	dir := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "demo", "x.txt")); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, []Change{{Path: "demo/x.txt", Content: []byte("new")}}, map[string]string{}); err == nil {
		t.Fatal("Apply must refuse a symlink target")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatalf("symlink target was modified: %q", b)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "demo", "x.txt")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced: %v", err)
	}
}

func TestApplyRefusesNonRegularTarget(t *testing.T) {
	dir := setup(t)
	if err := os.MkdirAll(filepath.Join(dir, "demo", "x.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, []Change{{Path: "demo/x.txt", Content: []byte("x")}}, map[string]string{}); err == nil {
		t.Fatal("Apply must refuse a directory target")
	}
	if err := Apply(dir, []Change{{Path: "demo/x.txt", Delete: true}}, map[string]string{}); err == nil {
		t.Fatal("Apply must refuse deleting a directory")
	}
}

func TestApplyDeleteMissingFails(t *testing.T) {
	dir := setup(t)
	if err := Apply(dir, []Change{{Path: "demo/missing.txt", Delete: true}}, map[string]string{}); err == nil {
		t.Fatal("deleting a missing file must fail")
	}
}

func TestApplyEscapes(t *testing.T) {
	dir := setup(t)
	outside := filepath.Dir(dir)
	for _, p := range []string{"../evil.txt", "/etc/passwd", "demo/../../evil.txt", `demo\evil.txt`} {
		if err := Apply(dir, []Change{{Path: p, Content: []byte("x")}}, map[string]string{}); err == nil {
			t.Errorf("Apply(%q) = nil, want error", p)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was written outside the skills folder: %v", err)
	}
}
