package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPermsAndRoundtrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bf")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "bergfried.db")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("db perm %v", fi.Mode().Perm())
	}
	if err := s.AddSession(Session{Name: "a", VM: "bf-a", TemplateVersion: 1, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSession(Session{Name: "a", VM: "bf-a2", TemplateVersion: 1, Created: time.Now()}); err == nil {
		t.Fatal("Duplikat muss scheitern")
	}
	if _, err := s.GetSession("nix"); err == nil {
		t.Fatal("fehlende Session muss Fehler liefern")
	}
	_ = s.Audit("cli", "session.create", "a", "", "x")
	l, _ := s.AuditList(10)
	if len(l) != 1 || l[0].Event != "session.create" {
		t.Fatalf("%v", l)
	}
	_ = s.AddAgent(Agent{Session: "a", Name: "main", Worktree: "/w", ComposeProject: "a-main", Created: time.Now()})
	_ = s.DeleteSession("a")
	if ag, _ := s.ListAgents("a"); len(ag) != 0 {
		t.Fatal("Agents müssen mitgelöscht werden")
	}
}
