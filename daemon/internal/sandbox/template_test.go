package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
)

const shipped = "../../../vm/bergfried.yaml"

func shippedYAML(t *testing.T) string {
	b, err := os.ReadFile(shipped)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestShippedTemplateIsValid(t *testing.T) {
	if _, err := CheckTemplate(shippedYAML(t)); err != nil {
		t.Fatal(err)
	}
}

func TestIsolationRulesEnforced(t *testing.T) {
	ok := shippedYAML(t)
	bad := map[string]string{
		"Home-Mount":        strings.Replace(ok, "mounts: []", "mounts:\n- location: \"~\"\n  writable: true", 1),
		"Default-Mounts":    strings.Replace(ok, "- template:_images/debian-13", "- template:_images/debian-13\n- template:_default/mounts", 1),
		"Docker-Basis":      strings.Replace(ok, "- template:_images/debian-13", "- template:docker", 1),
		"forwardAgent":      strings.Replace(ok, "forwardAgent: false", "forwardAgent: true", 1),
		"Docker-Socket":     strings.Replace(ok, "probes:", "- guestSocket: \"/var/run/docker.sock\"\n  hostSocket: \"{{.Dir}}/sock/docker.sock\"\nprobes:", 1),
		"Daemon-Socket weg": strings.Replace(ok, "/run/user/{{.UID}}/bergfried.sock", "/run/user/{{.UID}}/anders.sock", 1),
		"Agent-Socket weg":  strings.Replace(ok, "bergfried-agent.sock\"", "x.sock\"", 1),
		"kein YAML":         "a: [",
		"leer":              "",
	}
	for name, y := range bad {
		if _, err := CheckTemplate(y); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if _, err := CheckTemplate(strings.Repeat("a", MaxTemplateBytes+1)); err == nil {
		t.Fatal("too large")
	}
}

type okRunner struct{ calls [][]string }

func (r *okRunner) Run(_ context.Context, a ...string) ([]byte, error) {
	r.calls = append(r.calls, a)
	return nil, nil
}

func TestStoreRevisionAndReset(t *testing.T) {
	dir := t.TempDir()
	r := &okRunner{}
	ts := TemplateStore{Dir: filepath.Join(dir, "template"), Seed: shipped, L: lima.Client{R: r}}
	if err := ts.Ensure(); err != nil {
		t.Fatal(err)
	}
	if ts.Rev() != TemplateVersion {
		t.Fatal("Start-Revision")
	}
	y, _ := ts.Read()
	if _, err := ts.Save(context.Background(), y); err != nil || ts.Rev() != TemplateVersion {
		t.Fatalf("unchanged must not increase the revision: %v %d", err, ts.Rev())
	}
	if _, err := ts.Save(context.Background(), y+"\n# change\n"); err != nil || ts.Rev() != TemplateVersion+1 {
		t.Fatalf("%v %d", err, ts.Rev())
	}
	if len(r.calls) != 1 || r.calls[0][0] != "validate" {
		t.Fatalf("limactl validate must run: %v", r.calls)
	}
	// limactl requires file names matching ^[A-Za-z0-9]+(?:[._-](?:[A-Za-z0-9]+))*$ (no leading dot).
	if !regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-](?:[A-Za-z0-9]+))*\.yaml$`).MatchString(filepath.Base(r.calls[0][1])) {
		t.Fatalf("file name invalid for limactl: %s", r.calls[0][1])
	}
	bad := strings.Replace(y, "mounts: []", "mounts:\n- location: \"/\"", 1)
	if _, err := ts.Save(context.Background(), bad); err == nil || ts.Rev() != TemplateVersion+1 {
		t.Fatal("an unsafe template must not be saved")
	}
	if cur, _ := ts.Read(); strings.Contains(cur, "location") {
		t.Fatal("file must not have been overwritten")
	}
	if err := ts.Reset(); err != nil || ts.Rev() != TemplateVersion+2 {
		t.Fatalf("%v %d", err, ts.Rev())
	}
	if fi, _ := os.Stat(ts.Path()); fi.Mode().Perm() != 0o600 {
		t.Fatal("Rechte")
	}
}

func TestManagerBaseNameFollowsContent(t *testing.T) {
	ts := TemplateStore{Dir: t.TempDir(), Seed: shipped, L: lima.Client{R: &okRunner{}}}
	_ = ts.Ensure()
	m := &Manager{Tpl: &ts}
	first := m.BaseName()
	if !strings.HasPrefix(first, "bergfried-base-") || len(first) != len("bergfried-base-")+8 {
		t.Fatal(first)
	}
	_, _ = ts.Save(context.Background(), shippedYAML(t)+"\n#x\n")
	changed := m.BaseName()
	if changed == first {
		t.Fatal("changed content needs a new base")
	}
	if err := ts.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.BaseName() != first {
		t.Fatal("restored content must reuse the existing base")
	}
}
