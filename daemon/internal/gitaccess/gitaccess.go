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
	// MaxWindowMinutes: longest full repo access. Tokens are re-minted on demand while a window runs (Credential).
	MaxWindowMinutes = 24 * 60
	// MaxRepos: repositories in one request (one card, one decision, one grant per repository).
	MaxRepos = 10
	// Grace: a token that is replaced because the set of grants changed stays valid this long, so a running git process of
	// another grant is not cut off in the middle of a push.
	Grace = 2 * time.Minute
	// renewBefore: a token that expires sooner than this is replaced.
	renewBefore = 5 * time.Minute
)

// Detail is shown in the app (requests.extra).
type Detail struct {
	Kind  string   `json:"kind"`            // grant | action
	Repo  string   `json:"repo"`            // first repository (older apps show only this one)
	Repos []string `json:"repos,omitempty"` // all repositories of a grant request
	Write bool     `json:"write,omitempty"`
	// Workflows: the assistant also wants to change .github/workflows (only with write). The card pre-ticks it; the user decides.
	Workflows bool     `json:"workflows,omitempty"`
	Minutes   int      `json:"minutes,omitempty"`
	Action    string   `json:"action,omitempty"`  // pr_create | pr_merge | ci_rerun
	Summary   []string `json:"summary,omitempty"` // lines shown to the user (display text only)
	Others    []string `json:"others,omitempty"`  // other active grants of the sandbox
	Max       int      `json:"max_minutes,omitempty"`
}

// WindowResult is the answer of OpenWindow.
type WindowResult struct {
	ID      int64
	Expires time.Time
}

type outcome struct {
	ok      bool
	note    string
	grants  []int64 // one grant per repository of the request
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
		if g.Window {
			mode = "full repo access"
		}
		out = append(out, fmt.Sprintf("%s (%s, until %s)", g.Repo, mode, g.Expires.Local().Format("15:04")))
	}
	return out
}

// windowFor returns the active full repo access (window) of a sandbox for a repository (the one that runs longest).
func (s *Service) windowFor(sandbox, repo string) (store.GitGrant, bool) {
	gs, err := s.activeGrants(sandbox)
	if err != nil {
		return store.GitGrant{}, false
	}
	var best store.GitGrant
	found := false
	for _, g := range gs {
		if g.Window && strings.EqualFold(g.Repo, repo) && (!found || g.Expires.After(best.Expires)) {
			best, found = g, true
		}
	}
	return best, found
}

// OpenWindow is the user's own decision in the app (never an assistant's call): for the given time the sandbox may push to the
// repository, open pull requests and re-run CI without asking each time; merge only when merge is set. A running window of the
// same repository in the same sandbox is replaced, so there is at most one per repository; windows of other repositories are
// independent. The assistant cannot release a window (Release).
func (s *Service) OpenWindow(ctx context.Context, sandbox, repo string, minutes int, opt Opts) (WindowResult, error) {
	gr, err := parseRepo(repo)
	if err != nil {
		return WindowResult{}, err
	}
	if msg := s.notReady(ctx, gr); msg != "" {
		return WindowResult{}, errors.New(msg)
	}
	if minutes <= 0 {
		return WindowResult{}, errors.New("duration missing")
	}
	if minutes > MaxWindowMinutes {
		minutes = MaxWindowMinutes
	}
	return s.openWindow(sandbox, "", gr.String(), minutes, opt, "")
}

func (s *Service) openWindow(sandbox, folder, repo string, minutes int, opt Opts, reason string) (WindowResult, error) {
	if old, err := s.activeGrants(sandbox); err == nil {
		for _, g := range old {
			if g.Window && strings.EqualFold(g.Repo, repo) {
				_, _ = s.S.EndGitGrant(g.ID, 1)
			}
		}
	}
	exp := s.Now().Add(time.Duration(minutes) * time.Minute)
	id, err := s.S.AddGitGrant(store.GitGrant{Sandbox: sandbox, Worktree: folder, Repo: repo, Write: true, Window: true, Merge: opt.Merge, Workflows: opt.Workflows, Reason: reason, Expires: exp})
	if err != nil {
		return WindowResult{}, err
	}
	s.audit("git.window.opened", sandbox, folder, fmt.Sprintf("task=%d %s minutes=%d merge=%v workflows=%v until=%s", id, repo, minutes, opt.Merge, opt.Workflows, exp.Format(time.RFC3339)))
	return WindowResult{ID: id, Expires: exp}, nil
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
	TaskID  int64   // first grant (the only one for a single repository)
	TaskIDs []int64 // one per requested repository, in request order
	Expires time.Time
}

// parseRepos parses and de-duplicates the requested repositories (order kept).
func parseRepos(in []string) ([]github.Repo, error) {
	var out []github.Repo
	seen := map[string]bool{}
	for _, s := range in {
		if strings.TrimSpace(s) == "" {
			continue
		}
		gr, err := parseRepo(s)
		if err != nil {
			return nil, err
		}
		if k := strings.ToLower(gr.String()); !seen[k] {
			seen[k] = true
			out = append(out, gr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no repository given (use owner/name)")
	}
	if len(out) > MaxRepos {
		return nil, fmt.Errorf("at most %d repositories per request, got %d: split the task", MaxRepos, len(out))
	}
	return out, nil
}

// RequestGrant asks the user for a time-limited git grant on one or several repositories at once (one card, one decision with one
// duration and level for all of them; one grant, and so one task_id, per repository). Blocks until decided or timed out.
func (s *Service) RequestGrant(ctx context.Context, sandbox, folder string, repos []string, write, workflows bool, minutes int, reason string) (GrantResult, error) {
	workflows = workflows && write // workflow files need push access
	grs, err := parseRepos(repos)
	if err != nil {
		return GrantResult{Message: err.Error()}, nil
	}
	var names []string
	var problems []string
	for _, gr := range grs {
		names = append(names, gr.String())
		if msg := s.notReady(ctx, gr); msg != "" {
			if len(grs) > 1 {
				msg = gr.String() + ": " + msg
			}
			problems = append(problems, msg)
		}
	}
	if len(problems) > 0 { // nothing is asked until every repository can actually be granted
		return GrantResult{Message: strings.Join(problems, " ")}, nil
	}
	if minutes <= 0 {
		minutes = DefaultMinutes
	}
	if minutes > MaxMinutes {
		minutes = MaxMinutes
	}
	if res, ok := s.coveredByWindows(sandbox, folder, names, workflows); ok {
		return res, nil
	}
	d := Detail{Kind: "grant", Repo: names[0], Write: write, Workflows: workflows, Minutes: minutes, Max: MaxMinutes, Others: s.othersLine(sandbox)}
	if len(names) > 1 {
		d.Repos = names
	}
	extra, _ := json.Marshal(d)
	list := strings.Join(names, ", ")
	id, err := s.addRequest(store.Request{Sandbox: sandbox, Worktree: folder, Kind: "gitgrant", Path: list, Reason: clean(reason, 300), Status: "pending", Extra: string(extra)})
	if err != nil {
		return GrantResult{}, err
	}
	s.audit("git.requested", sandbox, folder, fmt.Sprintf("id=%d %s write=%v workflows=%v", id, list, write, workflows))
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
	ids := make([]string, len(o.grants))
	for i, g := range o.grants {
		ids[i] = fmt.Sprint(g)
	}
	until := o.expires.Local().Format("15:04")
	var msg string
	if len(names) == 1 {
		msg = fmt.Sprintf("Git access to %s granted (%s) until %s. Use plain git now; when the task is finished call release_git_access with task_id %d.", names[0], mode, until, o.grants[0])
	} else {
		var per []string
		for i, n := range names {
			per = append(per, fmt.Sprintf("%s = task %d", n, o.grants[i]))
		}
		msg = fmt.Sprintf("Git access to %d repositories granted (%s) until %s: %s. Use plain git now; when the task is finished call release_git_access with task_ids [%s].", len(names), mode, until, strings.Join(per, ", "), strings.Join(ids, ", "))
	}
	return GrantResult{Allowed: true, TaskID: o.grants[0], TaskIDs: o.grants, Expires: o.expires, Message: msg}, nil
}

// coveredByWindows answers a grant request without asking when every requested repository has running full repo access (windows
// include write access). The task ids returned are those of the windows; releasing them has no effect.
func (s *Service) coveredByWindows(sandbox, folder string, names []string, workflows bool) (GrantResult, bool) {
	var ids []int64
	var until time.Time
	for _, n := range names {
		w, ok := s.windowFor(sandbox, n)
		if !ok || (workflows && !w.Workflows) { // workflow files need the user's own tick: ask
			return GrantResult{}, false
		}
		ids = append(ids, w.ID)
		if until.IsZero() || w.Expires.Before(until) {
			until = w.Expires
		}
	}
	s.audit("git.window.used", sandbox, folder, fmt.Sprintf("git access without request for %s (windows %v)", strings.Join(names, ", "), ids))
	msg := fmt.Sprintf("The user has granted full repo access for %s: git access (read and write), pull requests and CI re-runs need no further approval until %s (workflow files and repository settings are not included). Use plain git now. Do not call release_git_access: the user controls this access.",
		strings.Join(names, ", "), until.Local().Format("15:04"))
	return GrantResult{Allowed: true, TaskID: ids[0], TaskIDs: ids, Expires: until, Message: msg}, true
}

// addRequest stores a pending request and registers its waiter in one step (under s.mu), so that Decide never sees a request
// whose requester is not yet waiting.
func (s *Service) addRequest(r store.Request) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.S.AddRequest(r)
	if err != nil {
		return 0, err
	}
	s.waiters[id] = make(chan outcome, 1)
	return id, nil
}

// wait blocks until a request created with addRequest is decided.
func (s *Service) wait(ctx context.Context, id int64, sandbox, folder, event string) (outcome, error) {
	s.mu.Lock()
	ch := s.waiters[id]
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
	return s.decide(ctx, id, allow, minutes, write, note, Opts{})
}

// Opts are the user's extra choices on a git grant: full repo access (Window), merging and workflow files.
type Opts struct {
	Window, Merge, Workflows bool
}

// DecideOpts is Decide with the extra choices. Workflows only counts together with write access.
func (s *Service) DecideOpts(ctx context.Context, id int64, allow bool, minutes int, write bool, note string, opt Opts) error {
	return s.decide(ctx, id, allow, minutes, write, note, opt)
}

// DecideWindow allows a grant request as full repo access (see OpenWindow) for all repositories of the request: up to
// MaxWindowMinutes, write access always included. It changes nothing for requests of single actions.
func (s *Service) DecideWindow(ctx context.Context, id int64, minutes int, merge bool, note string) error {
	return s.decide(ctx, id, true, minutes, true, note, Opts{Window: true, Merge: merge})
}

func (s *Service) decide(ctx context.Context, id int64, allow bool, minutes int, write bool, note string, opt Opts) error {
	window := opt.Window
	s.mu.Lock() // together with addRequest: a visible request always has its waiter
	r, err := s.S.GetRequest(id)
	ch, ok := s.waiters[id]
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if (r.Kind != "gitgrant" && r.Kind != "ghaction") || r.Status != "pending" {
		return fmt.Errorf("request %d is not an open git request", id)
	}
	if window && r.Kind != "gitgrant" {
		return fmt.Errorf("request %d is a single action, not a git grant", id)
	}
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
			limit := MaxMinutes
			if window {
				limit = MaxWindowMinutes
			}
			if minutes > limit {
				minutes = limit
			}
			// the user can lower the level (write -> read) but never raise it above what was asked; a window always includes
			// write access, it is the user's own offer and independent of what the assistant asked for
			w := (d.Write && write) || window
			o.write = w
			o.expires = s.Now().Add(time.Duration(minutes) * time.Minute)
			repos := d.Repos
			if len(repos) == 0 {
				repos = []string{d.Repo}
			}
			for _, repo := range repos {
				var gid int64
				var err error
				if window {
					var wr WindowResult
					wr, err = s.openWindow(r.Sandbox, r.Worktree, repo, minutes, opt, r.Reason)
					gid = wr.ID
				} else {
					gid, err = s.S.AddGitGrant(store.GitGrant{Sandbox: r.Sandbox, Worktree: r.Worktree, Repo: repo, Write: w, Workflows: w && opt.Workflows, Reason: r.Reason, Expires: o.expires})
				}
				if err != nil {
					for _, done := range o.grants { // all or nothing
						_, _ = s.S.EndGitGrant(done, 1)
					}
					return err
				}
				o.grants = append(o.grants, gid)
			}
		}
	}
	if ok, _ := s.S.SetRequestStatus(id, status); !ok {
		return errors.New("request no longer open")
	}
	detail := fmt.Sprintf("id=%d %s", id, r.Path)
	if len(o.grants) > 0 {
		detail += fmt.Sprintf(" tasks=%v write=%v until=%s", o.grants, o.write, o.expires.Format(time.RFC3339))
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
	// Per repository of that owner: the highest level of its grants and whether one of them allows workflow files. Repos with
	// workflow access get their own token (class "workflows"), so that permission never reaches the other repos of the owner.
	type repoLevel struct {
		name      string
		write, wf bool
	}
	levels := map[string]*repoLevel{}
	var order []string
	for _, g := range gs {
		if ownerOf(g.Repo) != owner {
			continue
		}
		key := strings.ToLower(g.Repo)
		rl := levels[key]
		if rl == nil {
			rl = &repoLevel{name: g.Repo[strings.IndexByte(g.Repo, '/')+1:]}
			levels[key] = rl
			order = append(order, key)
		}
		rl.write = rl.write || g.Write
		rl.wf = rl.wf || (g.Workflows && g.Write)
	}
	want, covered := levels[strings.ToLower(gr.String())]
	if !covered {
		return "", "", ErrNoGrant
	}
	wf := want.wf
	var repos []string
	write := false
	for _, k := range order {
		if levels[k].wf == wf {
			repos = append(repos, levels[k].name)
			write = write || levels[k].write
		}
	}
	now := s.Now()
	if t, ok, err := s.S.ActiveGitToken(sandbox, owner, wf); err != nil {
		return "", "", err
	} else if ok {
		if sameSet(t.Repos, repos) && t.Write == write && t.Expires.After(now.Add(renewBefore)) {
			return "x-access-token", t.Token, nil
		}
		if err := s.S.RetireGitTokensClass(sandbox, owner, wf, now.Add(Grace)); err != nil {
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
	perms := map[string]string{"contents": perm}
	if wf {
		perms["workflows"] = "write"
	}
	tok, err := s.App.Mint(ctx, inst, repos, perms)
	if err != nil {
		if wf {
			return "", "", fmt.Errorf("%w (for workflow files the GitHub App needs the permission \"Workflows: Read and write\": add it in the app's settings on GitHub and accept it for the installation, see Settings → GitHub)", err)
		}
		return "", "", err
	}
	if _, err := s.S.AddGitToken(store.GitToken{Sandbox: sandbox, Owner: owner, Token: tok.Token, Repos: repos, Write: write, Workflows: wf, Expires: tok.ExpiresAt}); err != nil {
		_ = s.App.Revoke(ctx, tok.Token)
		return "", "", err
	}
	extra := ""
	if wf {
		extra = " workflows=write"
	}
	s.audit("git.token", sandbox, "", fmt.Sprintf("owner=%s repos=%s contents=%s%s until=%s", owner, strings.Join(repos, ","), perm, extra, tok.ExpiresAt.Format(time.RFC3339)))
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
	if g.Window && g.Ended == 0 && g.Expires.After(s.Now()) {
		return ReleaseResult{Message: fmt.Sprintf("Git task %d is full repo access granted by the user for %s; only the user ends it (it also ends by itself at %s). Nothing to do.", id, g.Repo, g.Expires.Local().Format("15:04"))}, nil
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

// ReleaseMany ends several grants (the task_ids of one multi-repository request). Each id follows the rules of Release.
func (s *Service) ReleaseMany(ctx context.Context, sandbox string, ids []int64) (ReleaseResult, error) {
	if len(ids) == 1 {
		return s.Release(ctx, sandbox, ids[0])
	}
	if len(ids) == 0 {
		return ReleaseResult{Message: "No task_id given." + s.activeSummary(sandbox)}, nil
	}
	released := false
	var msgs []string
	for _, id := range ids {
		r, err := s.Release(ctx, sandbox, id)
		if err != nil {
			return ReleaseResult{}, err
		}
		released = released || r.Released
		msgs = append(msgs, r.Message)
	}
	return ReleaseResult{Released: released, Message: msgs[len(msgs)-1]}, nil
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

// Extend is the user's "extend" in the app: moves the end of a running grant or window by the given minutes. The end is never
// later than MaxMinutes (grant) or MaxWindowMinutes (window) from now; an expired or ended grant cannot be extended.
func (s *Service) Extend(ctx context.Context, id int64, minutes int) (time.Time, error) {
	g, err := s.S.GetGitGrant(id)
	if err != nil {
		return time.Time{}, err
	}
	now := s.Now()
	if g.Ended != 0 || !g.Expires.After(now) {
		return time.Time{}, errors.New("this git grant has ended – a new request is needed")
	}
	if minutes < 1 {
		return time.Time{}, errors.New("duration must be at least 1 minute")
	}
	limit := MaxMinutes
	if g.Window {
		limit = MaxWindowMinutes
	}
	exp := g.Expires.Add(time.Duration(minutes) * time.Minute)
	if max := now.Add(time.Duration(limit) * time.Minute).Truncate(time.Second); exp.After(max) { // whole seconds: the database keeps no more
		exp = max
	}
	if !exp.After(g.Expires) {
		return time.Time{}, fmt.Errorf("already at the maximum of %d minutes from now", limit)
	}
	if ok, err := s.S.SetGitGrantExpiry(id, exp); err != nil {
		return time.Time{}, err
	} else if !ok {
		return time.Time{}, errors.New("this git grant has ended – a new request is needed")
	}
	s.audit("git.extended", g.Sandbox, g.Worktree, fmt.Sprintf("task=%d %s until=%s", id, g.Repo, exp.Format(time.RFC3339)))
	return exp, nil
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
