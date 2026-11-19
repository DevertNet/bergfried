package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GitGrant is a time-limited permission for a sandbox to use git on one repository (see docs/GITHUB.md).
// ended: 0 = open, 1 = released by the assistant or the user, 2 = expired.
type GitGrant struct {
	ID                      int64
	Sandbox, Worktree, Repo string
	Write                   bool
	Reason                  string
	Expires, Created        time.Time
	Ended                   int
}

const gitGrantCols = `id,sandbox,COALESCE(worktree,''),repo,write,COALESCE(reason,''),expires,ended,created`

func scanGitGrant(sc interface{ Scan(...any) error }) (GitGrant, error) {
	var g GitGrant
	var w int
	var exp, cr int64
	err := sc.Scan(&g.ID, &g.Sandbox, &g.Worktree, &g.Repo, &w, &g.Reason, &exp, &g.Ended, &cr)
	g.Write, g.Expires, g.Created = w == 1, time.Unix(exp, 0), time.Unix(cr, 0)
	return g, err
}

func (s *Store) AddGitGrant(g GitGrant) (int64, error) {
	w := 0
	if g.Write {
		w = 1
	}
	res, err := s.DB.Exec(`INSERT INTO git_grants(sandbox,worktree,repo,write,reason,expires,created) VALUES(?,?,?,?,?,?,?)`,
		g.Sandbox, g.Worktree, g.Repo, w, g.Reason, g.Expires.Unix(), time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetGitGrant(id int64) (GitGrant, error) {
	g, err := scanGitGrant(s.DB.QueryRow(`SELECT `+gitGrantCols+` FROM git_grants WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return g, fmt.Errorf("git grant %d does not exist", id)
	}
	return g, err
}

// ListGitGrants: only active ones (open and not expired) or the latest 200. sandbox "" = all sandboxes.
func (s *Store) ListGitGrants(sandbox string, onlyActive bool, now time.Time) ([]GitGrant, error) {
	q := `SELECT ` + gitGrantCols + ` FROM git_grants WHERE 1=1`
	var args []any
	if sandbox != "" {
		q += ` AND sandbox=?`
		args = append(args, sandbox)
	}
	if onlyActive {
		q += ` AND ended=0 AND expires>?`
		args = append(args, now.Unix())
	}
	rows, err := s.DB.Query(q+` ORDER BY id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitGrant
	for rows.Next() {
		g, err := scanGitGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EndGitGrant closes an open grant (how: 1 released, 2 expired); false if it was already closed.
func (s *Store) EndGitGrant(id int64, how int) (bool, error) {
	res, err := s.DB.Exec(`UPDATE git_grants SET ended=? WHERE id=? AND ended=0`, how, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ExpireGitGrants closes all open grants whose time has run out and returns them.
func (s *Store) ExpireGitGrants(now time.Time) ([]GitGrant, error) {
	rows, err := s.DB.Query(`SELECT `+gitGrantCols+` FROM git_grants WHERE ended=0 AND expires<=?`, now.Unix())
	if err != nil {
		return nil, err
	}
	var out []GitGrant
	for rows.Next() {
		g, err := scanGitGrant(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	rows.Close()
	for _, g := range out {
		if _, err := s.EndGitGrant(g.ID, 2); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GitToken is an installation token handed out for a sandbox. Tokens are short-lived (one hour) and live only here and in the
// VM's memory/credential-helper output; the daemon revokes them (revoke_at) when grants end.
type GitToken struct {
	ID             int64
	Sandbox, Owner string
	Token          string
	Repos          []string
	Write          bool
	Expires        time.Time
	RevokeAt       time.Time // zero = in use
}

func (s *Store) AddGitToken(t GitToken) (int64, error) {
	w := 0
	if t.Write {
		w = 1
	}
	res, err := s.DB.Exec(`INSERT INTO git_tokens(sandbox,owner,token,repos,write,expires) VALUES(?,?,?,?,?,?)`,
		t.Sandbox, strings.ToLower(t.Owner), t.Token, strings.Join(t.Repos, ","), w, t.Expires.Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanGitToken(sc interface{ Scan(...any) error }) (GitToken, error) {
	var t GitToken
	var repos string
	var w int
	var exp, rev int64
	err := sc.Scan(&t.ID, &t.Sandbox, &t.Owner, &t.Token, &repos, &w, &exp, &rev)
	if repos != "" {
		t.Repos = strings.Split(repos, ",")
	}
	t.Write, t.Expires = w == 1, time.Unix(exp, 0)
	if rev > 0 {
		t.RevokeAt = time.Unix(rev, 0)
	}
	return t, err
}

const gitTokenCols = `id,sandbox,owner,token,repos,write,expires,revoke_at`

// ActiveGitToken returns the token currently in use for (sandbox, owner), if any.
func (s *Store) ActiveGitToken(sandbox, owner string) (GitToken, bool, error) {
	t, err := scanGitToken(s.DB.QueryRow(`SELECT `+gitTokenCols+` FROM git_tokens WHERE sandbox=? AND owner=? AND revoke_at=0 ORDER BY id DESC LIMIT 1`, sandbox, strings.ToLower(owner)))
	if err == sql.ErrNoRows {
		return t, false, nil
	}
	return t, err == nil, err
}

// RetireGitTokens schedules every in-use token of (sandbox, owner) for revocation at "at" (owner "" = all owners of the sandbox).
func (s *Store) RetireGitTokens(sandbox, owner string, at time.Time) error {
	// A token already retired for later is moved forward (never back): releasing the last grant of an owner must not wait for the
	// grace time that an earlier release of the same task started.
	q := `UPDATE git_tokens SET revoke_at=? WHERE sandbox=? AND (revoke_at=0 OR revoke_at>?)`
	args := []any{at.Unix(), sandbox, at.Unix()}
	if owner != "" {
		q += ` AND owner=?`
		args = append(args, strings.ToLower(owner))
	}
	_, err := s.DB.Exec(q, args...)
	return err
}

// DueGitTokens returns tokens whose revocation time has come, plus long expired ones that only need to be forgotten.
func (s *Store) DueGitTokens(now time.Time) ([]GitToken, error) {
	rows, err := s.DB.Query(`SELECT `+gitTokenCols+` FROM git_tokens WHERE (revoke_at>0 AND revoke_at<=?) OR expires<=?`, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitToken
	for rows.Next() {
		t, err := scanGitToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteGitToken(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM git_tokens WHERE id=?`, id)
	return err
}
