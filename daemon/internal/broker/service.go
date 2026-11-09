package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"golang.org/x/crypto/ssh"
)

var (
	hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

const MaxMinutes = 7 * 24 * 60

// HostKeyResolver returns the host key fingerprints of a target and where they come from
// ("known_hosts" = already trusted by the user, "keyscan" = unconfirmed).
type HostKeyResolver func(ctx context.Context, host string) (fps []string, source string, err error)

// VMHooks act on the VM (write SSH config, terminate running ssh processes).
type VMHooks interface {
	Apply(ctx context.Context, sandbox string, active []store.SSHGrant) error
	KillSSH(ctx context.Context, sandbox string) error
}

type SSHInfo struct {
	RequestedUser           string // user requested by the agent (may be empty)
	HostKeyFPs              []string
	Source                  string
	DefaultKey, DefaultUser string
	SuggestedMinutes        int
}

type SSHAsker interface {
	AskSSH(r store.Request, info SSHInfo)
}

type Decision struct {
	ID               int64
	Allow            bool
	Minutes          int
	KeyFP, User      string
	AllAgents        bool
	PerConn          bool
	TrustUnknownHost bool
}

type pending struct {
	ch   chan *store.SSHGrant // nil = abgelehnt
	info SSHInfo
}

type Service struct {
	B       *Broker
	Resolve HostKeyResolver
	Hooks   VMHooks
	Asker   SSHAsker
	Hosts   *HostConfig
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]*pending
	open    map[int64]string         // active grants -> sandbox (for expiry detection)
	cool    map[string]time.Time     // sandbox|host -> earliest time to ask again (after denial/expiry)
	attempt map[string]*attemptState // sandbox|host -> running request (background goroutine active)
}

// AttemptCooldown: after a denied or expired attempt the same host is not reported again for a while.
const AttemptCooldown = 60 * time.Second

func NewService(b *Broker, r HostKeyResolver, h VMHooks, a SSHAsker, hc *HostConfig) *Service {
	return &Service{B: b, Resolve: r, Hooks: h, Asker: a, Hosts: hc, Timeout: 10 * time.Minute,
		waiters: map[int64]*pending{}, open: map[int64]string{}, cool: map[string]time.Time{}, attempt: map[string]*attemptState{}}
}

func (s *Service) audit(event, sandbox, agent, detail string) {
	if err := s.B.St.Audit("daemon", event, sandbox, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

func clamp(m int) int {
	if m < 1 {
		return 15
	}
	if m > MaxMinutes {
		return MaxMinutes
	}
	return m
}

func (s *Service) RequestSSH(ctx context.Context, sandbox, agent, host, user string, minutes int, reason string) (*pb.SSHAccessResult, error) {
	deny := func(v pb.Verdict, msg string) (*pb.SSHAccessResult, error) {
		return &pb.SSHAccessResult{Verdict: v, Message: msg}, nil
	}
	if !hostRe.MatchString(host) {
		s.audit("ssh.request_invalid", sandbox, agent, fmt.Sprintf("host=%q", trunc(host, 100)))
		return deny(pb.Verdict_VERDICT_DENIED, "Invalid host name.")
	}
	host = strings.ToLower(host)
	user = strings.TrimSpace(user)
	if user != "" && !userRe.MatchString(user) {
		s.audit("ssh.request_invalid", sandbox, agent, fmt.Sprintf("user=%q", trunc(user, 40)))
		return deny(pb.Verdict_VERDICT_DENIED, "Invalid user name (allowed: a-z, 0-9, _ and -, lowercase).")
	}
	reason = trunc(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, reason), 500)
	fps, source, err := s.Resolve(ctx, host)
	if err != nil || len(fps) == 0 {
		s.audit("ssh.request_nohostkey", sandbox, agent, fmt.Sprintf("host=%s err=%v", host, err))
		return deny(pb.Verdict_VERDICT_DENIED, "Host key of the target could not be determined (not in known_hosts, not reachable).")
	}
	dk, du := s.Hosts.Default(host)
	info := SSHInfo{HostKeyFPs: fps, Source: source, DefaultKey: dk, DefaultUser: du, SuggestedMinutes: clamp(minutes), RequestedUser: user}
	extra, _ := json.Marshal(map[string]any{"fps": fps, "source": source, "user": user})
	req := store.Request{Sandbox: sandbox, Agent: agent, Kind: "ssh", Path: host, Reason: reason, Status: "pending", Bytes: int64(info.SuggestedMinutes), Extra: string(extra)}
	id, err := s.B.St.AddRequest(req)
	if err != nil {
		return nil, err
	}
	req.ID = id
	p := &pending{ch: make(chan *store.SSHGrant, 1), info: info}
	s.mu.Lock()
	s.waiters[id] = p
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	s.audit("ssh.request_created", sandbox, agent, fmt.Sprintf("id=%d host=%s user=%s source=%s hostkeys=%s", id, host, user, source, strings.Join(fps, ",")))
	if s.Asker != nil {
		s.Asker.AskSSH(req, info)
	}
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case g := <-p.ch:
		if g == nil {
			return deny(pb.Verdict_VERDICT_DENIED, "The user declined.")
		}
		s.mu.Lock()
		s.open[g.ID] = g.Sandbox
		s.mu.Unlock()
		s.apply(ctx, g.Sandbox)
		return &pb.SSHAccessResult{Verdict: pb.Verdict_VERDICT_ALLOWED, ExpiresUnix: g.Expires.Unix(), User: g.User,
			Message: fmt.Sprintf("Approved until %s. Connect with: ssh %s@%s", g.Expires.Format("15:04"), g.User, host)}, nil
	case <-timer.C:
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_expired", sandbox, agent, fmt.Sprintf("id=%d", id))
		}
		return deny(pb.Verdict_VERDICT_EXPIRED, "No answer, request expired.")
	case <-ctx.Done():
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_cancelled", sandbox, agent, fmt.Sprintf("id=%d", id))
		}
		return nil, ctx.Err()
	}
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Keys lists the keys of the upstream agent (public parts only).
func (s *Service) Keys() ([]*pb.KeyInfo, error) {
	ks, err := s.B.Up.List()
	if err != nil {
		return nil, err
	}
	var out []*pb.KeyInfo
	for _, k := range ks {
		out = append(out, &pb.KeyInfo{Fingerprint: ssh.FingerprintSHA256(k), Comment: k.Comment, Type: k.Type()})
	}
	return out, nil
}

// DecideSSH decides an open SSH request and creates the grant on approval.
func (s *Service) DecideSSH(d Decision) (string, error) {
	r, err := s.B.St.GetRequest(d.ID)
	if err != nil {
		return "", err
	}
	if r.Kind != "ssh" || r.Status != "pending" {
		return "", fmt.Errorf("request %d is not an open SSH request", d.ID)
	}
	s.mu.Lock()
	p, ok := s.waiters[d.ID]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("request %d is no longer waiting (agent gave up)", d.ID)
	}
	if !d.Allow {
		if ok, _ := s.B.St.SetRequestStatus(d.ID, "denied"); !ok {
			return "", errors.New("request no longer open")
		}
		s.audit("ssh.request_denied", r.Sandbox, r.Agent, fmt.Sprintf("id=%d", d.ID))
		p.ch <- nil
		return "denied", nil
	}
	if d.Minutes < 1 || d.Minutes > MaxMinutes {
		return "", fmt.Errorf("duration must be 1..%d minutes", MaxMinutes)
	}
	if d.PerConn && s.B.ConfirmEach == nil {
		// Without a confirmation path every connection would be refused (the grant would be worthless).
		return "", errors.New("\"confirm every connection\" is not yet available without the macOS dialog (serve without --no-dialog)")
	}
	if p.info.Source != "known_hosts" && !d.TrustUnknownHost {
		return "", fmt.Errorf("host key of %s is new (%s) – confirm with --trust-host-key", r.Path, strings.Join(p.info.HostKeyFPs, ", "))
	}
	// Precedence: user input, then the agent's request, then the host default.
	user := d.User
	if user == "" {
		user = p.info.RequestedUser
	}
	if user == "" {
		user = p.info.DefaultUser
	}
	if !userRe.MatchString(user) {
		return "", errors.New("invalid user name")
	}
	keyFP := d.KeyFP
	if keyFP == "" {
		keyFP = p.info.DefaultKey
	}
	ks, err := s.B.Up.List()
	if err != nil {
		return "", err
	}
	found := false
	for _, k := range ks {
		if ssh.FingerprintSHA256(k) == keyFP {
			found = true
		}
	}
	if !found {
		return "", errors.New("key not in the upstream agent (give --key SHA256:…, see `ssh keys`)")
	}
	if ok, _ := s.B.St.SetRequestStatus(d.ID, "allowed"); !ok {
		return "", errors.New("request no longer open")
	}
	agentName := r.Agent
	if d.AllAgents {
		agentName = ""
	}
	g := store.SSHGrant{Sandbox: r.Sandbox, Agent: agentName, Host: r.Path, KeyFP: keyFP, User: user, HostKeys: p.info.HostKeyFPs,
		Expires: s.B.now().Add(time.Duration(d.Minutes) * time.Minute), PerConn: d.PerConn}
	gid, err := s.B.St.AddGrant(g)
	if err != nil {
		return "", err
	}
	g.ID = gid
	s.Hosts.Remember(r.Path, keyFP, user)
	s.audit("ssh.grant_created", r.Sandbox, r.Agent, fmt.Sprintf("grant=%d request=%d host=%s key=%s user=%s minutes=%d all_agents=%v per_conn=%v", gid, d.ID, r.Path, keyFP, user, d.Minutes, d.AllAgents, d.PerConn))
	p.ch <- &g
	return fmt.Sprintf("allowed (grant %d)", gid), nil
}

func (s *Service) apply(ctx context.Context, sandbox string) {
	if s.Hooks == nil {
		return
	}
	gs := s.B.active(sandbox)
	if err := s.Hooks.Apply(ctx, sandbox, gs); err != nil {
		s.audit("ssh.vm_config_failed", sandbox, "", err.Error())
	}
}

func (s *Service) Revoke(ctx context.Context, id int64) error {
	g, err := s.B.St.GetGrant(id)
	if err != nil {
		return err
	}
	if err := s.B.St.RevokeGrant(id); err != nil {
		return err
	}
	s.audit("ssh.grant_revoked", g.Sandbox, g.Agent, fmt.Sprintf("grant=%d host=%s", id, g.Host))
	s.cleanup(ctx, g.Sandbox)
	s.mu.Lock()
	delete(s.open, id)
	s.mu.Unlock()
	return nil
}

func (s *Service) Extend(ctx context.Context, id int64, minutes int) (time.Time, error) {
	g, err := s.B.St.GetGrant(id)
	if err != nil {
		return time.Time{}, err
	}
	if g.Revoked {
		return time.Time{}, errors.New("grant was revoked – a new request is needed")
	}
	if minutes < 1 || minutes > MaxMinutes {
		return time.Time{}, fmt.Errorf("duration must be 1..%d minutes", MaxMinutes)
	}
	base := g.Expires
	if now := s.B.now(); base.Before(now) {
		base = now
	}
	exp := base.Add(time.Duration(minutes) * time.Minute)
	if err := s.B.St.SetGrantExpiry(id, exp); err != nil {
		return time.Time{}, err
	}
	s.audit("ssh.grant_extended", g.Sandbox, g.Agent, fmt.Sprintf("grant=%d until=%s", id, exp.Format(time.RFC3339)))
	s.mu.Lock()
	s.open[id] = g.Sandbox
	s.mu.Unlock()
	s.apply(ctx, g.Sandbox)
	return exp, nil
}

// cleanup: rewrite the SSH config and terminate running ssh processes of the sandbox.
func (s *Service) cleanup(ctx context.Context, sandbox string) {
	s.apply(ctx, sandbox)
	if s.Hooks != nil {
		if err := s.Hooks.KillSSH(ctx, sandbox); err != nil {
			s.audit("ssh.kill_failed", sandbox, "", err.Error())
		}
	}
}

// Sweep detects expired grants and cleans up. Called regularly.
func (s *Service) Sweep(ctx context.Context) {
	active, err := s.B.St.ListGrants(true, s.B.now())
	if err != nil {
		return
	}
	still := map[int64]string{}
	for _, g := range active {
		still[g.ID] = g.Sandbox
	}
	s.mu.Lock()
	var gone []string
	for id, sess := range s.open {
		if _, ok := still[id]; !ok {
			gone = append(gone, sess)
			delete(s.open, id)
			s.audit("ssh.grant_expired", sess, "", fmt.Sprintf("grant=%d", id))
		}
	}
	for id, sess := range still {
		s.open[id] = sess
	}
	s.mu.Unlock()
	seen := map[string]bool{}
	for _, sess := range gone {
		if !seen[sess] {
			seen[sess] = true
			s.cleanup(ctx, sess)
		}
	}
}

// RenderSSHConfig produces the managed config block of the VM (validated values only).
func RenderSSHConfig(active []store.SSHGrant) string {
	var b strings.Builder
	b.WriteString("# Managed by Bergfried – changes are lost.\n")
	seen := map[string]bool{}
	for _, g := range active {
		if !hostRe.MatchString(g.Host) || !userRe.MatchString(g.User) || seen[g.Host] {
			continue
		}
		seen[g.Host] = true
		h, port, _ := strings.Cut(g.Host, ":")
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  User %s\n", h, h, g.User)
		if port != "" {
			fmt.Fprintf(&b, "  Port %s\n", port)
		}
	}
	return b.String()
}

// ---- host key resolution (Mac) ----

func parseKeyLines(out []byte) []string {
	var fps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f[1] + " " + f[2]))
		if err != nil {
			continue
		}
		fps = append(fps, ssh.FingerprintSHA256(k))
	}
	return fps
}

// ResolveHostKeys: first the Mac's known_hosts (`ssh-keygen -F`), otherwise `ssh-keyscan`.
func ResolveHostKeys(knownHosts string) HostKeyResolver {
	return func(ctx context.Context, host string) ([]string, string, error) {
		h, port, _ := strings.Cut(host, ":")
		query := h
		if port != "" && port != "22" {
			query = "[" + h + "]:" + port
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(cctx, "ssh-keygen", "-F", query, "-f", knownHosts).Output(); err == nil {
			if fps := parseKeyLines(out); len(fps) > 0 {
				return fps, "known_hosts", nil
			}
		}
		args := []string{"-T", "5"}
		if port != "" {
			args = append(args, "-p", port)
		}
		out, err := exec.CommandContext(cctx, "ssh-keyscan", append(args, h)...).Output()
		if err != nil {
			return nil, "", err
		}
		return parseKeyLines(out), "keyscan", nil
	}
}

// ---- default key and user per host ----

type HostConfig struct {
	mu   sync.Mutex
	file string
	m    map[string]struct{ Key, User string }
}

func LoadHostConfig(file string) (*HostConfig, error) {
	hc := &HostConfig{file: file, m: map[string]struct{ Key, User string }{}}
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return hc, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]struct{ Key, User string }
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	hc.m = raw
	return hc, nil
}

func (h *HostConfig) Default(host string) (key, user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.m[host]
	user = v.User
	if user == "" {
		user = "claude"
	}
	return v.Key, user
}

func (h *HostConfig) Remember(host, key, user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.m[host] = struct{ Key, User string }{key, user}
	b, err := json.MarshalIndent(h.m, "", "  ")
	if err == nil && h.file != "" {
		_ = os.WriteFile(h.file, b, 0o600)
	}
}

// PendingInfo returns the decision aids of an open SSH request.
func (s *Service) PendingInfo(id int64) (SSHInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.waiters[id]
	if !ok {
		return SSHInfo{}, false
	}
	return p.info, true
}

type attemptState struct {
	done    chan struct{}
	verdict pb.Verdict
	expires int64
}

// Attempt is called by the VM's ssh hook when an ssh command starts there. If there is no active grant for the host
// and no open request, a request is created in the app in the background. The ssh call itself is left untouched.
// With wait=true the call blocks until the request is decided (interactive ssh then waits for the approval);
// ctx aborts the wait (e.g. Ctrl-C), the request itself stays open.
func (s *Service) Attempt(ctx context.Context, sandbox, host, port, user string, wait bool) (state, msg string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if port != "" && port != "22" {
		host += ":" + port
	}
	if !hostRe.MatchString(host) {
		return "ignored", ""
	}
	if user != "" && !userRe.MatchString(user) {
		user = ""
	}
	// active grant present? Then do nothing.
	for _, g := range s.B.active(sandbox) {
		if g.Host == host {
			return "granted", ""
		}
	}
	key := sandbox + "|" + host
	s.mu.Lock()
	st := s.attempt[key]
	started := false
	if st == nil {
		if until, ok := s.cool[key]; ok && s.B.now().Before(until) {
			s.mu.Unlock()
			return "cooldown", "Bergfried: SSH approval for " + host + " was not granted recently."
		}
		st = &attemptState{done: make(chan struct{})}
		s.attempt[key] = st
		started = true
	}
	s.mu.Unlock()
	if started {
		go func() {
			res, err := s.RequestSSH(context.Background(), sandbox, "terminal", host, user, 15, "SSH connection attempt in the VM (ssh "+host+")")
			s.mu.Lock()
			delete(s.attempt, key)
			if err != nil || res == nil {
				st.verdict = pb.Verdict_VERDICT_DENIED
			} else {
				st.verdict, st.expires = res.Verdict, res.ExpiresUnix
			}
			if st.verdict != pb.Verdict_VERDICT_ALLOWED {
				s.cool[key] = s.B.now().Add(AttemptCooldown)
			} else {
				delete(s.cool, key)
			}
			s.mu.Unlock()
			close(st.done)
		}()
		s.audit("ssh.attempt", sandbox, "terminal", fmt.Sprintf("host=%s user=%s wait=%v", host, user, wait))
	}
	if !wait {
		if started {
			return "pending", "Bergfried: SSH approval for " + host + " requested – please confirm in the app, then repeat the command."
		}
		return "pending", "Bergfried: SSH approval for " + host + " is already requested – please confirm in the app."
	}
	select {
	case <-st.done:
		if st.verdict == pb.Verdict_VERDICT_ALLOWED {
			return "allowed", "Bergfried: SSH approval for " + host + " granted – connecting …"
		}
		return "denied", "Bergfried: SSH approval for " + host + " not granted."
	case <-ctx.Done():
		return "aborted", ""
	}
}
