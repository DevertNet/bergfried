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

func TestSeededAndDeleteAgent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddSession(Session{Name: "a", VM: "bergfried-a", TemplateVersion: 1, Created: time.Now()})
	if s.Seeded("a") {
		t.Fatal("neue Session ist zunächst nicht markiert")
	}
	if !s.Seeded("unbekannt") {
		t.Fatal("unbekannte Session: nie automatisch anlegen")
	}
	_ = s.SetSeeded("a")
	if !s.Seeded("a") {
		t.Fatal("markiert")
	}
	_ = s.AddAgent(Agent{Session: "a", Name: "x", Worktree: "/w", ComposeProject: "a-x", Created: time.Now()})
	_ = s.DeleteAgent("a", "x")
	if ag, _ := s.ListAgents("a"); len(ag) != 0 {
		t.Fatal("Agent muss weg sein")
	}
}

func TestDeleteSessionRemovesPushesAndCIGrants(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddSession(Session{Name: "a", VM: "bergfried-a", TemplateVersion: 1, Created: time.Now()})
	_ = s.AddPush(GitPush{Session: "a", Agent: "main", Repo: "o/r", Branch: "agent/x", SHA: "abc"})
	_ = s.SetCIGrant("a", "o/r", time.Now().Add(time.Hour))
	if !s.HasPushed("a", "o/r", "agent/x") || !s.HasCIGrant("a", "o/r", time.Now()) {
		t.Fatal("Vorbedingung")
	}
	if err := s.DeleteSession("a"); err != nil {
		t.Fatal(err)
	}
	if s.HasPushed("a", "o/r", "agent/x") || s.HasCIGrant("a", "o/r", time.Now()) {
		t.Fatal("nach dem Löschen darf kein CI-Zugriff erhalten bleiben")
	}
}
