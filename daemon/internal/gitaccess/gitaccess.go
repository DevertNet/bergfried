// Package gitaccess: git and GitHub access of the sandboxes through the user's own GitHub App.
//
//   - Git grants work like SSH grants: the assistant asks (request_git_access), the user allows for a short time, and a credential
//     helper in the VM receives an installation token narrowed to the granted repositories (contents only). release_git_access ends
//     the grant and revokes the token on GitHub. Several grants of one sandbox share one token (the union), see Credential.
//   - Merging, opening pull requests and re-running CI are never done with a token in the VM: the daemon executes them after the user
//     allowed that single action (Action).
//
// The private key and every token live on the Mac. The grant is per sandbox: all assistants of a VM share the Linux user, so the
// folder name shown in requests is a hint, not a boundary (docs/SECURITY.md).
package gitaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghapp"
	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const (
	DefaultMinutes = 15
	MaxMinutes     = 60 // GitHub installation tokens live one hour
	// Grace: a token that is replaced because the set of grants changed stays valid this long, so a running git process of
	// another grant is not cut off in the middle of a push.
	Grace = 2 * time.Minute
	// renewBefore: a token that expires sooner than this is replaced.
	renewBefore = 5 * time.Minute
)

// Detail is shown in the app (requests.extra).
type Detail struct {
	Kind    string   `json:"kind"` // grant | action
	Repo    string   `json:"repo"`
	Write   bool     `json:"write,omitempty"`
	Minutes int      `json:"minutes,omitempty"`
	Action  string   `json:"action,omitempty"`  // pr_create | pr_merge | ci_rerun
	Summary []string `json:"summary,omitempty"` // lines shown to the user (display text only)
	Others  []string `json:"others,omitempty"`  // other active grants of the sandbox
	Max     int      `json:"max_minutes,omitempty"`
}

type outcome struct {
	ok      bool
	note    string
	grant   int64
	expires time.Time
	write   bool
}

// Service is the grant and action service of the daemon.
type Service struct {
	S       *store.Store
	App     *ghapp.Client
	Client  func(token string) *github.Client // tests: local stand-in
	Timeout time.Duration
	Now     func() time.Time
	// Install is the link shown when the app is not installed on a repository (set by the daemon from the app's slug).
	InstallHint func() string

	mu      sync.Mutex
	waiters map[int64]chan outcome
	credMu  sync.Mutex // one token is minted at a time: parallel git processes must not each mint their own
}

func NewService(st *store.Store, app *ghapp.Client) *Service {
	return &Service{S: st, App: app, Timeout: 10 * time.Minute, Now: time.Now, waiters: map[int64]chan outcome{},
		Client: func(t string) *github.Client { return github.New(github.StaticToken(t)) }}
}

func (s *Service) audit(event, sandbox, folder, detail string) {
	if err := s.S.Audit("daemon", event, sandbox, folder, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

// clean limits assistant-provided display text: no control characters, bounded length.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func parseRepo(s string) (github.Repo, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	return github.ParseRemote("https://github.com/" + s)
}

func ownerOf(repo string) string {
	if i := strings.IndexByte(repo, '/'); i > 0 {
		return strings.ToLower(repo[:i])
	}
	return strings.ToLower(repo)
}

// activeGrants returns the open, not expired grants of a sandbox.
func (s *Service) activeGrants(sandbox string) ([]store.GitGrant, error) {
	return s.S.ListGitGrants(sandbox, true, s.Now())
}

func (s *Service) othersLine(sandbox string) []string {
	gs, err := s.activeGrants(sandbox)
	if err != nil {
		return nil
	}
	var out []string
	for _, g := range gs {
		mode := "read"
		if g.Write {
			mode = "write"
		}
		out = append(out, fmt.Sprintf("%s (%s, until %s)", g.Repo, mode, g.Expires.Local().Format("15:04")))
	}
	return out
}

// notReady explains why nothing can be requested: no app, or the app is not installed on the repository.
func (s *Service) notReady(ctx context.Context, repo github.Repo) string {
	if !s.App.Store.Configured() {
		return "No GitHub App is set up on this Mac yet. Ask the user to set it up (Bergfried → Settings → GitHub)."
	}
	if _, err := s.App.InstallationFor(ctx, repo.Owner, repo.Name); err != nil {
		if errors.Is(err, ghapp.ErrNotInstalled) {
			h := ""
			if s.InstallHint != nil {
				h = " " + s.InstallHint()
			}
			return "The user's GitHub App is not installed on " + repo.String() + " (or has no access to it). Ask the user to install it for that repository." + h
		}
		return "GitHub could not be reached: " + err.Error()
	}
	return ""
}

// GrantResult is the answer of RequestGrant.
type GrantResult struct {
	Allowed bool
	Expired bool
	Message string
	TaskID  int64
	Expires time.Time
}

// RequestGrant asks the user for a time-limited git grant on one repository. Blocks until decided or timed out.
func (s *Service) RequestGrant(ctx context.Context, sandbox, folder, repo string, write bool, minutes int, reason string) (GrantResult, error) {
	gr, err := parseRepo(repo)
	if err != nil {
		return GrantResult{Message: err.Error()}, nil
	}
	if msg := s.notReady(ctx, gr); msg != "" {
		return GrantResult{Message: msg}, nil
	}
	if minutes <= 0 {
		minutes = DefaultMinutes
	}
	if minutes > MaxMinutes {
		minutes = MaxMinutes
	}
	d := Detail{Kind: "grant", Repo: gr.String(), Write: write, Minutes: minutes, Max: MaxMinutes, Others: s.othersLine(sandbox)}
	extra, _ := json.Marshal(d)
	id, err := s.S.AddRequest(store.Request{Sandbox: sandbox, Worktree: folder, Kind: "gitgrant", Path: gr.String(), Reason: clean(reason, 300), Status: "pending", Extra: string(extra)})
	if err != nil {
		return GrantResult{}, err
	}
	s.audit("git.requested", sandbox, folder, fmt.Sprintf("id=%d %s write=%v", id, gr, write))
	o, err := s.wait(ctx, id, sandbox, folder, "git")
	if err != nil {
		return GrantResult{}, err
	}
	if !o.ok {
		msg := "The user declined the git access."
		if o.note == "expired" {
			return GrantResult{Expired: true, Message: "No answer, request expired."}, nil
		}
		if o.note != "" {
			msg += " Note from the user: " + o.note
		}
		return GrantResult{Message: msg}, nil
	}
	mode := "read-only"
	if o.write {
		mode = "read and write"
	}
	return GrantResult{Allowed: true, TaskID: o.grant, Expires: o.expires,
		Message: fmt.Sprintf("Git access to %s granted (%s) until %s. Use plain git now; when the task is finished call release_git_access with task_id %d.", gr, mode, o.expires.Local().Format("15:04"), o.grant)}, nil
}

// wait registers a waiter for a request and blocks until it is decided.
func (s *Service) wait(ctx context.Context, id int64, sandbox, folder, event string) (outcome, error) {
	ch := make(chan outcome, 1)
	s.mu.Lock()
	s.waiters[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case o := <-ch:
		return o, nil
	case <-timer.C:
		if ok, _ := s.S.SetRequestStatus(id, "expired"); ok {
			s.audit(event+".request_expired", sandbox, folder, fmt.Sprintf("id=%d", id))
		}
		return outcome{note: "expired"}, nil
	case <-ctx.Done():
		_, _ = s.S.SetRequestStatus(id, "expired")
		return outcome{}, ctx.Err()
	}
}

// Decide is the user's answer from the app. For grants minutes and write may be changed by the user.
func (s *Service) Decide(ctx context.Context, id int64, allow bool, minutes int, write bool, note string) error {
	r, err := s.S.GetRequest(id)
	if err != nil {
		return err
	}
	if (r.Kind != "gitgrant" && r.Kind != "ghaction") || r.Status != "pending" {
		return fmt.Errorf("request %d is not an open git request", id)
	}
	s.mu.Lock()
	ch, ok := s.waiters[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("request %d is no longer waiting (the requester gave up)", id)
	}
	note = clean(note, 500)
	var d Detail
	_ = json.Unmarshal([]byte(r.Extra), &d)
	o := outcome{note: note}
	status := "denied"
	if allow {
		status = "allowed"
		o.ok = true
		if r.Kind == "gitgrant" {
			if minutes <= 0 {
				minutes = d.Minutes
			}
			if minutes <= 0 {
				minutes = DefaultMinutes
			}
			if minutes > MaxMinutes {
				minutes = MaxMinutes
			}
			// the user can lower the level (write -> read) but never raise it above what was asked
			w := d.Write && write
			o.write = w
			o.expires = s.Now().Add(time.Duration(minutes) * time.Minute)
			gid, err := s.S.AddGitGrant(store.GitGrant{Sandbox: r.Sandbox, Worktree: r.Worktree, Repo: d.Repo, Write: w, Reason: r.Reason, Expires: o.expires})
			if err != nil {
				return err
			}
			o.grant = gid
		}
	}
	if ok, _ := s.S.SetRequestStatus(id, status); !ok {
		return errors.New("request no longer open")
	}
	detail := fmt.Sprintf("id=%d %s", id, r.Path)
	if o.grant != 0 {
		detail += fmt.Sprintf(" task=%d write=%v until=%s", o.grant, o.write, o.expires.Format(time.RFC3339))
	}
	ev := "git.denied"
	if allow {
		ev = "git.allowed"
	}
	if r.Kind == "ghaction" {
		ev = strings.Replace(ev, "git.", "git.action.", 1)
	}
	s.audit(ev, r.Sandbox, r.Worktree, detail)
	ch <- o
	return nil
}

// ---- credential helper ----

// ErrNoGrant: the helper gets no credentials; git then fails with its usual "could not read Username".
var ErrNoGrant = errors.New("no active git grant for this repository")

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// Credential answers the git credential helper of a sandbox's VM. path is "owner/repo" or "owner/repo.git".
// The token covers the union of all active grants of the sandbox on that owner's repositories; a read-only
// union gets contents:read, any write grant raises it to contents:write.
func (s *Service) Credential(ctx context.Context, sandbox, host, path string) (user, token string, err error) {
	if strings.ToLower(host) != "github.com" {
		return "", "", ErrNoGrant
	}
	gr, perr := parseRepo(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"))
	if perr != nil {
		return "", "", ErrNoGrant
	}
	s.credMu.Lock()
	defer s.credMu.Unlock()
	owner := strings.ToLower(gr.Owner)
	gs, err := s.activeGrants(sandbox)
	if err != nil {
		return "", "", err
	}
	var repos []string
	seen := map[string]bool{}
	write, covered := false, false
	for _, g := range gs {
		if ownerOf(g.Repo) != owner {
			continue
		}
		name := g.Repo[strings.IndexByte(g.Repo, '/')+1:]
		if !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			repos = append(repos, name)
		}
		write = write || g.Write
		if strings.EqualFold(g.Repo, gr.String()) {
			covered = true
		}
	}
	if !covered {
		return "", "", ErrNoGrant
	}
	now := s.Now()
	if t, ok, err := s.S.ActiveGitToken(sandbox, owner); err != nil {
		return "", "", err
	} else if ok {
		if sameSet(t.Repos, repos) && t.Write == write && t.Expires.After(now.Add(renewBefore)) {
			return "x-access-token", t.Token, nil
		}
		if err := s.S.RetireGitTokens(sandbox, owner, now.Add(Grace)); err != nil {
			return "", "", err
		}
	}
	inst, err := s.App.InstallationFor(ctx, gr.Owner, repos[0])
	if err != nil {
		return "", "", err
	}
	perm := "read"
	if write {
		perm = "write"
	}
	tok, err := s.App.Mint(ctx, inst, repos, map[string]string{"contents": perm})
	if err != nil {
		return "", "", err
	}
	if _, err := s.S.AddGitToken(store.GitToken{Sandbox: sandbox, Owner: owner, Token: tok.Token, Repos: repos, Write: write, Expires: tok.ExpiresAt}); err != nil {
		_ = s.App.Revoke(ctx, tok.Token)
		return "", "", err
	}
	s.audit("git.token", sandbox, "", fmt.Sprintf("owner=%s repos=%s contents=%s until=%s", owner, strings.Join(repos, ","), perm, tok.ExpiresAt.Format(time.RFC3339)))
	return "x-access-token", tok.Token, nil
}

// ---- release, expiry, revocation ----

// afterEnd retires the tokens of the owner of an ended grant: right away when no grant of that owner is left, otherwise after the
// grace time (the next credential request mints a narrower token for the remaining grants).
func (s *Service) afterEnd(sandbox, repo string) {
	owner := ownerOf(repo)
	at := s.Now()
	if gs, err := s.activeGrants(sandbox); err == nil {
		for _, g := range gs {
			if ownerOf(g.Repo) == owner {
				at = at.Add(Grace)
				break
			}
		}
	}
	if err := s.S.RetireGitTokens(sandbox, owner, at); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: git tokens not retired:", err)
	}
}

// ReleaseResult is the answer of Release.
type ReleaseResult struct {
	Released bool
	Message  string
}

// Release ends the grant of a finished task. Only grants of the calling sandbox count; an unknown id and an id of another sandbox
// get the same answer, so a sandbox cannot probe for foreign ids. Needs no approval: it only reduces access.
func (s *Service) Release(ctx context.Context, sandbox string, id int64) (ReleaseResult, error) {
	g, err := s.S.GetGitGrant(id)
	if err != nil || g.Sandbox != sandbox {
		return ReleaseResult{Message: fmt.Sprintf("No git task %d in this sandbox.%s", id, s.activeSummary(sandbox))}, nil
	}
	ended, err := s.S.EndGitGrant(id, 1)
	if err != nil {
		return ReleaseResult{}, err
	}
	if !ended {
		return ReleaseResult{Message: fmt.Sprintf("Git task %d had already ended.%s", id, s.activeSummary(sandbox))}, nil
	}
	s.audit("git.released", sandbox, g.Worktree, fmt.Sprintf("task=%d %s", id, g.Repo))
	s.afterEnd(sandbox, g.Repo)
	s.Sweep(ctx)
	return ReleaseResult{Released: true, Message: fmt.Sprintf("Git access to %s ended.%s", g.Repo, s.activeSummary(sandbox))}, nil
}

func (s *Service) activeSummary(sandbox string) string {
	gs, err := s.activeGrants(sandbox)
	if err != nil || len(gs) == 0 {
		return ""
	}
	var parts []string
	for _, g := range gs {
		parts = append(parts, fmt.Sprintf("task %d (%s)", g.ID, g.Repo))
	}
	return " Active git tasks: " + strings.Join(parts, ", ") + "."
}

// Revoke is the user's "revoke" in the app.
func (s *Service) Revoke(ctx context.Context, id int64) error {
	g, err := s.S.GetGitGrant(id)
	if err != nil {
		return err
	}
	ended, err := s.S.EndGitGrant(id, 1)
	if err != nil {
		return err
	}
	if ended {
		s.audit("git.revoked", g.Sandbox, g.Worktree, fmt.Sprintf("task=%d %s", id, g.Repo))
		s.afterEnd(g.Sandbox, g.Repo)
		s.Sweep(ctx)
	}
	return nil
}

// EndSandbox ends every grant of a sandbox that stopped or was deleted and revokes its tokens.
func (s *Service) EndSandbox(ctx context.Context, sandbox string) {
	gs, err := s.S.ListGitGrants(sandbox, false, s.Now())
	if err != nil {
		return
	}
	for _, g := range gs {
		if ended, _ := s.S.EndGitGrant(g.ID, 1); ended {
			s.audit("git.released", sandbox, g.Worktree, fmt.Sprintf("task=%d %s (sandbox stopped)", g.ID, g.Repo))
		}
	}
	_ = s.S.RetireGitTokens(sandbox, "", s.Now())
	s.Sweep(ctx)
}

// Sweep closes expired grants and revokes tokens whose time has come (called every few seconds by the daemon).
func (s *Service) Sweep(ctx context.Context) {
	now := s.Now()
	if gs, err := s.S.ExpireGitGrants(now); err == nil {
		for _, g := range gs {
			s.audit("git.expired", g.Sandbox, g.Worktree, fmt.Sprintf("task=%d %s", g.ID, g.Repo))
			s.afterEnd(g.Sandbox, g.Repo)
		}
	}
	due, err := s.S.DueGitTokens(now)
	if err != nil {
		return
	}
	for _, t := range due {
		if t.Expires.After(now) { // still valid: revoke on GitHub first
			if err := s.App.Revoke(ctx, t.Token); err != nil {
				fmt.Fprintln(os.Stderr, "git token revoke failed (will retry):", err)
				continue
			}
			s.audit("git.token_revoked", t.Sandbox, "", fmt.Sprintf("owner=%s repos=%s", t.Owner, strings.Join(t.Repos, ",")))
		}
		_ = s.S.DeleteGitToken(t.ID)
	}
}
