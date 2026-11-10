// Package ghtoken: the GitHub token of a sandbox (fine-grained personal access token).
// It lives in the VM (~/.git-credentials, 0600) and, for the daemon's repo checks, in ~/.bergfried/sandboxes/<name>/github-token (0600).
// It is used for all git actions over HTTPS and for GitHub Actions. The agent in the VM can read it – this is deliberate and
// a conscious trade-off (see docs/GITHUB.md and docs/SECURITY.md): it is limited only by the repo selection, permissions and expiry date of the token.
package ghtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// ExpiresDays: validity of the suggested token (GitHub allows 1–366 days).
const ExpiresDays = 365

// Store: Mac-seitige Kopie je Sandbox.
type Store struct{ Home string }

func (s Store) path(sandbox string) string {
	return filepath.Join(s.Home, "sandboxes", sandbox, "github-token")
}

func (s Store) Get(sandbox string) (string, error) {
	b, err := os.ReadFile(s.path(sandbox))
	if err != nil {
		return "", github.ErrNoToken
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", github.ErrNoToken
	}
	return t, nil
}

func (s Store) Has(sandbox string) bool { _, err := s.Get(sandbox); return err == nil }

func (s Store) Set(sandbox, token string) error {
	if err := ValidToken(token); err != nil {
		return err
	}
	p := s.path(sandbox)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s Store) Delete(sandbox string) { _ = os.Remove(s.path(sandbox)) }

// ValidToken: printable characters without whitespace only – the token is later written to a file, never to a command line.
func ValidToken(t string) error {
	t = strings.TrimSpace(t)
	if len(t) < 20 || len(t) > 255 {
		return errors.New("token has an implausible length")
	}
	for _, r := range t {
		if r <= ' ' || r > '~' || r == '@' || r == '/' || r == ':' || r == '%' || r == '\'' || r == '"' || r == '\\' {
			return errors.New("token contains illegal characters")
		}
	}
	return nil
}

// CredentialsLine: line for ~/.git-credentials (format of the credential helper "store").
func CredentialsLine(token string) string {
	return "https://x-access-token:" + strings.TrimSpace(token) + "@github.com\n"
}

// Link: GitHub form "New fine-grained token", as far as it can be pre-filled by URL. The repository selection cannot be pre-filled.
// The Workflows permission is missing on purpose: the agent should not be able to change .github/workflows.
func Link(sandbox string) string {
	name := "bergfried-" + sandbox
	if len(name) > 40 {
		name = name[:40]
	}
	q := url.Values{}
	q.Set("name", name)
	q.Set("description", "Bergfried sandbox "+sandbox+": git (clone/push/pull) and GitHub Actions for the agent in the VM")
	q.Set("expires_in", fmt.Sprint(ExpiresDays))
	q.Set("contents", "write")
	q.Set("pull_requests", "write")
	q.Set("actions", "write")
	q.Set("metadata", "read")
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
}

// Hint: text about the repository selection (cannot be pre-filled).
const Hint = "Under \"Repository access\" choose \"Only select repositories\" and select only the repos of this sandbox."

// Detail: shown in the app (requests.extra).
type Detail struct {
	Repo     string `json:"repo"`
	HasToken bool   `json:"has_token"`
	Link     string `json:"link"`
	Hint     string `json:"hint"`
}

// Service: request "token/access" from the agent, decision in the app.
type Service struct {
	S      *store.Store
	Tokens Store
	// Install writes the token into the sandbox's VM.
	Install func(ctx context.Context, sandbox, token string) error
	// Client returns a GitHub client for the token (tests: local stand-in).
	Client  func(token string) *github.Client
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]chan result
}

type result struct {
	ok   bool
	note string
}

func NewService(st *store.Store, tokens Store, install func(context.Context, string, string) error) *Service {
	return &Service{S: st, Tokens: tokens, Install: install, Timeout: 10 * time.Minute, waiters: map[int64]chan result{},
		Client: func(t string) *github.Client { return github.New(github.StaticToken(t)) }}
}

func (s *Service) audit(event, sandbox, worktree, detail string) {
	if err := s.S.Audit("daemon", event, sandbox, worktree, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

// CheckRepo: does the sandbox's token reach this repo?
func (s *Service) CheckRepo(ctx context.Context, token string, repo github.Repo) error {
	_, err := s.Client(token).DefaultBranch(ctx, repo)
	var ae *github.APIError
	if errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403) {
		return fmt.Errorf("the token has no access to %s (repo not in the selection or permissions missing)", repo)
	}
	return err
}

// Validate checks whether GitHub accepts the token.
func (s *Service) Validate(ctx context.Context, token string) error {
	if err := ValidToken(token); err != nil {
		return err
	}
	if err := s.Client(token).Ping(ctx); err != nil {
		var ae *github.APIError
		if errors.As(err, &ae) && ae.Status == 401 {
			return errors.New("GitHub rejects the token (invalid, expired or revoked)")
		}
		return err
	}
	return nil
}

// Deposit verifies the token, stores it on the Mac and installs it in the VM. repo (optional, owner/name) must be reachable.
func (s *Service) Deposit(ctx context.Context, sandbox, token, repo string) error {
	token = strings.TrimSpace(token)
	if err := s.Validate(ctx, token); err != nil {
		return err
	}
	if repo != "" {
		r, err := github.ParseRemote("https://github.com/" + repo)
		if err != nil {
			return err
		}
		if err := s.CheckRepo(ctx, token, r); err != nil {
			return err
		}
	}
	if err := s.Tokens.Set(sandbox, token); err != nil {
		return err
	}
	if s.Install != nil {
		if err := s.Install(ctx, sandbox, token); err != nil {
			return fmt.Errorf("token could not be written into the VM: %w", err)
		}
	}
	s.audit("ghtoken.set", sandbox, "", repo)
	return nil
}

// cleanNote limits a user note and removes control characters (newlines stay).
func cleanNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 500 {
		return string(r[:500]) + "…"
	}
	return s
}

// Request: the agent needs a token (repo empty) or access to repo. Blocks until decided.
// Returns (done, message). A repo the token already reaches is confirmed without asking.
func (s *Service) Request(ctx context.Context, sandbox, worktree, repo, reason string) (bool, string, error) {
	var gr github.Repo
	if repo != "" {
		r, err := github.ParseRemote("https://github.com/" + strings.TrimPrefix(repo, "https://github.com/"))
		if err != nil {
			return false, err.Error(), nil
		}
		gr, repo = r, r.String()
	}
	has := s.Tokens.Has(sandbox)
	if has && repo != "" {
		tok, _ := s.Tokens.Get(sandbox)
		if err := s.CheckRepo(ctx, tok, gr); err == nil {
			return true, "The sandbox's token already has access to " + repo + ".", nil
		}
	}
	if has && repo == "" {
		return true, "This sandbox already has a token. Name the repo (owner/name) you lack access to.", nil
	}
	d := Detail{Repo: repo, HasToken: has, Link: Link(sandbox), Hint: Hint}
	extra, _ := json.Marshal(d)
	path := repo
	if path == "" {
		path = "GitHub-Token"
	}
	id, err := s.S.AddRequest(store.Request{Sandbox: sandbox, Worktree: worktree, Kind: "ghtoken", Path: path, Reason: reason, Status: "pending", Extra: string(extra)})
	if err != nil {
		return false, "", err
	}
	ch := make(chan result, 1)
	s.mu.Lock()
	s.waiters[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	s.audit("ghtoken.requested", sandbox, worktree, fmt.Sprintf("id=%d %s", id, path))
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.ok {
			return true, "Access set up. Repeat the git command.", nil
		}
		msg := "The user declined the setup."
		if r.note != "" {
			msg += " Note from the user: " + r.note
		}
		return false, msg, nil
	case <-timer.C:
		if ok, _ := s.S.SetRequestStatus(id, "expired"); ok {
			s.audit("ghtoken.expired", sandbox, worktree, fmt.Sprintf("id=%d", id))
		}
		return false, "No answer, request expired.", nil
	case <-ctx.Done():
		_, _ = s.S.SetRequestStatus(id, "expired")
		return false, "", ctx.Err()
	}
}

// Decide: decision from the app. With a token: verify, store, install. Without a token (access only extended): check the existing token
// against the repo. If that fails the request stays open and the error goes to the app (try again).
func (s *Service) Decide(ctx context.Context, id int64, allow bool, token, note string) error {
	r, err := s.S.GetRequest(id)
	if err != nil {
		return err
	}
	if r.Kind != "ghtoken" || r.Status != "pending" {
		return fmt.Errorf("request %d is not an open token request", id)
	}
	s.mu.Lock()
	ch, ok := s.waiters[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("request %d is no longer waiting (the requester gave up)", id)
	}
	note = strings.TrimSpace(cleanNote(note))
	status := "denied"
	if allow {
		var d Detail
		_ = json.Unmarshal([]byte(r.Extra), &d)
		token = strings.TrimSpace(token)
		if token != "" {
			if err := s.Deposit(ctx, r.Sandbox, token, d.Repo); err != nil {
				return err
			}
		} else {
			tok, err := s.Tokens.Get(r.Sandbox)
			if err != nil {
				return errors.New("please paste the token")
			}
			gr, perr := github.ParseRemote("https://github.com/" + d.Repo)
			if d.Repo == "" || perr != nil {
				return errors.New("please paste the token")
			}
			if err := s.CheckRepo(ctx, tok, gr); err != nil {
				return err
			}
		}
		status = "allowed"
	}
	if ok, _ := s.S.SetRequestStatus(id, status); !ok {
		return errors.New("request no longer open")
	}
	s.audit("ghtoken."+status, r.Sandbox, r.Worktree, fmt.Sprintf("id=%d", id))
	ch <- result{ok: allow, note: note}
	return nil
}
