package importer

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/index"
)

func tree(t *testing.T) (string, Importer) {
	home := t.TempDir()
	w := func(p, c string) {
		fp := filepath.Join(home, p)
		_ = os.MkdirAll(filepath.Dir(fp), 0o755)
		if err := os.WriteFile(fp, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("proj/main.go", "package main")
	w("proj/sub/a.txt", "a")
	w("proj/.env", "SECRET=1")
	w("proj/.env.local", "SECRET=2")
	w("proj/id_rsa", "KEY")
	w("proj/node_modules/x/y.js", "x")
	w("proj/.git/HEAD", "ref: refs/heads/main")
	w("proj/.git/config", "[remote \"origin\"]\n\turl = https://user:ghp_TOKEN123@github.com/x/y.git\n")
	w(".ssh/id_ed25519", "KEY")
	w("proj/keys/secret.txt", "s")
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "proj", "sshlink"))
	_ = os.Symlink("/etc", filepath.Join(home, "proj", "etclink"))
	bl := index.DefaultBlocklist()
	bl.Paths = append(bl.Paths, "proj/keys")
	return home, Importer{Home: home, BL: func() index.Blocklist { return bl }}
}

func names(t *testing.T, b []byte) map[string]string {
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		c, _ := io.ReadAll(tr)
		out[h.Name] = string(c)
	}
	return out
}

func TestImportSkipsSecretsAndNoise(t *testing.T) {
	home, im := tree(t)
	var buf bytes.Buffer
	files, _, err := im.Stream(filepath.Join(home, "proj"), &buf)
	if err != nil {
		t.Fatal(err)
	}
	n := names(t, buf.Bytes())
	for _, want := range []string{"main.go", "sub/a.txt", ".git/HEAD", ".git/config"} {
		if _, ok := n[want]; !ok {
			t.Fatalf("%s fehlt: %v", want, n)
		}
	}
	for _, bad := range []string{".env", ".env.local", "id_rsa", "node_modules/x/y.js", "sshlink", "etclink", "keys/secret.txt"} {
		if _, ok := n[bad]; ok {
			t.Fatalf("%s must not be copied", bad)
		}
	}
	for k := range n {
		if strings.Contains(k, "..") || strings.HasPrefix(k, "/") {
			t.Fatalf("unsicherer Name %q", k)
		}
	}
	if files != 4 {
		t.Fatalf("files=%d", files)
	}
	cfg := n[".git/config"]
	if strings.Contains(cfg, "ghp_TOKEN123") || !strings.Contains(cfg, "https://github.com/x/y.git") {
		t.Fatalf("credentials must be removed from .git/config: %q", cfg)
	}
}

func TestPreviewMatchesStream(t *testing.T) {
	home, im := tree(t)
	f, b, err := im.Preview(filepath.Join(home, "proj"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	sf, sb, _ := im.Stream(filepath.Join(home, "proj"), &buf)
	if f != sf || b != sb {
		t.Fatalf("Vorschau %d/%d != Strom %d/%d", f, b, sf, sb)
	}
}

func TestRejectedPaths(t *testing.T) {
	home, im := tree(t)
	outside := t.TempDir()
	link := filepath.Join(home, "proj", "outlink")
	_ = os.Symlink(outside, link)
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "sshdir"))
	for name, p := range map[string]string{
		"relativ":            "proj",
		"Home selbst":        home,
		"außerhalb":          outside,
		"gesperrt":           filepath.Join(home, ".ssh"),
		"symlink to .ssh":    filepath.Join(home, "sshdir"),
		"symlink outside":    link,
		"Sperrliste-Eintrag": filepath.Join(home, "proj", "keys"),
		"file":               filepath.Join(home, "proj", "main.go"),
		"fehlt":              filepath.Join(home, "gibtsnicht"),
		"Elternpfad ..":      filepath.Join(home, "proj", "..", ".."),
	} {
		if _, _, err := im.Preview(p); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
		if _, _, err := im.Stream(p, io.Discard); err == nil {
			t.Fatalf("%s (Stream) must be rejected", name)
		}
	}
}
