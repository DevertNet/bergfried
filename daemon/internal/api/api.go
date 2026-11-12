// Package api: HTTP/JSON interface of the control app (local unix socket only, 0600).
// Long actions run as jobs; the app polls their status.
package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/access"
	"github.com/DevertNet/bergfried/daemon/internal/broker"
	"github.com/DevertNet/bergfried/daemon/internal/ghtoken"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/sandbox"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	"github.com/DevertNet/bergfried/daemon/internal/update"
)

// Version is set at build time (-ldflags "-X github.com/DevertNet/bergfried/daemon/internal/api.Version=1.2.3").
var Version = "0.0.0-dev"

type Server struct {
	M             *sandbox.Manager // Actor "app"
	Access        *access.Service
	SSH           *broker.Service
	Ix            *index.Index
	BlocklistFile string
	Upstream      string
	Mon           *Monitor
	GH            *ghtoken.Service
	Upd           *update.Service

	Home string      // ~/.bergfried (log files)
	Gate *index.Gate // holds back the first index build until the app has explained the folder prompts (nil: not gated)

	mu   sync.Mutex
	jobs map[int64]*Job
	next int64
}

const maxJobLog = 300

type Job struct {
	ID    int64    `json:"id"`
	Kind  string   `json:"kind"`
	Step  string   `json:"step,omitempty"`
	Log   []string `json:"log,omitempty"` // the last lines of output of long-running steps (limactl, VM provisioning)
	State string   `json:"state"`         // running | done | failed
	Error string   `json:"error,omitempty"`
}

func (s *Server) job(kind string, f func(ctx context.Context) error) *Job {
	return s.jobFor(kind, "", f)
}

// jobFor is like job; while the job runs the monitor does not poll the sandbox.
func (s *Server) jobFor(kind, sess string, f func(ctx context.Context) error) *Job {
	if sess != "" && s.Mon != nil {
		s.Mon.Hold(sess)
	}
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[int64]*Job{}
	}
	s.next++
	j := &Job{ID: s.next, Kind: kind, State: "running"}
	s.jobs[j.ID] = j
	s.mu.Unlock()
	go func() {
		ctx := sandbox.WithProgress(context.Background(), func(msg string) {
			s.mu.Lock()
			j.Step = msg
			s.mu.Unlock()
		})
		ctx = lima.WithOutput(ctx, func(line string) {
			s.mu.Lock()
			j.Log = append(j.Log, line)
			if len(j.Log) > maxJobLog {
				j.Log = append([]string(nil), j.Log[len(j.Log)-maxJobLog:]...)
			}
			s.mu.Unlock()
		})
		fmt.Fprintf(os.Stderr, "%s job %d (%s) started\n", time.Now().Format(time.RFC3339), j.ID, kind)
		err := f(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s job %d (%s) FAILED: %v\n", time.Now().Format(time.RFC3339), j.ID, kind, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s job %d (%s) done\n", time.Now().Format(time.RFC3339), j.ID, kind)
		}
		if sess != "" && s.Mon != nil {
			s.Mon.Release(sess)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			j.State, j.Error = "failed", err.Error()
		} else {
			j.State = "done"
		}
	}()
	return j
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

type claudeJ struct {
	Name     string `json:"name"`
	Folder   string `json:"folder"`
	Window   bool   `json:"window"`
	Running  bool   `json:"running"`
	RC       bool   `json:"rc"`
	RCURL    string `json:"rc_url"`
	RCAction string `json:"rc_action"` // "", "activating", "failed", "waiting"
}

type sandboxJ struct {
	Name              string    `json:"name"`
	VM                string    `json:"vm"`
	Status            string    `json:"status"`
	Repo              string    `json:"repo"`
	TemplateVersion   int       `json:"template_version"`
	Created           int64     `json:"created_unix"`
	CPUs              int       `json:"cpus"`
	Memory            int64     `json:"memory"`
	Disk              int64     `json:"disk"`
	ClaudeSessions    []claudeJ `json:"claude_sessions"`
	ClaudeLoggedIn    *bool     `json:"claude_logged_in"`
	OpenCodeLoggedIn  *bool     `json:"opencode_logged_in"`
	ClaudeInstalled   *bool     `json:"claude_installed"`
	OpenCodeInstalled *bool     `json:"opencode_installed"`
	OpenCodeRunning   *bool     `json:"opencode_running"`
	OpenCodePort      int       `json:"opencode_port"`
	HostDiskUsed      int64     `json:"host_disk_used"`
	GuestDiskUsed     int64     `json:"guest_disk_used"`
	GuestDiskSize     int64     `json:"guest_disk_size"`
	ProbeError        string    `json:"probe_error,omitempty"`
	ActiveGrants      int       `json:"active_grants"`
	Pending           int       `json:"pending_requests"`
	HasToken          bool      `json:"has_github_token"`
}
type sshInfoJ struct {
	HostKeyFPs       []string `json:"host_key_fingerprints"`
	Source           string   `json:"source"`
	DefaultKey       string   `json:"default_key"`
	DefaultUser      string   `json:"default_user"`
	RequestedUser    string   `json:"requested_user"`
	SuggestedMinutes int      `json:"suggested_minutes"`
}
type requestJ struct {
	ID      int64           `json:"id"`
	Sandbox string          `json:"sandbox"`
	Folder  string          `json:"folder"`
	Kind    string          `json:"kind"`
	Path    string          `json:"path"`
	Reason  string          `json:"reason"`
	Status  string          `json:"status"`
	Created int64           `json:"created_unix"`
	Bytes   int64           `json:"bytes"`
	Files   int64           `json:"files"`
	Large   bool            `json:"large"`
	SSH     *sshInfoJ       `json:"ssh,omitempty"`
	GitHub  *ghtoken.Detail `json:"github,omitempty"`
}
type grantJ struct {
	ID         int64    `json:"id"`
	Sandbox    string   `json:"sandbox"`
	Folder     string   `json:"folder"`
	Host       string   `json:"host"`
	Key        string   `json:"key_fingerprint"`
	User       string   `json:"user"`
	Expires    int64    `json:"expires_unix"`
	PerConn    bool     `json:"per_connection"`
	Revoked    bool     `json:"revoked"`
	Active     bool     `json:"active"`
	HostKeyFPs []string `json:"host_key_fingerprints"`
}
type auditJ struct {
	TS      int64  `json:"ts_unix"`
	Actor   string `json:"actor"`
	Event   string `json:"event"`
	Sandbox string `json:"sandbox"`
	Folder  string `json:"folder"`
	Detail  string `json:"detail"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/sandboxes", s.sandboxes)
	mux.HandleFunc("POST /v1/sandboxes/create", s.create)
	mux.HandleFunc("POST /v1/sandboxes/start", s.simple("sandbox.start", func(ctx context.Context, n string, _ bool) error { return s.M.Start(ctx, n) }))
	mux.HandleFunc("POST /v1/sandboxes/stop", s.simple("sandbox.stop", func(ctx context.Context, n string, _ bool) error { return s.M.Stop(ctx, n) }))
	mux.HandleFunc("POST /v1/sandboxes/delete", s.simple("sandbox.delete", func(ctx context.Context, n string, f bool) error { return s.M.Delete(ctx, n, f) }))
	mux.HandleFunc("POST /v1/sandboxes/pair", s.pair)
	mux.HandleFunc("GET /v1/claude/folders", s.claudeFolders)
	mux.HandleFunc("GET /v1/claude/browse", s.claudeBrowse)
	mux.HandleFunc("POST /v1/claude/mkdir", s.claudeMkdir)
	mux.HandleFunc("POST /v1/claude/start", s.claudeStart)
	mux.HandleFunc("POST /v1/claude/stop", s.claudeStop)
	mux.HandleFunc("POST /v1/claude/restart", s.claudeRestart)
	mux.HandleFunc("POST /v1/claude/remove", s.claudeRemove)
	mux.HandleFunc("POST /v1/claude/rc-ok", s.claudeRCOK)
	mux.HandleFunc("POST /v1/opencode/start", s.simple("opencode.start", func(ctx context.Context, n string, _ bool) error { return s.M.OpenCodeStart(ctx, n) }))
	mux.HandleFunc("POST /v1/opencode/stop", s.simple("opencode.stop", func(ctx context.Context, n string, _ bool) error { return s.M.OpenCodeStop(ctx, n) }))
	mux.HandleFunc("POST /v1/github/decide", s.githubDecide)
	mux.HandleFunc("POST /v1/index/start", s.indexStart)
	mux.HandleFunc("GET /v1/log", s.logTail)
	mux.HandleFunc("GET /v1/update", s.updateInfo)
	mux.HandleFunc("POST /v1/update/install", s.updateInstall)
	mux.HandleFunc("PUT /v1/update/channel", s.updateChannel)
	mux.HandleFunc("GET /v1/github/link", s.githubLink)
	mux.HandleFunc("POST /v1/base/build", s.baseBuild)
	mux.HandleFunc("GET /v1/jobs/{id}", s.jobGet)
	mux.HandleFunc("POST /v1/import/preview", s.importPreview)
	mux.HandleFunc("GET /v1/template", s.getTemplate)
	mux.HandleFunc("PUT /v1/template", s.putTemplate)
	mux.HandleFunc("POST /v1/template/reset", s.resetTemplate)
	mux.HandleFunc("GET /v1/requests", s.requests)
	mux.HandleFunc("POST /v1/requests/decide", s.decide)
	mux.HandleFunc("POST /v1/ssh/decide", s.sshDecide)
	mux.HandleFunc("GET /v1/ssh/grants", s.grants)
	mux.HandleFunc("POST /v1/ssh/revoke", s.revoke)
	mux.HandleFunc("POST /v1/ssh/extend", s.extend)
	mux.HandleFunc("GET /v1/ssh/keys", s.keys)
	mux.HandleFunc("GET /v1/audit", s.audit)
	mux.HandleFunc("GET /v1/audit.csv", s.auditCSV)
	mux.HandleFunc("GET /v1/blocklist", s.getBlocklist)
	mux.HandleFunc("PUT /v1/blocklist", s.putBlocklist)
	return mux
}

// Serve listens on the unix socket (directory 0700, socket 0600).
func (s *Server) Serve(ctx context.Context, sock string) error {
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	vms, err := s.M.L.List(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	base := false
	for _, v := range vms {
		if v.Name == s.M.BaseName() {
			base = true
		}
	}
	ss, _ := s.M.S.ListSandboxes()
	st := map[string]any{"version": Version, "base_ready": base, "base_name": s.M.BaseName(),
		"template_version": s.M.Rev(), "index_ready": s.Ix.Ready(), "index_waiting": s.Gate != nil && s.Gate.Waiting(), "upstream_agent": s.Upstream != "", "sandboxes": len(ss)}
	if s.Upd != nil {
		if u := s.Upd.Last(); u.Available {
			st["update_available"], st["update_version"], st["update_size"] = true, u.Latest, u.Size
		}
	}
	writeJSON(w, 200, st)
}

func (s *Server) sandboxes(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.S.ListSandboxes()
	if err != nil {
		fail(w, 500, err)
		return
	}
	vms, err := s.M.L.List(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	byVM := map[string]int{}
	for i, v := range vms {
		byVM[v.Name] = i
	}
	grants, _ := s.M.S.ListGrants(true, time.Now())
	pend, _ := s.M.S.ListRequests(true, 500)
	out := []sandboxJ{}
	for _, x := range ss {
		j := sandboxJ{Name: x.Name, VM: x.VM, Repo: x.Repo, TemplateVersion: x.TemplateVersion, Created: x.Created.Unix(), Status: "missing", ClaudeSessions: []claudeJ{}}
		if i, ok := byVM[x.VM]; ok {
			v := vms[i]
			j.Status, j.CPUs, j.Memory, j.Disk = v.Status, v.CPUs, v.Memory, v.Disk
		}
		var ps SandboxState
		var havePS bool
		if s.Mon != nil && j.Status == "Running" {
			ps, havePS = s.Mon.Get(x.Name)
		}
		if havePS {
			j.ClaudeLoggedIn, j.OpenCodeLoggedIn = ps.ClaudeLoggedIn, ps.OpenCodeLoggedIn
			j.ClaudeInstalled, j.OpenCodeInstalled, j.OpenCodeRunning = boolPtr(ps.ClaudeInstalled), boolPtr(ps.OpenCodeInstalled), boolPtr(ps.OpenCodeRunning)
			j.GuestDiskUsed, j.GuestDiskSize, j.ProbeError = ps.GuestUsed, ps.GuestSize, ps.Err
		}
		j.HostDiskUsed = hostDiskUsed(x.VM)
		j.OpenCodePort = x.OpenCodePort
		if s.GH != nil {
			j.HasToken = s.GH.Tokens.Has(x.Name)
		}
		wts, _ := s.M.S.ListWorktrees(x.Name)
		for _, w := range wts {
			ws := ps.Worktrees[w.Name]
			j.ClaudeSessions = append(j.ClaudeSessions, claudeJ{Name: w.Name, Folder: sandbox.FolderOf(w.Path), Window: ws.Window, Running: ws.Running, RC: ws.RC, RCURL: ws.URL, RCAction: ws.Action})
		}
		for _, g := range grants {
			if g.Sandbox == x.Name {
				j.ActiveGrants++
			}
		}
		for _, q := range pend {
			if q.Sandbox == x.Name {
				j.Pending++
			}
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

type nameReq struct {
	Name    string `json:"name"`
	Repo    string `json:"repo"`
	Force   bool   `json:"force"`
	Import  string `json:"import_path"`
	Token   string `json:"github_token"`
	Sandbox string `json:"sandbox"`
	Folder  string `json:"folder"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if err := sandbox.ValidSandboxName(q.Name); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 202, s.jobFor("sandbox.create", q.Name, func(ctx context.Context) error { return s.M.Create(ctx, q.Name, q.Repo, q.Import, q.Token) }))
}

// pair prepares the local OpenCode server and returns a one-time link for the desktop app.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if sandbox.ValidName(q.Name) != nil {
		fail(w, 400, fmt.Errorf("invalid name"))
		return
	}
	link, err := s.M.PairOpenCode(r.Context(), q.Name)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"url": link})
}

func (s *Server) simple(kind string, f func(context.Context, string, bool) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var q nameReq
		if err := decode(r, &q); err != nil {
			fail(w, 400, err)
			return
		}
		if err := sandbox.ValidName(q.Name); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 202, s.jobFor(kind, q.Name, func(ctx context.Context) error { return f(ctx, q.Name, q.Force) }))
	}
}

func (s *Server) claudeFolders(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("sandbox")
	if sandbox.ValidName(name) != nil {
		fail(w, 400, fmt.Errorf("invalid sandbox"))
		return
	}
	folders, err := s.M.ClaudeFolders(r.Context(), name)
	if err != nil {
		fail(w, 400, err)
		return
	}
	out := []map[string]string{}
	for _, f := range folders {
		out = append(out, map[string]string{"folder": f.Path, "label": f.Label})
	}
	writeJSON(w, 200, map[string]any{"folders": out})
}

func (s *Server) claudeBrowse(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("sandbox")
	if sandbox.ValidName(name) != nil {
		fail(w, 400, fmt.Errorf("invalid sandbox"))
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "work"
	}
	res, err := s.M.ClaudeBrowse(r.Context(), name, path, r.URL.Query().Get("hidden") == "1")
	if err != nil {
		fail(w, 400, err)
		return
	}
	folders := []map[string]string{}
	for _, f := range res.Folders {
		folders = append(folders, map[string]string{"folder": f.Path, "label": f.Label})
	}
	writeJSON(w, 200, map[string]any{"path": res.Path, "parent": res.Parent, "folders": folders})
}

func (s *Server) claudeMkdir(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Sandbox string `json:"sandbox"`
		Parent  string `json:"parent"`
		Name    string `json:"name"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if sandbox.ValidName(q.Sandbox) != nil {
		fail(w, 400, fmt.Errorf("invalid sandbox"))
		return
	}
	path, err := s.M.ClaudeMkdir(r.Context(), q.Sandbox, q.Parent, q.Name)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}

func (s *Server) claudeStart(w http.ResponseWriter, r *http.Request) {
	s.claudeAction(w, r, "claude.start", func(ctx context.Context, sandbox, folder string) error {
		return s.M.ClaudeStart(ctx, sandbox, folder)
	})
}

func (s *Server) claudeStop(w http.ResponseWriter, r *http.Request) {
	s.claudeAction(w, r, "claude.stop", func(ctx context.Context, sandbox, folder string) error {
		return s.M.ClaudeStop(ctx, sandbox, folder)
	})
}

func (s *Server) claudeRestart(w http.ResponseWriter, r *http.Request) {
	s.claudeAction(w, r, "claude.restart", func(ctx context.Context, sandbox, folder string) error {
		return s.M.ClaudeRestart(ctx, sandbox, folder)
	})
}

func (s *Server) claudeAction(w http.ResponseWriter, r *http.Request, kind string, f func(context.Context, string, string) error) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if sandbox.ValidName(q.Sandbox) != nil {
		fail(w, 400, fmt.Errorf("invalid sandbox"))
		return
	}
	writeJSON(w, 202, s.jobFor(kind, q.Sandbox, func(ctx context.Context) error {
		err := f(ctx, q.Sandbox, q.Folder)
		if s.Mon != nil {
			s.Mon.ResetClaude(q.Sandbox, q.Folder)
			s.Mon.Poke(q.Sandbox)
		}
		return err
	}))
}

func (s *Server) claudeRemove(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if sandbox.ValidName(q.Sandbox) != nil {
		fail(w, 400, fmt.Errorf("invalid sandbox"))
		return
	}
	writeJSON(w, 202, s.jobFor("claude.remove", q.Sandbox, func(ctx context.Context) error {
		err := s.M.ClaudeRemove(ctx, q.Sandbox, q.Folder)
		if err == nil && s.Mon != nil {
			s.Mon.ResetClaude(q.Sandbox, q.Folder)
		}
		return err
	}))
}

// updateInfo: current/latest version; ?refresh=1 asks GitHub now, otherwise the cached answer is returned.
func (s *Server) updateInfo(w http.ResponseWriter, r *http.Request) {
	if s.Upd == nil {
		fail(w, 404, fmt.Errorf("updates are not available"))
		return
	}
	info := s.Upd.Last()
	if r.URL.Query().Get("refresh") == "1" || info.Checked == 0 {
		info = s.Upd.Check(r.Context())
	}
	writeJSON(w, 200, info)
}

// updateChannel switches between "stable" and "dev" and checks again right away.
func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Channel string `json:"channel"`
	}
	if err := decode(r, &q); err != nil || s.Upd == nil {
		fail(w, 400, fmt.Errorf("invalid request"))
		return
	}
	if err := s.Upd.SetChannel(q.Channel); err != nil {
		fail(w, 400, err)
		return
	}
	_ = s.M.S.Audit("app", "update.channel", "", "", q.Channel)
	writeJSON(w, 200, s.Upd.Check(r.Context()))
}

// indexStart: the user has read the explanation in the app; the index build (and with it the macOS folder prompts) may begin.
func (s *Server) indexStart(w http.ResponseWriter, r *http.Request) {
	if s.Gate != nil {
		if err := s.Gate.Open(); err != nil {
			fail(w, 500, err)
			return
		}
	}
	_ = s.M.S.Audit("app", "index.start", "", "", Version)
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

// updateInstall downloads, verifies and installs the latest release as a job; the helper then restarts daemon and app.
func (s *Server) updateInstall(w http.ResponseWriter, r *http.Request) {
	if s.Upd == nil {
		fail(w, 404, fmt.Errorf("updates are not available"))
		return
	}
	writeJSON(w, 202, s.jobFor("update.install", "", func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		ctx = update.WithReport(ctx, func(m string) { sandbox.Progress(ctx, m) })
		if err := s.Upd.Install(ctx); err != nil {
			return err
		}
		_ = s.M.S.Audit("app", "update.install", "", "", "to "+s.Upd.Last().Latest+" from "+s.Upd.Current)
		return nil
	}))
}

func (s *Server) githubDecide(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID    int64  `json:"id"`
		Allow bool   `json:"allow"`
		Token string `json:"token"`
		Note  string `json:"note"`
	}
	if err := decode(r, &q); err != nil || s.GH == nil {
		fail(w, 400, fmt.Errorf("invalid request"))
		return
	}
	if err := s.GH.Decide(r.Context(), q.ID, q.Allow, q.Token, q.Note); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

// githubLink: pre-filled link for creating a fine-grained token (before the sandbox exists, so only the name).
func (s *Server) githubLink(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("sandbox")
	if name == "" {
		name = "new"
	}
	if sandbox.ValidName(name) != nil {
		fail(w, 400, fmt.Errorf("invalid name"))
		return
	}
	writeJSON(w, 200, map[string]string{"url": ghtoken.Link(name), "hint": ghtoken.Hint})
}

// claudeRCOK: the user confirms that Remote Control is running (resets "cannot be activated").
func (s *Server) claudeRCOK(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if sandbox.ValidName(q.Sandbox) != nil || s.Mon == nil {
		fail(w, 400, fmt.Errorf("invalid request"))
		return
	}
	name, err := s.M.ClaudeName(q.Sandbox, q.Folder)
	if err != nil {
		fail(w, 409, err)
		return
	}
	if err := s.Mon.MarkRC(q.Sandbox, name); err != nil {
		fail(w, 409, err)
		return
	}
	_ = s.M.S.Audit("app", "claude.rc_confirmed", q.Sandbox, q.Folder, "confirmed active by the user")
	writeJSON(w, 200, map[string]string{"message": "ok"})
}

func (s *Server) baseBuild(w http.ResponseWriter, r *http.Request) {
	rebuild := r.URL.Query().Get("rebuild") == "1"
	writeJSON(w, 202, s.job("base.build", func(ctx context.Context) error { return s.M.BuildBase(ctx, rebuild) }))
}

func (s *Server) jobGet(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	j, ok := s.jobs[id]
	var cp Job
	if ok {
		cp = *j
		cp.Log = append([]string(nil), j.Log...)
	}
	s.mu.Unlock()
	if !ok {
		fail(w, 404, fmt.Errorf("Job unbekannt"))
		return
	}
	writeJSON(w, 200, cp)
}

func (s *Server) importPreview(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Path string `json:"path"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if s.M.Imp == nil {
		fail(w, 500, fmt.Errorf("import not available"))
		return
	}
	files, bytes, err := s.M.Imp.Preview(q.Path)
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]int64{"files": files, "bytes": bytes})
}

type templateJ struct {
	YAML      string   `json:"yaml"`
	Revision  int      `json:"revision"`
	BaseName  string   `json:"base_name"`
	BaseReady bool     `json:"base_ready"`
	Path      string   `json:"path"`
	Warnings  []string `json:"warnings"`
}

func (s *Server) templateState(r *http.Request, warnings []string) (templateJ, error) {
	y, err := s.M.Tpl.Read()
	if err != nil {
		return templateJ{}, err
	}
	vms, _ := s.M.L.List(r.Context())
	ready := false
	for _, v := range vms {
		if v.Name == s.M.BaseName() {
			ready = true
		}
	}
	if warnings == nil {
		warnings = []string{}
	}
	if s.M.Tpl.SeedNewer() {
		warnings = append(warnings, "The bundled template has changed (for example new tools in the VM image), but you edited your copy, so it was not replaced. \"Reset\" adopts the bundled one and discards your edits.")
	}
	return templateJ{YAML: y, Revision: s.M.Rev(), BaseName: s.M.BaseName(), BaseReady: ready, Path: s.M.Tpl.Path(), Warnings: warnings}, nil
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	y, _ := s.M.Tpl.Read()
	warnings, _ := sandbox.CheckTemplate(y)
	t, err := s.templateState(r, warnings)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) putTemplate(w http.ResponseWriter, r *http.Request) {
	var q struct {
		YAML string `json:"yaml"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	warnings, err := s.M.Tpl.Save(r.Context(), q.YAML)
	if err != nil {
		fail(w, 400, err)
		return
	}
	_ = s.M.S.Audit("app", "template.save", "", "", fmt.Sprintf("revision=%d", s.M.Rev()))
	t, err := s.templateState(r, warnings)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) resetTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.M.Tpl.Reset(); err != nil {
		fail(w, 500, err)
		return
	}
	_ = s.M.S.Audit("app", "template.reset", "", "", fmt.Sprintf("revision=%d", s.M.Rev()))
	y, _ := s.M.Tpl.Read()
	warnings, _ := sandbox.CheckTemplate(y)
	t, err := s.templateState(r, warnings)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) requests(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	rs, err := s.M.S.ListRequests(!all, 200)
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := []requestJ{}
	for _, q := range rs {
		j := requestJ{ID: q.ID, Sandbox: q.Sandbox, Folder: q.Worktree, Kind: q.Kind, Path: q.Path, Reason: q.Reason, Status: q.Status,
			Created: q.Created.Unix(), Bytes: q.Bytes, Files: q.Files, Large: access.Large(q)}
		switch q.Kind {
		case "ghtoken":
			var d ghtoken.Detail
			if json.Unmarshal([]byte(q.Extra), &d) == nil {
				j.GitHub = &d
			}
		}
		if q.Kind == "ssh" && q.Status == "pending" {
			if info, ok := s.SSH.PendingInfo(q.ID); ok {
				j.SSH = &sshInfoJ{info.HostKeyFPs, info.Source, info.DefaultKey, info.DefaultUser, info.RequestedUser, info.SuggestedMinutes}
			}
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID           int64  `json:"id"`
		Allow        bool   `json:"allow"`
		ConfirmLarge bool   `json:"confirm_large"`
		Note         string `json:"note"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	msg, err := s.Access.Decide(q.ID, q.Allow, q.ConfirmLarge, q.Note)
	if err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}

func (s *Server) sshDecide(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID               int64  `json:"id"`
		Allow            bool   `json:"allow"`
		Minutes          int    `json:"minutes"`
		Key              string `json:"key_fingerprint"`
		User             string `json:"user"`
		TrustUnknownHost bool   `json:"trust_unknown_host"`
		Passphrase       string `json:"passphrase"` // only for a key that is not loaded in the agent; never stored or logged
		Note             string `json:"note"`       // optional message for the agent
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	msg, err := s.SSH.DecideSSH(broker.Decision{ID: q.ID, Allow: q.Allow, Minutes: q.Minutes, KeyFP: q.Key, User: q.User,
		TrustUnknownHost: q.TrustUnknownHost, Passphrase: []byte(q.Passphrase), Note: q.Note})
	if err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}

func (s *Server) grants(w http.ResponseWriter, r *http.Request) {
	gs, err := s.M.S.ListGrants(r.URL.Query().Get("all") != "1", time.Now())
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := []grantJ{}
	now := time.Now()
	for _, g := range gs {
		out = append(out, grantJ{g.ID, g.Sandbox, g.Worktree, g.Host, g.KeyFP, g.User, g.Expires.Unix(), g.PerConn, g.Revoked,
			!g.Revoked && g.Expires.After(now), g.HostKeys})
	}
	writeJSON(w, 200, out)
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID int64 `json:"id"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.SSH.Revoke(r.Context(), q.ID); err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "entzogen"})
}

func (s *Server) extend(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID      int64 `json:"id"`
		Minutes int   `json:"minutes"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	exp, err := s.SSH.Extend(r.Context(), q.ID, q.Minutes)
	if err != nil {
		fail(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]any{"message": "extended", "expires_unix": exp.Unix()})
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.SSH.KeysAll()
	type k struct {
		Fingerprint string `json:"fingerprint"`
		Comment     string `json:"comment"`
		Type        string `json:"type"`
		Loaded      bool   `json:"loaded"`
		Encrypted   bool   `json:"encrypted"`
	}
	out := []k{}
	for _, x := range ks {
		out = append(out, k{x.Fingerprint, x.Comment, x.Type, x.Loaded, x.Encrypted})
	}
	resp := map[string]any{"keys": out}
	if err != nil {
		resp["error"] = err.Error() // the agent could not be listed; file keys are still offered
	}
	writeJSON(w, 200, resp)
}

func (s *Server) auditEntries(r *http.Request, def int) ([]auditJ, error) {
	n := def
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100000 {
		n = v
	}
	es, err := s.M.S.AuditSearch(n, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		return nil, err
	}
	out := []auditJ{}
	for _, e := range es {
		out = append(out, auditJ{e.TS.Unix(), e.Actor, e.Event, e.Sandbox, e.Worktree, e.Detail})
	}
	return out, nil
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	out, err := s.auditEntries(r, 500)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, out)
}

// csvSafe prevents formula injection when opened in spreadsheets.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

func (s *Server) auditCSV(w http.ResponseWriter, r *http.Request) {
	out, err := s.auditEntries(r, 100000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"zeit", "akteur", "ereignis", "sandbox", "ordner", "details"})
	for _, e := range out {
		_ = cw.Write([]string{time.Unix(e.TS, 0).Format(time.RFC3339), csvSafe(e.Actor), csvSafe(e.Event), csvSafe(e.Sandbox), csvSafe(e.Folder), csvSafe(e.Detail)})
	}
	cw.Flush()
}

func (s *Server) getBlocklist(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Ix.Blocklist())
}

func (s *Server) putBlocklist(w http.ResponseWriter, r *http.Request) {
	var bl index.Blocklist
	if err := decode(r, &bl); err != nil {
		fail(w, 400, err)
		return
	}
	clean := func(in []string) []string {
		out := []string{}
		for _, x := range in {
			if x = strings.TrimSpace(x); x != "" && !strings.ContainsRune(x, 0) {
				out = append(out, x)
			}
		}
		return out
	}
	bl.Paths, bl.Names = clean(bl.Paths), clean(bl.Names)
	// Safety net: mandatory entries must not be removed.
	def := index.DefaultBlocklist()
	for _, must := range def.Paths[:3] { // .ssh, Library, .config
		found := false
		for _, p := range bl.Paths {
			if strings.EqualFold(p, must) {
				found = true
			}
		}
		if !found {
			fail(w, 400, fmt.Errorf("%q is part of the fixed blocklist and cannot be removed", must))
			return
		}
	}
	if err := bl.Save(s.BlocklistFile); err != nil {
		fail(w, 500, err)
		return
	}
	s.Ix.SetBlocklist(bl)
	_ = s.M.S.Audit("app", "blocklist.update", "", "", fmt.Sprintf("%d Pfade, %d Muster", len(bl.Paths), len(bl.Names)))
	writeJSON(w, 200, bl)
}

var _ = store.Request{}

const maxLogBytes = 256 << 10

var limaVMRe = regexp.MustCompile(`^bergfried-[a-z0-9-]{1,40}$`)

// logPath maps a log source to a file: the daemon log, the updater's log or Lima's host-agent log of one of our VMs.
func (s *Server) logPath(source, vm string) (string, error) {
	switch source {
	case "", "daemon":
		return filepath.Join(s.Home, "daemon.log"), nil
	case "update":
		return filepath.Join(s.Home, "update", "update.log"), nil
	case "base":
		fs, _ := filepath.Glob(filepath.Join(s.M.BaseFailureDir(), "*.log"))
		if len(fs) == 0 {
			return filepath.Join(s.M.BaseFailureDir(), "(none)"), nil
		}
		sort.Strings(fs)
		return fs[len(fs)-1], nil // names are timestamps: the newest is last
	case "lima":
		if !limaVMRe.MatchString(vm) {
			return "", fmt.Errorf("invalid VM name")
		}
		lh := os.Getenv("LIMA_HOME")
		if lh == "" {
			h, _ := os.UserHomeDir()
			lh = filepath.Join(h, ".lima")
		}
		return filepath.Join(lh, vm, "ha.stderr.log"), nil
	}
	return "", fmt.Errorf("unknown log source")
}

// readTail returns at most max bytes from the end of the file (starting at a line boundary).
func readTail(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := int64(0)
	if fi.Size() > max {
		off = fi.Size() - max
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return "", err
	}
	txt := string(buf)
	if off > 0 {
		if i := strings.IndexByte(txt, '\n'); i >= 0 {
			txt = txt[i+1:]
		}
	}
	return txt, nil
}

// logTail returns the end of a log file for the app's Diagnostics pane (only the app socket, owner-only).
func (s *Server) logTail(w http.ResponseWriter, r *http.Request) {
	p, err := s.logPath(r.URL.Query().Get("source"), r.URL.Query().Get("vm"))
	if err != nil {
		fail(w, 400, err)
		return
	}
	txt, err := readTail(p, maxLogBytes)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]string{"path": p, "text": ""})
			return
		}
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"path": p, "text": txt})
}
