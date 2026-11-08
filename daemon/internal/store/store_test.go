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
	if err := s.AddSandbox(Sandbox{Name: "a", VM: "bf-a", TemplateVersion: 1, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSandbox(Sandbox{Name: "a", VM: "bf-a2", TemplateVersion: 1, Created: time.Now()}); err == nil {
		t.Fatal("Duplikat muss scheitern")
	}
	if _, err := s.GetSandbox("nix"); err == nil {
		t.Fatal("fehlende Sandbox muss Fehler liefern")
	}
	_ = s.Audit("cli", "sandbox.create", "a", "", "x")
	l, _ := s.AuditList(10)
	if len(l) != 1 || l[0].Event != "sandbox.create" {
		t.Fatalf("%v", l)
	}
	_ = s.AddAgent(Agent{Sandbox: "a", Name: "main", Worktree: "/w", ComposeProject: "a-main", Created: time.Now()})
	_ = s.DeleteSandbox("a")
	if ag, _ := s.ListAgents("a"); len(ag) != 0 {
		t.Fatal("Agents müssen mitgelöscht werden")
	}
}

func TestSeededAndDeleteAgent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddSandbox(Sandbox{Name: "a", VM: "bergfried-a", TemplateVersion: 1, Created: time.Now()})
	if s.Seeded("a") {
		t.Fatal("neue Sandbox ist zunächst nicht markiert")
	}
	if !s.Seeded("unbekannt") {
		t.Fatal("unbekannte Sandbox: nie automatisch anlegen")
	}
	_ = s.SetSeeded("a")
	if !s.Seeded("a") {
		t.Fatal("markiert")
	}
	_ = s.AddAgent(Agent{Sandbox: "a", Name: "x", Worktree: "/w", ComposeProject: "a-x", Created: time.Now()})
	_ = s.DeleteAgent("a", "x")
	if ag, _ := s.ListAgents("a"); len(ag) != 0 {
		t.Fatal("Agent muss weg sein")
	}
}

func TestMigrateLegacySessionNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Alte DB nachbauen: Tabellen und Spalten mit dem früheren Namen.
	for _, q := range []string{
		`DROP TABLE sandboxes`, `DROP TABLE agents`, `DROP TABLE requests`, `DROP TABLE ssh_grants`, `DROP TABLE audit`,
		`CREATE TABLE sessions(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL, seeded INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE agents(session TEXT NOT NULL, name TEXT NOT NULL, worktree TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(session,name))`,
		`CREATE TABLE requests(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, kind TEXT NOT NULL, path TEXT NOT NULL, reason TEXT, status TEXT NOT NULL, created INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0, extra TEXT)`,
		`CREATE TABLE ssh_grants(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, host TEXT NOT NULL, key_fp TEXT NOT NULL, user TEXT NOT NULL, hostkeys TEXT NOT NULL, expires INTEGER NOT NULL, per_conn INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL)`,
		`CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT NOT NULL, event TEXT NOT NULL, session TEXT, agent TEXT, detail TEXT)`,
		`INSERT INTO sessions(name,vm,repo,template_version,created) VALUES('alt','bergfried-alt','',1,1)`,
		`INSERT INTO agents(session,name,worktree,compose_project,created) VALUES('alt','main','~/work/main','alt-main',1)`,
		`INSERT INTO audit(ts,actor,event,session,agent,detail) VALUES(1,'app','session.create','alt','','')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	s.DB.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if x, err := s.GetSandbox("alt"); err != nil || x.VM != "bergfried-alt" {
		t.Fatal(x, err)
	}
	if ag, _ := s.ListAgents("alt"); len(ag) != 1 {
		t.Fatalf("Agents nach Migration: %v", ag)
	}
	au, _ := s.AuditSearch(10, "sandbox.create")
	if len(au) != 1 || au[0].Sandbox != "alt" {
		t.Fatalf("Audit nach Migration: %+v", au)
	}
	if _, err := s.AddRequest(Request{Sandbox: "alt", Kind: "file", Path: "x", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
}
