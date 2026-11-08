// Package api: HTTP/JSON-Schnittstelle der Control-App (nur lokaler Unix-Socket, 0600).
// Lange Aktionen laufen als Jobs; die App fragt den Status ab.
package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/access"
	"github.com/DevertNet/bergfried/daemon/internal/broker"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/session"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const Version = "0.4.0"

type Server struct {
	M             *session.Manager // Actor "app"
	Access        *access.Service
	SSH           *broker.Service
	Ix            *index.Index
	BlocklistFile string
	Upstream      string

	mu   sync.Mutex
	jobs map[int64]*Job
	next int64
}

type Job struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"` // running | done | failed
	Error string `json:"error,omitempty"`
}

func (s *Server) job(kind string, f func(ctx context.Context) error) *Job {
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = map[int64]*Job{}
	}
	s.next++
	j := &Job{ID: s.next, Kind: kind, State: "running"}
	s.jobs[j.ID] = j
	s.mu.Unlock()
	go func() {
		err := f(context.Background())
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

type agentJ struct {
	Name     string `json:"name"`
	Worktree string `json:"worktree"`
}
type sessionJ struct {
	Name            string   `json:"name"`
	VM              string   `json:"vm"`
	Status          string   `json:"status"`
	Repo            string   `json:"repo"`
	TemplateVersion int      `json:"template_version"`
	Created         int64    `json:"created_unix"`
	CPUs            int      `json:"cpus"`
	Memory          int64    `json:"memory"`
	Disk            int64    `json:"disk"`
	Agents          []agentJ `json:"agents"`
	ActiveGrants    int      `json:"active_grants"`
	Pending         int      `json:"pending_requests"`
}
type sshInfoJ struct {
	HostKeyFPs       []string `json:"host_key_fingerprints"`
	Source           string   `json:"source"`
	DefaultKey       string   `json:"default_key"`
	DefaultUser      string   `json:"default_user"`
	SuggestedMinutes int      `json:"suggested_minutes"`
}
type requestJ struct {
	ID      int64     `json:"id"`
	Session string    `json:"session"`
	Agent   string    `json:"agent"`
	Kind    string    `json:"kind"`
	Path    string    `json:"path"`
	Reason  string    `json:"reason"`
	Status  string    `json:"status"`
	Created int64     `json:"created_unix"`
	Bytes   int64     `json:"bytes"`
	Files   int64     `json:"files"`
	Large   bool      `json:"large"`
	SSH     *sshInfoJ `json:"ssh,omitempty"`
}
type grantJ struct {
	ID         int64    `json:"id"`
	Session    string   `json:"session"`
	Agent      string   `json:"agent"`
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
	Session string `json:"session"`
	Agent   string `json:"agent"`
	Detail  string `json:"detail"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("GET /v1/sessions", s.sessions)
	mux.HandleFunc("POST /v1/sessions/create", s.create)
	mux.HandleFunc("POST /v1/sessions/start", s.simple("session.start", func(ctx context.Context, n string, _ bool) error { return s.M.Start(ctx, n) }))
	mux.HandleFunc("POST /v1/sessions/stop", s.simple("session.stop", func(ctx context.Context, n string, _ bool) error { return s.M.Stop(ctx, n) }))
	mux.HandleFunc("POST /v1/sessions/delete", s.simple("session.delete", func(ctx context.Context, n string, f bool) error { return s.M.Delete(ctx, n, f) }))
	mux.HandleFunc("POST /v1/agents/add", s.agentAdd)
	mux.HandleFunc("POST /v1/base/build", s.baseBuild)
	mux.HandleFunc("GET /v1/jobs/{id}", s.jobGet)
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

// Serve lauscht auf dem Unix-Socket (Verzeichnis 0700, Socket 0600).
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
		if v.Name == session.BaseName {
			base = true
		}
	}
	ss, _ := s.M.S.ListSessions()
	writeJSON(w, 200, map[string]any{"version": Version, "base_ready": base, "base_name": session.BaseName,
		"template_version": session.TemplateVersion, "index_ready": s.Ix.Ready(), "upstream_agent": s.Upstream != "", "sessions": len(ss)})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	ss, err := s.M.S.ListSessions()
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
	out := []sessionJ{}
	for _, x := range ss {
		j := sessionJ{Name: x.Name, VM: x.VM, Repo: x.Repo, TemplateVersion: x.TemplateVersion, Created: x.Created.Unix(), Status: "fehlt", Agents: []agentJ{}}
		if i, ok := byVM[x.VM]; ok {
			v := vms[i]
			j.Status, j.CPUs, j.Memory, j.Disk = v.Status, v.CPUs, v.Memory, v.Disk
		}
		ags, _ := s.M.S.ListAgents(x.Name)
		for _, a := range ags {
			j.Agents = append(j.Agents, agentJ{a.Name, a.Worktree})
		}
		for _, g := range grants {
			if g.Session == x.Name {
				j.ActiveGrants++
			}
		}
		for _, q := range pend {
			if q.Session == x.Name {
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
	Session string `json:"session"`
	Agent   string `json:"agent"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if err := session.ValidName(q.Name); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 202, s.job("session.create", func(ctx context.Context) error { return s.M.Create(ctx, q.Name, q.Repo) }))
}

func (s *Server) simple(kind string, f func(context.Context, string, bool) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var q nameReq
		if err := decode(r, &q); err != nil {
			fail(w, 400, err)
			return
		}
		if err := session.ValidName(q.Name); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 202, s.job(kind, func(ctx context.Context) error { return f(ctx, q.Name, q.Force) }))
	}
}

func (s *Server) agentAdd(w http.ResponseWriter, r *http.Request) {
	var q nameReq
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	if session.ValidName(q.Session) != nil || session.ValidName(q.Agent) != nil {
		fail(w, 400, fmt.Errorf("ungültiger Name"))
		return
	}
	writeJSON(w, 202, s.job("agent.add", func(ctx context.Context) error { return s.M.AgentAdd(ctx, q.Session, q.Agent) }))
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
	}
	s.mu.Unlock()
	if !ok {
		fail(w, 404, fmt.Errorf("Job unbekannt"))
		return
	}
	writeJSON(w, 200, cp)
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
		j := requestJ{ID: q.ID, Session: q.Session, Agent: q.Agent, Kind: q.Kind, Path: q.Path, Reason: q.Reason, Status: q.Status,
			Created: q.Created.Unix(), Bytes: q.Bytes, Files: q.Files, Large: access.Large(q)}
		if q.Kind == "ssh" && q.Status == "pending" {
			if info, ok := s.SSH.PendingInfo(q.ID); ok {
				j.SSH = &sshInfoJ{info.HostKeyFPs, info.Source, info.DefaultKey, info.DefaultUser, info.SuggestedMinutes}
			}
		}
		out = append(out, j)
	}
	writeJSON(w, 200, out)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	var q struct {
		ID           int64 `json:"id"`
		Allow        bool  `json:"allow"`
		ConfirmLarge bool  `json:"confirm_large"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	msg, err := s.Access.Decide(q.ID, q.Allow, q.ConfirmLarge)
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
		AllAgents        bool   `json:"all_agents"`
		PerConn          bool   `json:"per_connection"`
		TrustUnknownHost bool   `json:"trust_unknown_host"`
	}
	if err := decode(r, &q); err != nil {
		fail(w, 400, err)
		return
	}
	msg, err := s.SSH.DecideSSH(broker.Decision{ID: q.ID, Allow: q.Allow, Minutes: q.Minutes, KeyFP: q.Key, User: q.User,
		AllAgents: q.AllAgents, PerConn: q.PerConn, TrustUnknownHost: q.TrustUnknownHost})
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
		out = append(out, grantJ{g.ID, g.Session, g.Agent, g.Host, g.KeyFP, g.User, g.Expires.Unix(), g.PerConn, g.Revoked,
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
	writeJSON(w, 200, map[string]any{"message": "verlängert", "expires_unix": exp.Unix()})
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	ks, err := s.SSH.Keys()
	if err != nil {
		writeJSON(w, 200, map[string]any{"keys": []any{}, "error": err.Error()})
		return
	}
	type k struct {
		Fingerprint string `json:"fingerprint"`
		Comment     string `json:"comment"`
		Type        string `json:"type"`
	}
	out := []k{}
	for _, x := range ks {
		out = append(out, k{x.Fingerprint, x.Comment, x.Type})
	}
	writeJSON(w, 200, map[string]any{"keys": out})
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
		out = append(out, auditJ{e.TS.Unix(), e.Actor, e.Event, e.Session, e.Agent, e.Detail})
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

// csvSafe verhindert Formel-Injektion beim Öffnen in Tabellenkalkulationen.
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
	_ = cw.Write([]string{"zeit", "akteur", "ereignis", "session", "agent", "details"})
	for _, e := range out {
		_ = cw.Write([]string{time.Unix(e.TS, 0).Format(time.RFC3339), csvSafe(e.Actor), csvSafe(e.Event), csvSafe(e.Session), csvSafe(e.Agent), csvSafe(e.Detail)})
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
	// Sicherheitsnetz: Pflichteinträge dürfen nicht entfernt werden.
	def := index.DefaultBlocklist()
	for _, must := range def.Paths[:4] { // .ssh, Library, .config, .mcp-einfach123-secrets
		found := false
		for _, p := range bl.Paths {
			if strings.EqualFold(p, must) {
				found = true
			}
		}
		if !found {
			fail(w, 400, fmt.Errorf("%q gehört zur festen Sperrliste und kann nicht entfernt werden", must))
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
