package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogPathSources(t *testing.T) {
	s := &Server{Home: "/h"}
	if p, err := s.logPath("", ""); err != nil || p != "/h/daemon.log" {
		t.Fatalf("daemon: %q %v", p, err)
	}
	if p, _ := s.logPath("update", ""); p != "/h/update/update.log" {
		t.Fatalf("update: %q", p)
	}
	t.Setenv("LIMA_HOME", "/lima")
	if p, err := s.logPath("lima", "bergfried-shop"); err != nil || p != "/lima/bergfried-shop/ha.stderr.log" {
		t.Fatalf("lima: %q %v", p, err)
	}
	for _, vm := range []string{"", "../etc", "bergfried-../x", "other", "bergfried-a/b"} {
		if _, err := s.logPath("lima", vm); err == nil {
			t.Errorf("VM name %q must be refused", vm)
		}
	}
	if _, err := s.logPath("passwd", ""); err == nil {
		t.Error("unknown source must be refused")
	}
}

func TestReadTailCutsAtLineBoundary(t *testing.T) {
	f := filepath.Join(t.TempDir(), "l.log")
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString("line of the log\n")
	}
	if err := os.WriteFile(f, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	txt, err := readTail(f, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(txt) > 100 || !strings.HasPrefix(txt, "line of the log") {
		t.Fatalf("tail %q", txt)
	}
	small, _ := readTail(f, 1<<20)
	if small != b.String() {
		t.Fatal("small file must be returned whole")
	}
}
