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
	TrustUnknownHost bool
	// Passphrase of a key that is not loaded in the upstream agent. Used once to decrypt the key file for this grant; the caller's
	// slice is zeroed by DecideSSH. Never stored, logged or audited.
	Passphrase []byte
	// Note is an optional message from the user that is returned to the agent with the decision.
	Note string
}

type pending struct {
	ch   chan pendingResult
	info SSHInfo
}

type pendingResult struct {
	grant *store.SSHGrant // nil = declined
	note  string
}

type Service struct {
	B       *Broker
	Resolve HostKeyResolver
	Hooks   VMHooks
	Asker   SSHAsker
	Hosts   *HostConfig
	Timeout time.Duration
	SSHDir  string // ~/.ssh: key files that are not loaded in the agent can be granted with their passphrase (see localkeys.go)

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

func (s *Service) audit(event, sandbox, worktree, detail string) {
	if err := s.B.St.Audit("daemon", event, sandbox, worktree, detail); err != nil {
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

func (s *Service) RequestSSH(ctx context.Context, sandbox, worktree, host, user string, minutes int, reason string) (*pb.SSHAccessResult, error) {
	deny := func(v pb.Verdict, msg string) (*pb.SSHAccessResult, error) {
		return &pb.SSHAccessResult{Verdict: v, Message: msg}, nil
	}
	if !hostRe.MatchString(host) {
		s.audit("ssh.request_invalid", sandbox, worktree, fmt.Sprintf("host=%q", trunc(host, 100)))
		return deny(pb.Verdict_VERDICT_DENIED, "Invalid host name.")
	}
	host = strings.ToLower(host)
	user = strings.TrimSpace(user)
	if user != "" && !userRe.MatchString(user) {
		s.audit("ssh.request_invalid", sandbox, worktree, fmt.Sprintf("user=%q", trunc(user, 40)))
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
		s.audit("ssh.request_nohostkey", sandbox, worktree, fmt.Sprintf("host=%s err=%v", host, err))
		return deny(pb.Verdict_VERDICT_DENIED, "Host key of the target could not be determined (not in known_hosts, not reachable).")
	}
	dk, du := s.Hosts.Default(host)
	info := SSHInfo{HostKeyFPs: fps, Source: source, DefaultKey: dk, DefaultUser: du, SuggestedMinutes: clamp(minutes), RequestedUser: user}
	extra, _ := json.Marshal(map[string]any{"fps": fps, "source": source, "user": user})
	req := store.Request{Sandbox: sandbox, Worktree: worktree, Kind: "ssh", Path: host, Reason: reason, Status: "pending", Bytes: int64(info.SuggestedMinutes), Extra: string(extra)}
	id, err := s.B.St.AddRequest(req)
	if err != nil {
		return nil, err
	}
	req.ID = id
	p := &pending{ch: make(chan pendingResult, 1), info: info}
	s.mu.Lock()
	s.waiters[id] = p
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	s.audit("ssh.request_created", sandbox, worktree, fmt.Sprintf("id=%d host=%s user=%s source=%s hostkeys=%s", id, host, user, source, strings.Join(fps, ",")))
	if s.Asker != nil {
		s.Asker.AskSSH(req, info)
	}
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		if r.grant == nil {
			msg := "The user declined."
			if r.note != "" {
				msg += " Note from the user: " + r.note
			}
			return deny(pb.Verdict_VERDICT_DENIED, msg)
		}
		g := r.grant
		s.mu.Lock()
		s.open[g.ID] = g.Sandbox
		s.mu.Unlock()
		s.apply(ctx, g.Sandbox)
		msg := fmt.Sprintf("Approved until %s. Connect with: ssh %s@%s", g.Expires.Format("15:04"), g.User, host)
		if r.note != "" {
			msg += " Note from the user: " + r.note
		}
		msg += fmt.Sprintf(" task_id=%d. When the task this access was requested for is finished, call release_ssh with this task_id.", g.ID)
		return &pb.SSHAccessResult{Verdict: pb.Verdict_VERDICT_ALLOWED, ExpiresUnix: g.Expires.Unix(), User: g.User, TaskId: g.ID, Message: msg}, nil
	case <-timer.C:
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_expired", sandbox, worktree, fmt.Sprintf("id=%d", id))
		}
		return deny(pb.Verdict_VERDICT_EXPIRED, "No answer, request expired.")
	case <-ctx.Done():
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_cancelled", sandbox, worktree, fmt.Sprintf("id=%d", id))
		}
		return nil, ctx.Err()
	}
}

// cleanNote limits the user's note and removes control characters (newlines stay).
func cleanNote(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	return trunc(s, 500)
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

// KeysAll lists the keys of the upstream agent (Loaded) and key pairs from SSHDir that are not loaded in the agent.
// If the agent cannot be listed, the file keys are still returned together with the error.
func (s *Service) KeysAll() ([]KeyEntry, error) {
	var out []KeyEntry
	seen := map[string]bool{}
	ks, upErr := s.B.Up.List()
	for _, k := range ks {
		fp := ssh.FingerprintSHA256(k)
		seen[fp] = true
		out = append(out, KeyEntry{Fingerprint: fp, Comment: k.Comment, Type: k.Type(), Loaded: true})
	}
	if s.SSHDir != "" {
		for _, lk := range scanLocalKeys(s.SSHDir) {
			if !seen[lk.entry.Fingerprint] {
				seen[lk.entry.Fingerprint] = true
				out = append(out, lk.entry)
			}
		}
	}
	return out, upErr
}

func (s *Service) localKeyPath(fp string) string {
	if s.SSHDir == "" {
		return ""
	}
	for _, lk := range scanLocalKeys(s.SSHDir) {
		if lk.entry.Fingerprint == fp {
			return lk.path
		}
	}
	return ""
}

// DecideSSH decides an open SSH request and creates the grant on approval.
func (s *Service) DecideSSH(d Decision) (string, error) {
	defer clear(d.Passphrase)
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
		return "", fmt.Errorf("request %d is no longer waiting (the requester gave up)", d.ID)
	}
	if !d.Allow {
		if ok, _ := s.B.St.SetRequestStatus(d.ID, "denied"); !ok {
			return "", errors.New("request no longer open")
		}
		s.audit("ssh.request_denied", r.Sandbox, r.Worktree, fmt.Sprintf("id=%d", d.ID))
		p.ch <- pendingResult{note: cleanNote(d.Note)}
		return "denied", nil
	}
	if d.Minutes < 1 || d.Minutes > MaxMinutes {
		return "", fmt.Errorf("duration must be 1..%d minutes", MaxMinutes)
	}
	if p.info.Source != "known_hosts" && !d.TrustUnknownHost {
		return "", fmt.Errorf("host key of %s is new (%s) – confirm with --trust-host-key", r.Path, strings.Join(p.info.HostKeyFPs, ", "))
	}
	// Precedence: user input, then the requested user, then the host default.
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
	ks, upErr := s.B.Up.List()
	found := false
	for _, k := range ks {
		if ssh.FingerprintSHA256(k) == keyFP {
			found = true
		}
	}
	// A key that is not loaded in the agent can still be granted if its key file is in ~/.ssh: the daemon decrypts it with the passphrase
	// given now and holds it in memory until the grant ends. A wrong passphrase leaves the request open.
	var local *localSigner
	if !found {
		path := s.localKeyPath(keyFP)
		switch {
		case path != "":
			ls, err := loadLocalSigner(path, keyFP, d.Passphrase)
			if err != nil {
				return "", err
			}
			local = ls
		case upErr != nil:
			return "", upErr
		default:
			return "", errors.New("key not in the upstream agent and no key file for it in ~/.ssh (give --key SHA256:…, see `ssh keys`)")
		}
	}
	if ok, _ := s.B.St.SetRequestStatus(d.ID, "allowed"); !ok {
		return "", errors.New("request no longer open")
	}
	g := store.SSHGrant{Sandbox: r.Sandbox, Worktree: r.Worktree, Host: r.Path, KeyFP: keyFP, User: user, HostKeys: p.info.HostKeyFPs,
		Expires: s.B.now().Add(time.Duration(d.Minutes) * time.Minute)}
	gid, err := s.B.St.AddGrant(g)
	if err != nil {
		return "", err
	}
	g.ID = gid
	source := "agent"
	if local != nil {
		s.B.setLocal(gid, local)
		source = "key file, decrypted in memory until the grant ends (not loaded into the agent, passphrase not stored)"
	}
	s.Hosts.Remember(r.Path, keyFP, user)
	s.audit("ssh.grant_created", r.Sandbox, r.Worktree, fmt.Sprintf("grant=%d request=%d host=%s key=%s source=%s user=%s minutes=%d", gid, d.ID, r.Path, keyFP, source, user, d.Minutes))
	p.ch <- pendingResult{grant: &g, note: cleanNote(d.Note)}
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
	s.B.DropLocal(id)
	s.audit("ssh.grant_revoked", g.Sandbox, g.Worktree, fmt.Sprintf("grant=%d host=%s", id, g.Host))
	s.cleanup(ctx, g.Sandbox)
	s.mu.Lock()
	delete(s.open, id)
	s.mu.Unlock()
	return nil
}

// Release ends the grant of a finished task. Only a grant of the calling sandbox can be released; an unknown id and an id
// of another sandbox get the same answer (do not reveal that the id exists elsewhere).
func (s *Service) Release(ctx context.Context, sandbox string, taskID int64) (*pb.SSHReleaseResult, error) {
	g, err := s.B.St.GetGrant(taskID)
	if err != nil || g.Sandbox != sandbox {
		msg := fmt.Sprintf("No SSH task %d in this sandbox.", taskID)
		active, _ := s.B.St.ListGrants(true, s.B.now())
		var own []string
		for _, ag := range active {
			if ag.Sandbox == sandbox {
				own = append(own, fmt.Sprintf("task_id=%d host=%s user=%s", ag.ID, ag.Host, ag.User))
			}
		}
		if len(own) > 0 {
			msg += " Active tasks: " + strings.Join(own, "; ")
		} else {
			msg += " There are no active SSH tasks."
		}
		s.audit("ssh.release_unknown", sandbox, "", fmt.Sprintf("id=%d", taskID))
		return &pb.SSHReleaseResult{Released: false, Message: msg}, nil
	}
	if g.Revoked || !g.Expires.After(s.B.now()) {
		return &pb.SSHReleaseResult{Released: false, Message: fmt.Sprintf("SSH task %d already ended.", taskID)}, nil
	}
	if err := s.Revoke(ctx, taskID); err != nil {
		return nil, err
	}
	s.audit("ssh.task_released", sandbox, g.Worktree, fmt.Sprintf("grant=%d host=%s", taskID, g.Host))
	return &pb.SSHReleaseResult{Released: true, Message: fmt.Sprintf("Released: SSH access to %s ended, running ssh connections were terminated.", g.Host)}, nil
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
	if ks, err := s.B.Up.List(); err == nil && s.B.localSigner(id) == nil {
		inAgent := false
		for _, k := range ks {
			if ssh.FingerprintSHA256(k) == g.KeyFP {
				inAgent = true
			}
		}
		if !inAgent {
			return time.Time{}, errors.New("the decrypted key was removed when the grant ended – a new request with the passphrase is needed")
		}
	}
	base := g.Expires
	if now := s.B.now(); base.Before(now) {
		base = now
	}
	exp := base.Add(time.Duration(minutes) * time.Minute)
	if err := s.B.St.SetGrantExpiry(id, exp); err != nil {
		return time.Time{}, err
	}
	s.audit("ssh.grant_extended", g.Sandbox, g.Worktree, fmt.Sprintf("grant=%d until=%s", id, exp.Format(time.RFC3339)))
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
	s.B.SweepLocal()
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
