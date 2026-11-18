package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghapp"
	"github.com/DevertNet/bergfried/daemon/internal/gitaccess"
)

// GitHub App (docs/GITHUB.md): setup status, the manifest flow, git grants and the decision for git requests.

type githubAppJ struct {
	Configured    bool                 `json:"configured"`
	Name          string               `json:"name,omitempty"`
	Slug          string               `json:"slug,omitempty"`
	AppID         int64                `json:"app_id,omitempty"`
	InstallURL    string               `json:"install_url,omitempty"`
	SettingsURL   string               `json:"settings_url,omitempty"`
	Installations []ghapp.Installation `json:"installations"`
	Error         string               `json:"error,omitempty"`
}

func (s *Server) githubAppStatus(w http.ResponseWriter, r *http.Request) {
	out := githubAppJ{Installations: []ghapp.Installation{}}
	if s.GitApp == nil {
		writeJSON(w, 200, out)
		return
	}
	cfg, _, err := s.GitApp.Store.Load()
	if err != nil {
		writeJSON(w, 200, out) // not set up
		return
	}
	out.Configured, out.Name, out.Slug, out.AppID = true, cfg.Name, cfg.Slug, cfg.AppID
	out.InstallURL = "https://github.com/apps/" + url.PathEscape(cfg.Slug) + "/installations/new"
	out.SettingsURL = "https://github.com/settings/apps/" + url.PathEscape(cfg.Slug)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if ins, err := s.GitApp.Installations(ctx); err != nil {
		out.Error = err.Error()
	} else if ins != nil {
		out.Installations = ins
	}
	writeJSON(w, 200, out)
}

// githubAppSetup starts the manifest flow and returns the loopback URL the app opens in the browser.
func (s *Server) githubAppSetup(w http.ResponseWriter, r *http.Request) {
	if s.GitSetup == nil {
		fail(w, 400, fmt.Errorf("GitHub App setup is not available"))
		return
	}
	if s.GitApp != nil && s.GitApp.Store.Configured() {
		fail(w, 409, fmt.Errorf("a GitHub App is already set up – forget it first (this ends all git grants)"))
		return
	}
	u, err := s.GitSetup.Start()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"url": u})
}

// githubAppForget ends all git grants (revoking their tokens) and removes the key from this Mac. The app itself stays on GitHub.
func (s *Server) githubAppForget(w http.ResponseWriter, r *http.Request) {
	if s.GitApp == nil || s.Git == nil {
		fail(w, 400, fmt.Errorf("not available"))
		return
	}
	ss, err := s.M.S.ListSandboxes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	for _, x := range ss {
		s.Git.EndSandbox(r.Context(), x.Name)
	}
	s.GitApp.Store.Delete()
	_ = s.M.S.Audit("app", "githubapp.forget", "", "", "")
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

type gitGrantJ struct {
	ID      int64  `json:"id"`
	Sandbox string `json:"sandbox"`
	Folder  string `json:"folder"`
	Repo    string `json:"repo"`
	Write   bool   `json:"write"`
	Reason  string `json:"reason"`
	Expires int64  `json:"expires_unix"`
}

func (s *Server) gitGrants(w http.ResponseWriter, r *http.Request) {
	out := []gitGrantJ{}
	gs, err := s.M.S.ListGitGrants("", true, time.Now())
	if err != nil {
		fail(w, 500, err)
		return
	}
	for _, g := range gs {
		out = append(out, gitGrantJ{g.ID, g.Sandbox, g.Worktree, g.Repo, g.Write, g.Reason, g.Expires.Unix()})
	}
	writeJSON(w, 200, out)
}

func (s *Server) gitRevoke(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID int64 `json:"id"`
	}
	if err := decode(r, &q); err != nil || s.Git == nil {
		fail(w, 400, fmt.Errorf("invalid request"))
		return
	}
	if err := s.Git.Revoke(r.Context(), q.ID); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

func (s *Server) gitDecide(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID      int64  `json:"id"`
		Allow   bool   `json:"allow"`
		Minutes int    `json:"minutes"`
		Write   bool   `json:"write"`
		Note    string `json:"note"`
	}
	if err := decode(r, &q); err != nil || s.Git == nil {
		fail(w, 400, fmt.Errorf("invalid request"))
		return
	}
	if err := s.Git.Decide(r.Context(), q.ID, q.Allow, q.Minutes, q.Write, q.Note); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

// githubLegacyRemove switches a sandbox from its personal token to the GitHub App: the token is removed from the VM and the Mac.
func (s *Server) githubLegacyRemove(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.M.RemoveGitToken(r.Context(), q.Name); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

var _ = gitaccess.DefaultMinutes
