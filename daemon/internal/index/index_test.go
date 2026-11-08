package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mk(t *testing.T, home string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func setup(t *testing.T) (string, *Index) {
	home := t.TempDir()
	mk(t, home, "Documents/a.txt", "Documents/sub/b.txt", ".ssh/id_rsa", "Library/Keychains/login.keychain-db",
		"proj/.env", "proj/.env.local", "proj/main.go", ".config/gh/hosts.yml", ".mcp-einfach123-secrets/k", "x/id_ed25519")
	ix, err := Build(home, DefaultBlocklist())
	if err != nil {
		t.Fatal(err)
	}
	return home, ix
}

func TestBlockedNeverInIndex(t *testing.T) {
	_, ix := setup(t)
	for _, e := range ix.entries {
		l := strings.ToLower(e.Path)
		for _, bad := range []string{".ssh", "library", ".config", ".mcp-einfach123-secrets", ".env", "id_rsa", "id_ed25519", "keychain"} {
			if strings.Contains(l, bad) {
				t.Fatalf("gesperrter Eintrag im Index: %s", e.Path)
			}
		}
	}
	if es, _ := ix.Search("main"); len(es) != 1 {
		t.Fatal("proj/main.go muss auffindbar sein")
	}
	if es, _ := ix.Search("keychain"); len(es) != 0 {
		t.Fatal("Keychain darf nicht suchbar sein")
	}
}

func TestListAndTraversal(t *testing.T) {
	_, ix := setup(t)
	es, _, err := ix.List("Documents", "")
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	if _, _, err := ix.List(".ssh", ""); !errors.Is(err, ErrBlocked) {
		t.Fatal("List auf gesperrten Pfad muss ErrBlocked liefern")
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "~/x", "a/../../b", "Documents/../../x", "a\x00b"} {
		if _, _, err := ix.Resolve(bad); err == nil {
			t.Fatalf("%q muss abgelehnt werden", bad)
		}
	}
}

func TestResolveBlockedVariants(t *testing.T) {
	_, ix := setup(t)
	for _, p := range []string{".ssh/id_rsa", ".SSH/id_rsa", "library/Keychains/login.keychain-db", "LIBRARY", "proj/.env", "proj/.ENV.local", ".config/gh/hosts.yml", "./.ssh/id_rsa", "Documents/../.ssh/id_rsa"} {
		if _, _, err := ix.Resolve(p); err == nil {
			t.Fatalf("%q muss abgelehnt werden", p)
		}
	}
	if _, _, err := ix.Resolve("Documents/a.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkEscapes(t *testing.T) {
	home, ix := setup(t)
	outside := t.TempDir()
	mk(t, outside, "secret.txt")
	_ = os.Symlink(outside, filepath.Join(home, "Documents", "out"))
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "Documents", "sshlink"))
	_ = os.Symlink(filepath.Join(home, ".ssh", "id_rsa"), filepath.Join(home, "Documents", "keylink"))
	_ = ix.Refresh()
	for _, p := range []string{"Documents/out/secret.txt", "Documents/sshlink/id_rsa", "Documents/keylink"} {
		if _, _, err := ix.Resolve(p); err == nil {
			t.Fatalf("Symlink-Ausbruch %q muss scheitern", p)
		}
	}
	if es, _ := ix.Search("link"); len(es) != 0 {
		t.Fatal("Symlinks dürfen nicht im Index stehen")
	}
}

func TestBlocklistRoundtrip(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bl.json")
	bl := DefaultBlocklist()
	bl.Paths = append(bl.Paths, "Private")
	if err := bl.Save(f); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBlocklist(f)
	if err != nil || !got.BlockedRel("Private/x") {
		t.Fatal("eigener Eintrag fehlt")
	}
	if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
		t.Fatal("Rechte")
	}
}
