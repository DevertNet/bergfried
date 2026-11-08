// Package store hält Sandboxes, Agents und das Audit-Log in SQLite (reines Go, kein cgo).
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
	Created         time.Time
}

type Agent struct {
	Sandbox, Name, Worktree, ComposeProject string
	Created                                 time.Time
}

type AuditEntry struct {
	TS                                   time.Time
	Actor, Event, Sandbox, Agent, Detail string
}

// Open legt das Datenverzeichnis mit 0700 und die DB mit 0600 an.
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

// renameLegacy benennt Tabellen und Spalten aus der Zeit vor „Sandbox“ um (sessions -> sandboxes, Spalte session -> sandbox).
func (s *Store) renameLegacy() {
	has := func(table, column string) bool {
		rows, err := s.DB.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return false
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var c string
			_ = rows.Scan(&c)
			n++
			if column != "" && c == column {
				return true
			}
		}
		return column == "" && n > 0
	}
	if has("sessions", "") && !has("sandboxes", "") {
		_, _ = s.DB.Exec(`ALTER TABLE sessions RENAME TO sandboxes`)
	}
	for _, t := range []string{"agents", "requests", "ssh_grants", "audit"} {
		if has(t, "session") && !has(t, "sandbox") {
			_, _ = s.DB.Exec(`ALTER TABLE ` + t + ` RENAME COLUMN session TO sandbox`)
		}
	}
	_, _ = s.DB.Exec(`UPDATE audit SET event='sandbox.'||substr(event,9) WHERE event LIKE 'session.%'`)
}

func (s *Store) migrate() error {
	s.renameLegacy()
	defer func() {
		// Migration älterer DBs (Spalte extra); Fehler "duplicate column" ist erwartet.
		_, _ = s.DB.Exec(`ALTER TABLE requests ADD COLUMN extra TEXT`)
		_, _ = s.DB.Exec(`ALTER TABLE sandboxes ADD COLUMN seeded INTEGER NOT NULL DEFAULT 0`)
		// Reste des früheren Mac-Push-Konzepts (Token liegt jetzt in der VM, siehe docs/DESIGN-GIT.md).
		_, _ = s.DB.Exec(`DROP TABLE IF EXISTS git_pushes`)
		_, _ = s.DB.Exec(`DROP TABLE IF EXISTS ci_grants`)
	}()
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS sandboxes(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agents(sandbox TEXT NOT NULL, name TEXT NOT NULL, worktree TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(sandbox,name));
CREATE TABLE IF NOT EXISTS requests(id INTEGER PRIMARY KEY AUTOINCREMENT, sandbox TEXT NOT NULL, agent TEXT, kind TEXT NOT NULL, path TEXT NOT NULL, reason TEXT, status TEXT NOT NULL, created INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0, extra TEXT);
CREATE TABLE IF NOT EXISTS ssh_grants(id INTEGER PRIMARY KEY AUTOINCREMENT, sandbox TEXT NOT NULL, agent TEXT, host TEXT NOT NULL, key_fp TEXT NOT NULL, user TEXT NOT NULL, hostkeys TEXT NOT NULL, expires INTEGER NOT NULL, per_conn INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT NOT NULL, event TEXT NOT NULL, sandbox TEXT, agent TEXT, detail TEXT);`)
	return err
}

func (s *Store) AddSandbox(x Sandbox) error {
	_, err := s.DB.Exec(`INSERT INTO sandboxes(name,vm,repo,template_version,created) VALUES(?,?,?,?,?)`, x.Name, x.VM, x.Repo, x.TemplateVersion, x.Created.Unix())
	return err
}
func (s *Store) GetSandbox(name string) (Sandbox, error) {
	var x Sandbox
	var c int64
	err := s.DB.QueryRow(`SELECT name,vm,repo,template_version,created FROM sandboxes WHERE name=?`, name).Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c)
	if err == sql.ErrNoRows {
		return x, fmt.Errorf("Sandbox %q existiert nicht", name)
	}
	x.Created = time.Unix(c, 0)
	return x, err
}
func (s *Store) ListSandboxes() ([]Sandbox, error) {
	rows, err := s.DB.Query(`SELECT name,vm,repo,template_version,created FROM sandboxes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sandbox
	for rows.Next() {
		var x Sandbox
		var c int64
		if err := rows.Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c); err != nil {
			return nil, err
		}
		x.Created = time.Unix(c, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) DeleteSandbox(name string) error {
	if _, err := s.DB.Exec(`DELETE FROM agents WHERE sandbox=?`, name); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM sandboxes WHERE name=?`, name)
	return err
}
func (s *Store) AddAgent(a Agent) error {
	_, err := s.DB.Exec(`INSERT OR REPLACE INTO agents(sandbox,name,worktree,compose_project,created) VALUES(?,?,?,?,?)`, a.Sandbox, a.Name, a.Worktree, a.ComposeProject, a.Created.Unix())
	return err
}
func (s *Store) ListAgents(sandbox string) ([]Agent, error) {
	rows, err := s.DB.Query(`SELECT sandbox,name,worktree,compose_project,created FROM agents WHERE sandbox=? ORDER BY name`, sandbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		var c int64
		if err := rows.Scan(&a.Sandbox, &a.Name, &a.Worktree, &a.ComposeProject, &c); err != nil {
			return nil, err
		}
		a.Created = time.Unix(c, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Audit schreibt einen Eintrag; Fehler dürfen nie verschluckt werden.
func (s *Store) Audit(actor, event, sandbox, agent, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO audit(ts,actor,event,sandbox,agent,detail) VALUES(?,?,?,?,?,?)`, time.Now().Unix(), actor, event, sandbox, agent, detail)
	return err
}
func (s *Store) AuditList(limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(`SELECT ts,actor,event,COALESCE(sandbox,''),COALESCE(agent,''),COALESCE(detail,'') FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Event, &e.Sandbox, &e.Agent, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.Unix(t, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

type Request struct {
	ID                                 int64
	Sandbox, Agent, Kind, Path, Reason string
	Status                             string
	Created                            time.Time
	Bytes, Files                       int64
	Extra                              string // z. B. Host-Key-Fingerprints bei SSH-Anfragen
}

func (s *Store) AddRequest(r Request) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO requests(sandbox,agent,kind,path,reason,status,created,bytes,files,extra) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.Sandbox, r.Agent, r.Kind, r.Path, r.Reason, r.Status, time.Now().Unix(), r.Bytes, r.Files, r.Extra)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetRequestStatus ändert nur eine noch offene Anfrage (pending); sonst false.
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
	var agent, reason, extra sql.NullString
	err := sc.Scan(&r.ID, &r.Sandbox, &agent, &r.Kind, &r.Path, &reason, &r.Status, &c, &r.Bytes, &r.Files, &extra)
	r.Agent, r.Reason, r.Extra, r.Created = agent.String, reason.String, extra.String, time.Unix(c, 0)
	return r, err
}

const reqCols = `id,sandbox,agent,kind,path,reason,status,created,bytes,files,extra`

func (s *Store) GetRequest(id int64) (Request, error) {
	r, err := scanReq(s.DB.QueryRow(`SELECT `+reqCols+` FROM requests WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return r, fmt.Errorf("Anfrage %d existiert nicht", id)
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

// ExpirePending markiert beim Daemon-Start alle offenen Anfragen als abgelaufen
// (die wartenden Agent-Aufrufe sind mit dem Neustart ohnehin weg).
func (s *Store) ExpirePending() error {
	_, err := s.DB.Exec(`UPDATE requests SET status='expired' WHERE status='pending'`)
	return err
}

type SSHGrant struct {
	ID                   int64
	Sandbox, Agent, Host string
	KeyFP, User          string
	HostKeys             []string // erlaubte Host-Key-Fingerprints (SHA256:...)
	Expires              time.Time
	PerConn, Revoked     bool
	Created              time.Time
}

func (s *Store) AddGrant(g SSHGrant) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO ssh_grants(sandbox,agent,host,key_fp,user,hostkeys,expires,per_conn,created) VALUES(?,?,?,?,?,?,?,?,?)`,
		g.Sandbox, g.Agent, g.Host, g.KeyFP, g.User, strings.Join(g.HostKeys, ","), g.Expires.Unix(), g.PerConn, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const grantCols = `id,sandbox,COALESCE(agent,''),host,key_fp,user,hostkeys,expires,per_conn,revoked,created`

func scanGrant(sc interface{ Scan(...any) error }) (SSHGrant, error) {
	var g SSHGrant
	var hk string
	var exp, cr int64
	err := sc.Scan(&g.ID, &g.Sandbox, &g.Agent, &g.Host, &g.KeyFP, &g.User, &hk, &exp, &g.PerConn, &g.Revoked, &cr)
	if hk != "" {
		g.HostKeys = strings.Split(hk, ",")
	}
	g.Expires, g.Created = time.Unix(exp, 0), time.Unix(cr, 0)
	return g, err
}

func (s *Store) GetGrant(id int64) (SSHGrant, error) {
	g, err := scanGrant(s.DB.QueryRow(`SELECT `+grantCols+` FROM ssh_grants WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return g, fmt.Errorf("Freigabe %d existiert nicht", id)
	}
	return g, err
}

// ListGrants: nur aktive (nicht entzogen, nicht abgelaufen) oder alle.
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
		return fmt.Errorf("Freigabe %d existiert nicht", id)
	}
	return nil
}

func (s *Store) SetGrantExpiry(id int64, t time.Time) error {
	_, err := s.DB.Exec(`UPDATE ssh_grants SET expires=? WHERE id=? AND revoked=0`, t.Unix(), id)
	return err
}

// AuditSearch: neueste zuerst, optional Textfilter über alle Felder.
func (s *Store) AuditSearch(limit int, q string) ([]AuditEntry, error) {
	like := "%" + strings.ReplaceAll(strings.ReplaceAll(q, "%", ""), "_", "") + "%"
	rows, err := s.DB.Query(`SELECT ts,actor,event,COALESCE(sandbox,''),COALESCE(agent,''),COALESCE(detail,'') FROM audit
		WHERE ?='%%' OR actor LIKE ? OR event LIKE ? OR sandbox LIKE ? OR agent LIKE ? OR detail LIKE ? ORDER BY id DESC LIMIT ?`,
		like, like, like, like, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Event, &e.Sandbox, &e.Agent, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.Unix(t, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Seeded: Der erste Agent ("main") wurde für die Sandbox bereits angelegt. Danach wird nie wieder automatisch einer erzeugt.
func (s *Store) Seeded(name string) bool {
	var v int
	if err := s.DB.QueryRow(`SELECT seeded FROM sandboxes WHERE name=?`, name).Scan(&v); err != nil {
		return true // im Zweifel nichts automatisch anlegen
	}
	return v == 1
}

func (s *Store) SetSeeded(name string) error {
	_, err := s.DB.Exec(`UPDATE sandboxes SET seeded=1 WHERE name=?`, name)
	return err
}

func (s *Store) DeleteAgent(sandbox, agent string) error {
	_, err := s.DB.Exec(`DELETE FROM agents WHERE sandbox=? AND name=?`, sandbox, agent)
	return err
}
