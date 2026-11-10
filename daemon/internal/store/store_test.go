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
		t.Fatal("a duplicate must fail")
	}
	if _, err := s.GetSandbox("nix"); err == nil {
		t.Fatal("a missing sandbox must return an error")
	}
	_ = s.Audit("cli", "sandbox.create", "a", "", "x")
	l, _ := s.AuditList(10)
	if len(l) != 1 || l[0].Event != "sandbox.create" {
		t.Fatalf("%v", l)
	}
	if err := s.SetOpenCode("a", true); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetSandbox("a"); !x.OpenCode {
		t.Fatal("OpenCode state not stored")
	}
	_ = s.AddWorktree(Worktree{Sandbox: "a", Name: "main", Path: "/w", ComposeProject: "a-main", Created: time.Now()})
	_ = s.DeleteSandbox("a")
	if wt, _ := s.ListWorktrees("a"); len(wt) != 0 {
		t.Fatal("worktrees must be deleted too")
	}
}

func TestSeededAndDeleteWorktree(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddSandbox(Sandbox{Name: "a", VM: "bergfried-a", TemplateVersion: 1, Created: time.Now()})
	if s.Seeded("a") {
		t.Fatal("a new sandbox is not marked at first")
	}
	if !s.Seeded("unbekannt") {
		t.Fatal("unbekannte Sandbox: nie automatisch anlegen")
	}
	_ = s.SetSeeded("a")
	if !s.Seeded("a") {
		t.Fatal("markiert")
	}
	_ = s.AddWorktree(Worktree{Sandbox: "a", Name: "x", Path: "/w", ComposeProject: "a-x", Created: time.Now()})
	_ = s.DeleteWorktree("a", "x")
	if wt, _ := s.ListWorktrees("a"); len(wt) != 0 {
		t.Fatal("worktree must be gone")
	}
}

func TestMigrateLegacySessionNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild an old DB: tables and columns with the former names.
	for _, q := range []string{
		`DROP TABLE sandboxes`, `DROP TABLE worktrees`, `DROP TABLE requests`, `DROP TABLE ssh_grants`, `DROP TABLE audit`,
		`CREATE TABLE sessions(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL, seeded INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE agents(session TEXT NOT NULL, name TEXT NOT NULL, worktree TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, runtime TEXT NOT NULL DEFAULT 'claude', PRIMARY KEY(session,name))`,
		`CREATE TABLE requests(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, kind TEXT NOT NULL, path TEXT NOT NULL, reason TEXT, status TEXT NOT NULL, created INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0, extra TEXT)`,
		`CREATE TABLE ssh_grants(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, host TEXT NOT NULL, key_fp TEXT NOT NULL, user TEXT NOT NULL, hostkeys TEXT NOT NULL, expires INTEGER NOT NULL, per_conn INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL)`,
		`CREATE TABLE audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT NOT NULL, event TEXT NOT NULL, session TEXT, agent TEXT, detail TEXT)`,
		`INSERT INTO sessions(name,vm,repo,template_version,created) VALUES('alt','bergfried-alt','',1,1)`,
		`INSERT INTO agents(session,name,worktree,compose_project,created,runtime) VALUES('alt','main','~/work/main','alt-main',1,'claude')`,
		`INSERT INTO agents(session,name,worktree,compose_project,created,runtime) VALUES('alt','oc','~/work/oc','alt-oc',1,'opencode')`,
		`INSERT INTO audit(ts,actor,event,session,agent,detail) VALUES(1,'app','agent.add','alt','main','')`,
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
	wt, _ := s.ListWorktrees("alt")
	if len(wt) != 2 {
		t.Fatalf("worktrees after migration: %v", wt)
	}
	byName := map[string]Worktree{}
	for _, w := range wt {
		byName[w.Name] = w
	}
	if !byName["main"].Claude || byName["oc"].Claude {
		t.Fatalf("runtime mapping wrong: %+v", wt)
	}
	au, _ := s.AuditSearch(10, "worktree.add")
	if len(au) != 1 || au[0].Sandbox != "alt" || au[0].Worktree != "main" {
		t.Fatalf("audit after migration: %+v", au)
	}
	if _, err := s.AddRequest(Request{Sandbox: "alt", Worktree: "main", Kind: "file", Path: "x", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeClaudeAndOpenCodePort(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "r"))
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddSandbox(Sandbox{Name: "a", VM: "bergfried-a", TemplateVersion: 1, Created: time.Now(), OpenCodePort: 42123})
	x, err := s.GetSandbox("a")
	if err != nil || x.OpenCodePort != 42123 {
		t.Fatalf("%+v %v", x, err)
	}
	if err := s.SetOpenCodePort("a", 42456); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetSandbox("a"); x.OpenCodePort != 42456 {
		t.Fatalf("port not updated: %+v", x)
	}
	if err := s.SetOpenCode("a", true); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetSandbox("a"); !x.OpenCode {
		t.Fatal("OpenCode state not stored")
	}
	_ = s.AddWorktree(Worktree{Sandbox: "a", Name: "main", Path: "/w", ComposeProject: "a-main", Created: time.Now()})
	_ = s.AddWorktree(Worktree{Sandbox: "a", Name: "oc", Path: "/o", ComposeProject: "a-oc", Created: time.Now()})
	wt, _ := s.ListWorktrees("a")
	if len(wt) != 2 || wt[0].Claude || wt[1].Claude {
		t.Fatalf("%+v", wt)
	}
	if err := s.SetWorktreeClaude("a", "main", true); err != nil {
		t.Fatal(err)
	}
	wt, _ = s.ListWorktrees("a")
	if len(wt) != 2 || !wt[0].Claude || wt[1].Claude {
		t.Fatalf("%+v", wt)
	}
}

func TestMigrateAddsClaudeAndPort(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// An old database without the claude/opencode_port columns.
	for _, q := range []string{
		`DROP TABLE sandboxes`, `DROP TABLE worktrees`,
		`CREATE TABLE sandboxes(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL, seeded INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE agents(sandbox TEXT NOT NULL, name TEXT NOT NULL, worktree TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, runtime TEXT NOT NULL DEFAULT 'claude', PRIMARY KEY(sandbox,name))`,
		`INSERT INTO sandboxes(name,vm,repo,template_version,created,seeded) VALUES('alt','bergfried-alt','',1,1,0)`,
		`INSERT INTO agents(sandbox,name,worktree,compose_project,created,runtime) VALUES('alt','main','/w','alt-main',1,'claude')`,
		`INSERT INTO agents(sandbox,name,worktree,compose_project,created,runtime) VALUES('alt','oc','/o','alt-oc',1,'opencode')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	x, err := s2.GetSandbox("alt")
	if err != nil || x.OpenCodePort != 0 {
		t.Fatalf("%+v %v", x, err)
	}
	wt, _ := s2.ListWorktrees("alt")
	if len(wt) != 2 {
		t.Fatalf("old worktrees: %+v", wt)
	}
	byName := map[string]Worktree{}
	for _, w := range wt {
		byName[w.Name] = w
	}
	if !byName["main"].Claude || byName["oc"].Claude {
		t.Fatalf("old runtime must map to claude: %+v", wt)
	}
	_ = s2.SetOpenCodePort("alt", 42123)
	if err := s2.SetOpenCode("alt", true); err != nil {
		t.Fatal(err)
	}
	if x, _ := s2.GetSandbox("alt"); x.OpenCodePort != 42123 || !x.OpenCode {
		t.Fatal("column missing")
	}
}
