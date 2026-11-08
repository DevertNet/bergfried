// Package store hält Sessions, Agents und das Audit-Log in SQLite (reines Go, kein cgo).
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

type Session struct {
	Name, VM, Repo  string
	TemplateVersion int
	Created         time.Time
}

type Agent struct {
	Session, Name, Worktree, ComposeProject string
	Created                                 time.Time
}

type AuditEntry struct {
	TS                                   time.Time
	Actor, Event, Session, Agent, Detail string
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

func (s *Store) migrate() error {
	defer func() {
		// Migration älterer DBs (Spalte extra); Fehler "duplicate column" ist erwartet.
		_, _ = s.DB.Exec(`ALTER TABLE requests ADD COLUMN extra TEXT`)
	}()
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS sessions(name TEXT PRIMARY KEY, vm TEXT NOT NULL, repo TEXT, template_version INTEGER NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS agents(session TEXT NOT NULL, name TEXT NOT NULL, worktree TEXT NOT NULL, compose_project TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(session,name));
CREATE TABLE IF NOT EXISTS requests(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, kind TEXT NOT NULL, path TEXT NOT NULL, reason TEXT, status TEXT NOT NULL, created INTEGER NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0, extra TEXT);
CREATE TABLE IF NOT EXISTS ssh_grants(id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL, agent TEXT, host TEXT NOT NULL, key_fp TEXT NOT NULL, user TEXT NOT NULL, hostkeys TEXT NOT NULL, expires INTEGER NOT NULL, per_conn INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, actor TEXT NOT NULL, event TEXT NOT NULL, session TEXT, agent TEXT, detail TEXT);`)
	return err
}

func (s *Store) AddSession(x Session) error {
	_, err := s.DB.Exec(`INSERT INTO sessions VALUES(?,?,?,?,?)`, x.Name, x.VM, x.Repo, x.TemplateVersion, x.Created.Unix())
	return err
}
func (s *Store) GetSession(name string) (Session, error) {
	var x Session
	var c int64
	err := s.DB.QueryRow(`SELECT name,vm,repo,template_version,created FROM sessions WHERE name=?`, name).Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c)
	if err == sql.ErrNoRows {
		return x, fmt.Errorf("Session %q existiert nicht", name)
	}
	x.Created = time.Unix(c, 0)
	return x, err
}
func (s *Store) ListSessions() ([]Session, error) {
	rows, err := s.DB.Query(`SELECT name,vm,repo,template_version,created FROM sessions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		var c int64
		if err := rows.Scan(&x.Name, &x.VM, &x.Repo, &x.TemplateVersion, &c); err != nil {
			return nil, err
		}
		x.Created = time.Unix(c, 0)
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) DeleteSession(name string) error {
	if _, err := s.DB.Exec(`DELETE FROM agents WHERE session=?`, name); err != nil {
		return err
	}
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE name=?`, name)
	return err
}
func (s *Store) AddAgent(a Agent) error {
	_, err := s.DB.Exec(`INSERT INTO agents VALUES(?,?,?,?,?)`, a.Session, a.Name, a.Worktree, a.ComposeProject, a.Created.Unix())
	return err
}
func (s *Store) ListAgents(session string) ([]Agent, error) {
	rows, err := s.DB.Query(`SELECT session,name,worktree,compose_project,created FROM agents WHERE session=? ORDER BY name`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		var c int64
		if err := rows.Scan(&a.Session, &a.Name, &a.Worktree, &a.ComposeProject, &c); err != nil {
			return nil, err
		}
		a.Created = time.Unix(c, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Audit schreibt einen Eintrag; Fehler dürfen nie verschluckt werden.
func (s *Store) Audit(actor, event, session, agent, detail string) error {
	_, err := s.DB.Exec(`INSERT INTO audit(ts,actor,event,session,agent,detail) VALUES(?,?,?,?,?,?)`, time.Now().Unix(), actor, event, session, agent, detail)
	return err
}
func (s *Store) AuditList(limit int) ([]AuditEntry, error) {
	rows, err := s.DB.Query(`SELECT ts,actor,event,COALESCE(session,''),COALESCE(agent,''),COALESCE(detail,'') FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&t, &e.Actor, &e.Event, &e.Session, &e.Agent, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.Unix(t, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

type Request struct {
	ID                                 int64
	Session, Agent, Kind, Path, Reason string
	Status                             string
	Created                            time.Time
	Bytes, Files                       int64
	Extra                              string // z. B. Host-Key-Fingerprints bei SSH-Anfragen
}

func (s *Store) AddRequest(r Request) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO requests(session,agent,kind,path,reason,status,created,bytes,files,extra) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.Session, r.Agent, r.Kind, r.Path, r.Reason, r.Status, time.Now().Unix(), r.Bytes, r.Files, r.Extra)
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
	err := sc.Scan(&r.ID, &r.Session, &agent, &r.Kind, &r.Path, &reason, &r.Status, &c, &r.Bytes, &r.Files, &extra)
	r.Agent, r.Reason, r.Extra, r.Created = agent.String, reason.String, extra.String, time.Unix(c, 0)
	return r, err
}

const reqCols = `id,session,agent,kind,path,reason,status,created,bytes,files,extra`

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
	Session, Agent, Host string
	KeyFP, User          string
	HostKeys             []string // erlaubte Host-Key-Fingerprints (SHA256:...)
	Expires              time.Time
	PerConn, Revoked     bool
	Created              time.Time
}

func (s *Store) AddGrant(g SSHGrant) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO ssh_grants(session,agent,host,key_fp,user,hostkeys,expires,per_conn,created) VALUES(?,?,?,?,?,?,?,?,?)`,
		g.Session, g.Agent, g.Host, g.KeyFP, g.User, strings.Join(g.HostKeys, ","), g.Expires.Unix(), g.PerConn, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const grantCols = `id,session,COALESCE(agent,''),host,key_fp,user,hostkeys,expires,per_conn,revoked,created`

func scanGrant(sc interface{ Scan(...any) error }) (SSHGrant, error) {
	var g SSHGrant
	var hk string
	var exp, cr int64
	err := sc.Scan(&g.ID, &g.Session, &g.Agent, &g.Host, &g.KeyFP, &g.User, &hk, &exp, &g.PerConn, &g.Revoked, &cr)
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
