// Package store keeps sandboxes, worktrees and the audit log in SQLite (pure Go, no cgo).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }

type Sandbox struct {
	Name, VM, Repo  string
	TemplateVersion int
	OpenCodePort    int  // stable local port for the OpenCode desktop connection (0 = not assigned)
	OpenCode        bool // the sandbox-level OpenCode service should run
	Created         time.Time
}

// Worktree is one code directory. Claude can run in it (Claude=true), OpenCode can use it as a
// project; the worktree itself has no runtime.
type Worktree struct {
	Sandbox, Name, Path, ComposeProject string
	Claude                              bool
	Created                             time.Time
}

type AuditEntry struct {
	TS                                      time.Time
	Actor, Event, Sandbox, Worktree, Detail string
}

// Open creates the data directory with 0700 and the DB with 0600.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "bergfried.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	return s, s.migrate()
}

func columns(s *Store, table string) map[string]bool {
	out := map[string]bool{}
	rows, err := s.DB.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			out[c] = true
		}
	}
	return out
}

func hasTable(s *Store, table string) bool {
	var n int
	return s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n) == nil && n > 0
}

// renameLegacy moves the pre-"worktree" names to the current schema and keeps existing data.
func (s *Store) renameLegacy() {
	if hasTable(s, "sessions") && !hasTable(s, "sandboxes") {
		_, _ = s.DB.Exec(`ALTER TABLE sessions RENAME TO sandboxes`)
	}
	// requests/ssh_grants/audit: column session -> sandbox (older) and agent -> worktree.
	for _, t := range []string{"requests", "ssh_grants", "audit"} {
		cols := columns(s, t)
		if cols["session"] && !cols["sandbox"] {
			_, _ = s.DB.Exec(`ALTER TABLE ` + t + ` RENAME COLUMN session TO sandbox`)
		}
		if cols["agent"] && !cols["worktree"] {
			_, _ = s.DB.Exec(`ALTER TABLE ` + t + ` RENAME COLUMN agent TO worktree`)
		}
	}
	// agents -> worktrees; worktree column -> path; drop the old runtime column.
	if hasTable(s, "agents") {
		if c := columns(s, "agents"); c["session"] && !c["sandbox"] {
			_, _ = s.DB.Exec(`ALTER TABLE agents RENAME COLUMN session TO sandbox`)
		}
	}
	if hasTable(s, "agents") {
		if !hasTable(s, "worktrees") {
			_, _ = s.DB.Exec(`ALTER TABLE agents RENAME TO worktrees`)
		} else {
			// Both exist: copy missing rows, then drop the old table.
			_, _ = s.DB.Exec(`INSERT OR IGNORE INTO worktrees(sandbox,name,path,compose_project,created,claude)
				SELECT sandbox,name,worktree,compose_project,created,CASE WHEN runtime='opencode' THEN 0 ELSE 1 END FROM agents`)
			_, _ = s.DB.Exec(`DROP TABLE agents`)
		}
	}
	if hasTable(s, "worktrees") {
		cols := columns(s, "worktrees")
		if cols["worktree"] && !cols["path"] {
			_, _ = s.DB.Exec(`ALTER TABLE worktrees RENAME COLUMN worktree TO path`)
		}
		if !cols["claude"] {
			_, _ = s.DB.Exec(`ALTER TABLE worktrees ADD COLUMN claude INTEGER NOT NULL DEFAULT 1`)
		}
		if cols["runtime"] {
			_, _ = s.DB.Exec(`UPDATE worktrees SET claude = CASE WHEN runtime='opencode' THEN 0 ELSE 1 END`)
			// SQLite >= 3.35 supports DROP COLUMN; ignore if this build does not.
			_, _ = s.DB.Exec(`ALTER TABLE worktrees DROP COLUMN runtime`)
		}
	}
	_, _ = s.DB.Exec(`UPDATE audit SET event='worktree.'||substr(event,7) WHERE event LIKE 'agent.%'`)
	_, _ = s.DB.Exec(`UPDATE audit SET event='claude.'||substr(event,10) WHERE event LIKE 'worktree.%'`)
}

func (s *Store) migrate() error {
	s.renameLegacy()
	defer func() {
		// Migration of older DBs; the "duplicate column" error is expected.
		_, _ = s.DB.Exec(`ALTER TABLE requests ADD COLUMN extra TEXT`)
		_, _ = s.DB.Exec(`ALTER TABLE sandboxes ADD COLUMN seeded INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.DB.Exec(`ALTER TABLE sandboxes ADD COLUMN opencode_port INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.DB.Exec(`ALTER TABLE sandboxes ADD COLUMN opencode INTEGER NOT NULL DEFAULT 0`)
		_, _ = s.DB.Exec(`ALTER TABLE helper_stamps ADD COLUMN skills TEXT NOT NULL DEFAULT ''`)
		// Leftovers of the former Mac-push concept (the token now lives in the VM, see docs/GITHUB.md).
		_, _ = s.DB.Exec(`DROP TABLE IF EXISTS git_pushes`)
		_, _ = s.DB.Exec(`DROP TABLE IF EXISTS ci_grants`)
	}()
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS sandboxes(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL, seeded INTEGER NOT NULL DEFAULT 0, opencode_port INTEGER NOT NULL DEFAULT 0, opencode INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS worktrees(sandbox TEXT NOT NULL, name TEXT NOT NULL, path TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, claude INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(sandbox,name));
CREATE TABLE IF NOT EXISTS requests(id INTEGER PRIMARY KEY AUTOINCREMENT, sandbox TEXT NOT NULL, worktree TEXT, kind TEXT NOT NULL, path TEXT NOT NULL, reason TEXT, status TEXT NOT NULL, created INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0, extra TEXT);
CREATE TABLE IF NOT EXISTS ssh_grants(id INTEGER PRIMARY KEY AUTOINCREMENT, sandbox TEXT NOT NULL, worktree TEXT, host TEXT NOT NULL, key_fp TEXT NOT NULL, user TEXT NOT NULL, hostkeys TEXT NOT NULL, expires INTEGER NOT NULL, per_conn INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT NOT NULL, event TEXT NOT NULL, sandbox TEXT, worktree TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS helper_stamps(sandbox TEXT NOT NULL, target TEXT NOT NULL, guidance TEXT NOT NULL, mcp TEXT NOT NULL, skills TEXT NOT NULL DEFAULT '', PRIMARY KEY(sandbox,target));`)
	return err
}

func (s *Store) AddSandbox(x Sandbox) error {
	oc := 0
	if x.OpenCode {
		oc = 1
	}
	_, err := s.DB.Exec(`INSERT INTO sandboxes(name,vm,repo,template_version,created,opencode_port,opencode) VALUES(?,?,?,?,?,?,?)`, x.Name, x.VM, x.Repo, x.TemplateVersion, x.Created.Unix(), x.OpenCodePort, oc)
	return err
}
func (s *Store) GetSandbox(name string) (Sandbox, error) {
	var x Sandbox
	var c int64
	var oc int
	err := s.DB.QueryRow(`SELECT name,vm,repo,template_version,created,opencode_port,opencode FROM sandboxes WHERE name=?`, name).Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c, &x.OpenCodePort, &oc)
	if err == sql.ErrNoRows {
		return x, fmt.Errorf("sandbox %q does not exist", name)
	}
	x.Created, x.OpenCode = time.Unix(c, 0), oc == 1
	return x, err
}
func (s *Store) ListSandboxes() ([]Sandbox, error) {
	rows, err := s.DB.Query(`SELECT name,vm,repo,template_version,created,opencode_port,opencode FROM sandboxes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sandbox
	for rows.Next() {
		var x Sandbox
		var c int64
		var oc int
		if err := rows.Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c, &x.OpenCodePort, &oc); err != nil {
			return nil, err
		}
		x.Created, x.OpenCode = time.Unix(c, 0), oc == 1
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) DeleteSandbox(name string) error {
	if _, err := s.DB.Exec(`DELETE FROM worktrees WHERE sandbox=?`, name); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`DELETE FROM helper_stamps WHERE sandbox=?`, name); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM sandboxes WHERE name=?`, name)
	return err
}
func (s *Store) SetOpenCodePort(name string, port int) error {
	_, err := s.DB.Exec(`UPDATE sandboxes SET opencode_port=? WHERE name=?`, port, name)
	return err
}
func (s *Store) SetOpenCode(name string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	_, err := s.DB.Exec(`UPDATE sandboxes SET opencode=? WHERE name=?`, v, name)
	return err
}
func (s *Store) AddWorktree(w Worktree) error {
	claude := 0
	if w.Claude {
		claude = 1
	}
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO worktrees(sandbox,name,path,compose_project,created,claude) VALUES(?,?,?,?,?,?)`, w.Sandbox, w.Name, w.Path, w.ComposeProject, w.Created.Unix(), claude)
	return err
}
func (s *Store) ListWorktrees(sandbox string) ([]Worktree, error) {
	rows, err := s.DB.Query(`SELECT sandbox,name,path,compose_project,created,claude FROM worktrees WHERE sandbox=? ORDER BY name`, sandbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worktree
	for rows.Next() {
		var w Worktree
		var c int64
		var claude int
		if err := rows.Scan(&w.Sandbox, &w.Name, &w.Path, &w.ComposeProject, &c, &claude); err != nil {
			return nil, err
		}
		w.Created, w.Claude = time.Unix(c, 0), claude == 1
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *Store) SetWorktreeClaude(sandbox, name string, claude bool) error {
	v := 0
	if claude {
		v = 1
	}
	_, err := s.DB.Exec(`UPDATE worktrees SET claude=? WHERE sandbox=? AND name=?`, v, sandbox, name)
	return err
}
func (s *Store) DeleteWorktree(sandbox, name string) error {
	if _, err := s.DB.Exec(`DELETE FROM worktrees WHERE sandbox=? AND name=?`, sandbox, name); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM helper_stamps WHERE sandbox=? AND target=?`, sandbox, "claude:"+name)
	return err
}

// HelperStamp is the hash of the guidance text, of the bergfried-mcp binary and of the shared
// skills folder an assistant was started with (target = "claude:<worktree>" or "opencode").
type HelperStamp struct{ Guidance, MCP, Skills string }

// SetHelperStamp stores (or replaces) the stamp of a target in a sandbox.
func (s *Store) SetHelperStamp(sandbox, target string, st HelperStamp) error {
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO helper_stamps(sandbox,target,guidance,mcp,skills) VALUES(?,?,?,?,?)`, sandbox, target, st.Guidance, st.MCP, st.Skills)
	return err
}

// ListHelperStamps returns all stamps of a sandbox (empty map when there are none).
func (s *Store) ListHelperStamps(sandbox string) (map[string]HelperStamp, error) {
	rows, err := s.DB.Query(`SELECT target,guidance,mcp,skills FROM helper_stamps WHERE sandbox=?`, sandbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HelperStamp{}
	for rows.Next() {
		var target string
		var st HelperStamp
		if err := rows.Scan(&target, &st.Guidance, &st.MCP, &st.Skills); err != nil {
			return nil, err
		}
		out[target] = st
	}
	return out, rows.Err()
}

// Audit writes an entry; errors must never be swallowed.
func (s *Store) Audit(actor, event, sandbox, worktree, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO audit(ts,actor,event,sandbox,worktree,detail) VALUES(?,?,?,?,?,?)`, time.Now().Unix(), actor, event, sandbox, worktree, detail)
	return err
}
func (s *Store) AuditList(limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(`SELECT ts,actor,event,COALESCE(sandbox,''),COALESCE(worktree,''),COALESCE(detail,'') FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Event, &e.Sandbox, &e.Worktree, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.Unix(t, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

type Request struct {
	ID                                    int64
	Sandbox, Worktree, Kind, Path, Reason string
	Status                                string
	Created                               time.Time
	Bytes, Files                          int64
	Extra                                 string // e.g. host key fingerprints for SSH requests
}

func (s *Store) AddRequest(r Request) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO requests(sandbox,worktree,kind,path,reason,status,created,bytes,files,extra) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.Sandbox, r.Worktree, r.Kind, r.Path, r.Reason, r.Status, time.Now().Unix(), r.Bytes, r.Files, r.Extra)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRequestStatus changes only a still open request (pending); otherwise false.
func (s *Store) SetRequestStatus(id int64, status string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE requests SET status=? WHERE id=? AND status='pending'`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func scanReq(sc interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var c int64
	var worktree, reason, extra sql.NullString
	err := sc.Scan(&r.ID, &r.Sandbox, &worktree, &r.Kind, &r.Path, &reason, &r.Status, &c, &r.Bytes, &r.Files, &extra)
	r.Worktree, r.Reason, r.Extra, r.Created = worktree.String, reason.String, extra.String, time.Unix(c, 0)
	return r, err
}

const reqCols = `id,sandbox,worktree,kind,path,reason,status,created,bytes,files,extra`

func (s *Store) GetRequest(id int64) (Request, error) {
	r, err := scanReq(s.DB.QueryRow(`SELECT `+reqCols+` FROM requests WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return r, fmt.Errorf("request %d does not exist", id)
	}
	return r, err
}

func (s *Store) ListRequests(onlyPending bool, limit int) ([]Request, error) {
	q := `SELECT ` + reqCols + ` FROM requests`
	if onlyPending {
		q += ` WHERE status='pending'`
	}
	rows, err := s.DB.Query(q+` ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scanReq(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ExpirePending marks all open requests as expired at daemon start
// (the waiting request calls are gone with the restart anyway).
func (s *Store) ExpirePending() error {
	_, err := s.DB.Exec(`UPDATE requests SET status='expired' WHERE status='pending'`)
	return err
}

type SSHGrant struct {
	ID                      int64
	Sandbox, Worktree, Host string
	KeyFP, User             string
	HostKeys                []string // erlaubte Host-Key-Fingerprints (SHA256:...)
	Expires                 time.Time
	PerConn, Revoked        bool
	Created                 time.Time
}

func (s *Store) AddGrant(g SSHGrant) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO ssh_grants(sandbox,worktree,host,key_fp,user,hostkeys,expires,per_conn,created) VALUES(?,?,?,?,?,?,?,?,?)`,
		g.Sandbox, g.Worktree, g.Host, g.KeyFP, g.User, strings.Join(g.HostKeys, ","), g.Expires.Unix(), g.PerConn, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const grantCols = `id,sandbox,COALESCE(worktree,''),host,key_fp,user,hostkeys,expires,per_conn,revoked,created`

func scanGrant(sc interface{ Scan(...any) error }) (SSHGrant, error) {
	var g SSHGrant
	var hk string
	var exp, cr int64
	err := sc.Scan(&g.ID, &g.Sandbox, &g.Worktree, &g.Host, &g.KeyFP, &g.User, &hk, &exp, &g.PerConn, &g.Revoked, &cr)
	if hk != "" {
		g.HostKeys = strings.Split(hk, ",")
	}
	g.Expires, g.Created = time.Unix(exp, 0), time.Unix(cr, 0)
	return g, err
}

func (s *Store) GetGrant(id int64) (SSHGrant, error) {
	g, err := scanGrant(s.DB.QueryRow(`SELECT `+grantCols+` FROM ssh_grants WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return g, fmt.Errorf("grant %d does not exist", id)
	}
	return g, err
}

// ListGrants: only active (not revoked, not expired) or all.
func (s *Store) ListGrants(onlyActive bool, now time.Time) ([]SSHGrant, error) {
	q := `SELECT ` + grantCols + ` FROM ssh_grants`
	var args []any
	if onlyActive {
		q += ` WHERE revoked=0 AND expires>?`
		args = append(args, now.Unix())
	}
	rows, err := s.DB.Query(q+` ORDER BY id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHGrant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) RevokeGrant(id int64) error {
	res, err := s.DB.Exec(`UPDATE ssh_grants SET revoked=1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("grant %d does not exist", id)
	}
	return nil
}

func (s *Store) SetGrantExpiry(id int64, t time.Time) error {
	_, err := s.DB.Exec(`UPDATE ssh_grants SET expires=? WHERE id=? AND revoked=0`, t.Unix(), id)
	return err
}

// AuditSearch: newest first, optional text filter over all fields.
func (s *Store) AuditSearch(limit int, q string) ([]AuditEntry, error) {
	like := "%" + strings.ReplaceAll(strings.ReplaceAll(q, "%", ""), "_", "") + "%"
	rows, err := s.DB.Query(`SELECT ts,actor,event,COALESCE(sandbox,''),COALESCE(worktree,''),COALESCE(detail,'') FROM audit
		WHERE ?='%%' OR actor LIKE ? OR event LIKE ? OR sandbox LIKE ? OR worktree LIKE ? OR detail LIKE ? ORDER BY id DESC LIMIT ?`,
		like, like, like, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Event, &e.Sandbox, &e.Worktree, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.Unix(t, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Seeded: the first worktree ("main") was already created for the sandbox. After that none is ever created automatically again.
func (s *Store) Seeded(name string) bool {
	var v int
	if err := s.DB.QueryRow(`SELECT seeded FROM sandboxes WHERE name=?`, name).Scan(&v); err != nil {
		return true // when in doubt create nothing automatically
	}
	return v == 1
}

func (s *Store) SetSeeded(name string) error {
	_, err := s.DB.Exec(`UPDATE sandboxes SET seeded=1 WHERE name=?`, name)
	return err
}

// Backup writes a consistent copy of the database to dst (VACUUM INTO).
func (s *Store) Backup(dst string) error {
	_ = os.Remove(dst)
	_, err := s.DB.Exec(`VACUUM INTO ?`, dst)
	return err
}
