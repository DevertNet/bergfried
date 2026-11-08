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

// HostKeyResolver liefert die Host-Key-Fingerprints eines Ziels und woher sie stammen
// ("known_hosts" = vom Nutzer schon vertraut, "keyscan" = unbestätigt).
type HostKeyResolver func(ctx context.Context, host string) (fps []string, source string, err error)

// VMHooks wirken auf die VM (SSH-Config schreiben, laufende ssh-Prozesse beenden).
type VMHooks interface {
	Apply(ctx context.Context, session string, active []store.SSHGrant) error
	KillSSH(ctx context.Context, session string) error
}

type SSHInfo struct {
	RequestedUser           string // vom Agent angefragter Benutzer (kann leer sein)
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
	open    map[int64]string     // aktive Grants -> Session (für Ablauf-Erkennung)
	cool    map[string]time.Time // Session|Host -> frühestens wieder anfragen (nach Ablehnung/Ablauf)
	attempt map[string]bool      // Session|Host -> Anfrage läuft (Hintergrund-Goroutine aktiv)
}

// AttemptCooldown: nach einem abgelehnten oder abgelaufenen Versuch wird derselbe Host eine Weile nicht erneut gemeldet.
const AttemptCooldown = 60 * time.Second

func NewService(b *Broker, r HostKeyResolver, h VMHooks, a SSHAsker, hc *HostConfig) *Service {
	return &Service{B: b, Resolve: r, Hooks: h, Asker: a, Hosts: hc, Timeout: 10 * time.Minute,
		waiters: map[int64]*pending{}, open: map[int64]string{}, cool: map[string]time.Time{}, attempt: map[string]bool{}}
}

func (s *Service) audit(event, session, agent, detail string) {
	if err := s.B.St.Audit("daemon", event, session, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
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

func (s *Service) RequestSSH(ctx context.Context, session, agent, host, user string, minutes int, reason string) (*pb.SSHAccessResult, error) {
	deny := func(v pb.Verdict, msg string) (*pb.SSHAccessResult, error) {
		return &pb.SSHAccessResult{Verdict: v, Message: msg}, nil
	}
	if !hostRe.MatchString(host) {
		s.audit("ssh.request_invalid", session, agent, fmt.Sprintf("host=%q", trunc(host, 100)))
		return deny(pb.Verdict_VERDICT_DENIED, "Hostname ungültig.")
	}
	host = strings.ToLower(host)
	user = strings.TrimSpace(user)
	if user != "" && !userRe.MatchString(user) {
		s.audit("ssh.request_invalid", session, agent, fmt.Sprintf("user=%q", trunc(user, 40)))
		return deny(pb.Verdict_VERDICT_DENIED, "Benutzername ungültig (erlaubt: a-z, 0-9, _ und -, kleingeschrieben).")
	}
	reason = trunc(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, reason), 500)
	fps, source, err := s.Resolve(ctx, host)
	if err != nil || len(fps) == 0 {
		s.audit("ssh.request_nohostkey", session, agent, fmt.Sprintf("host=%s err=%v", host, err))
		return deny(pb.Verdict_VERDICT_DENIED, "Host-Key des Ziels nicht ermittelbar (nicht in known_hosts, nicht erreichbar).")
	}
	dk, du := s.Hosts.Default(host)
	info := SSHInfo{HostKeyFPs: fps, Source: source, DefaultKey: dk, DefaultUser: du, SuggestedMinutes: clamp(minutes), RequestedUser: user}
	extra, _ := json.Marshal(map[string]any{"fps": fps, "source": source, "user": user})
	req := store.Request{Session: session, Agent: agent, Kind: "ssh", Path: host, Reason: reason, Status: "pending", Bytes: int64(info.SuggestedMinutes), Extra: string(extra)}
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
	s.audit("ssh.request_created", session, agent, fmt.Sprintf("id=%d host=%s user=%s source=%s hostkeys=%s", id, host, user, source, strings.Join(fps, ",")))
	if s.Asker != nil {
		s.Asker.AskSSH(req, info)
	}
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case g := <-p.ch:
		if g == nil {
			return deny(pb.Verdict_VERDICT_DENIED, "Der Nutzer hat abgelehnt.")
		}
		s.mu.Lock()
		s.open[g.ID] = g.Session
		s.mu.Unlock()
		s.apply(ctx, g.Session)
		return &pb.SSHAccessResult{Verdict: pb.Verdict_VERDICT_ALLOWED, ExpiresUnix: g.Expires.Unix(), User: g.User,
			Message: fmt.Sprintf("Freigegeben bis %s. Verbinde mit: ssh %s@%s", g.Expires.Format("15:04"), g.User, host)}, nil
	case <-timer.C:
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_expired", session, agent, fmt.Sprintf("id=%d", id))
		}
		return deny(pb.Verdict_VERDICT_EXPIRED, "Keine Antwort, Anfrage abgelaufen.")
	case <-ctx.Done():
		if ok, _ := s.B.St.SetRequestStatus(id, "expired"); ok {
			s.audit("ssh.request_cancelled", session, agent, fmt.Sprintf("id=%d", id))
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

// Keys listet die Keys des Upstream-Agents (nur öffentliche Teile).
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

// DecideSSH entscheidet eine offene SSH-Anfrage und legt bei Zustimmung die Freigabe an.
func (s *Service) DecideSSH(d Decision) (string, error) {
	r, err := s.B.St.GetRequest(d.ID)
	if err != nil {
		return "", err
	}
	if r.Kind != "ssh" || r.Status != "pending" {
		return "", fmt.Errorf("Anfrage %d ist keine offene SSH-Anfrage", d.ID)
	}
	s.mu.Lock()
	p, ok := s.waiters[d.ID]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("Anfrage %d wartet nicht mehr (Agent hat abgebrochen)", d.ID)
	}
	if !d.Allow {
		if ok, _ := s.B.St.SetRequestStatus(d.ID, "denied"); !ok {
			return "", errors.New("Anfrage nicht mehr offen")
		}
		s.audit("ssh.request_denied", r.Session, r.Agent, fmt.Sprintf("id=%d", d.ID))
		p.ch <- nil
		return "denied", nil
	}
	if d.Minutes < 1 || d.Minutes > MaxMinutes {
		return "", fmt.Errorf("Dauer muss 1..%d Minuten sein", MaxMinutes)
	}
	if p.info.Source != "known_hosts" && !d.TrustUnknownHost {
		return "", fmt.Errorf("Host-Key von %s ist neu (%s) – mit --trust-host-key bestätigen", r.Path, strings.Join(p.info.HostKeyFPs, ", "))
	}
	// Vorrang: Eingabe des Nutzers, dann Wunsch des Agents, dann Standard des Hosts.
	user := d.User
	if user == "" {
		user = p.info.RequestedUser
	}
	if user == "" {
		user = p.info.DefaultUser
	}
	if !userRe.MatchString(user) {
		return "", errors.New("Benutzername ungültig")
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
		return "", errors.New("Key nicht im Upstream-Agent (--key SHA256:… angeben, siehe `ssh keys`)")
	}
	if ok, _ := s.B.St.SetRequestStatus(d.ID, "allowed"); !ok {
		return "", errors.New("Anfrage nicht mehr offen")
	}
	agentName := r.Agent
	if d.AllAgents {
		agentName = ""
	}
	g := store.SSHGrant{Session: r.Session, Agent: agentName, Host: r.Path, KeyFP: keyFP, User: user, HostKeys: p.info.HostKeyFPs,
		Expires: s.B.now().Add(time.Duration(d.Minutes) * time.Minute), PerConn: d.PerConn}
	gid, err := s.B.St.AddGrant(g)
	if err != nil {
		return "", err
	}
	g.ID = gid
	s.Hosts.Remember(r.Path, keyFP, user)
	s.audit("ssh.grant_created", r.Session, r.Agent, fmt.Sprintf("grant=%d request=%d host=%s key=%s user=%s minutes=%d all_agents=%v per_conn=%v", gid, d.ID, r.Path, keyFP, user, d.Minutes, d.AllAgents, d.PerConn))
	p.ch <- &g
	return fmt.Sprintf("allowed (Freigabe %d)", gid), nil
}

func (s *Service) apply(ctx context.Context, session string) {
	if s.Hooks == nil {
		return
	}
	gs := s.B.active(session)
	if err := s.Hooks.Apply(ctx, session, gs); err != nil {
		s.audit("ssh.vm_config_failed", session, "", err.Error())
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
	s.audit("ssh.grant_revoked", g.Session, g.Agent, fmt.Sprintf("grant=%d host=%s", id, g.Host))
	s.cleanup(ctx, g.Session)
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
		return time.Time{}, errors.New("Freigabe wurde entzogen – neue Anfrage nötig")
	}
	if minutes < 1 || minutes > MaxMinutes {
		return time.Time{}, fmt.Errorf("Dauer muss 1..%d Minuten sein", MaxMinutes)
	}
	base := g.Expires
	if now := s.B.now(); base.Before(now) {
		base = now
	}
	exp := base.Add(time.Duration(minutes) * time.Minute)
	if err := s.B.St.SetGrantExpiry(id, exp); err != nil {
		return time.Time{}, err
	}
	s.audit("ssh.grant_extended", g.Session, g.Agent, fmt.Sprintf("grant=%d until=%s", id, exp.Format(time.RFC3339)))
	s.mu.Lock()
	s.open[id] = g.Session
	s.mu.Unlock()
	s.apply(ctx, g.Session)
	return exp, nil
}

// cleanup: SSH-Config neu schreiben und laufende ssh-Prozesse der Session beenden.
func (s *Service) cleanup(ctx context.Context, session string) {
	s.apply(ctx, session)
	if s.Hooks != nil {
		if err := s.Hooks.KillSSH(ctx, session); err != nil {
			s.audit("ssh.kill_failed", session, "", err.Error())
		}
	}
}

// Sweep erkennt abgelaufene Freigaben und räumt auf. Wird regelmäßig aufgerufen.
func (s *Service) Sweep(ctx context.Context) {
	active, err := s.B.St.ListGrants(true, s.B.now())
	if err != nil {
		return
	}
	still := map[int64]string{}
	for _, g := range active {
		still[g.ID] = g.Session
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

// RenderSSHConfig erzeugt den verwalteten Konfig-Block der VM (nur validierte Werte).
func RenderSSHConfig(active []store.SSHGrant) string {
	var b strings.Builder
	b.WriteString("# Von Bergfried verwaltet – Änderungen gehen verloren.\n")
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

// ---- Host-Key-Auflösung (Mac) ----

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

// ResolveHostKeys: erst known_hosts des Macs (`ssh-keygen -F`), sonst `ssh-keyscan`.
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

// ---- Standard-Key und -User je Host ----

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

// PendingInfo liefert die Entscheidungshilfen einer offenen SSH-Anfrage.
func (s *Service) PendingInfo(id int64) (SSHInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.waiters[id]
	if !ok {
		return SSHInfo{}, false
	}
	return p.info, true
}

// Attempt wird vom ssh-Hook der VM aufgerufen, wenn dort ein ssh-Befehl startet. Gibt es für den Host keine aktive Freigabe
// und keine offene Anfrage, wird (im Hintergrund) eine Anfrage in der App angelegt. Der ssh-Aufruf selbst bleibt unberührt.
func (s *Service) Attempt(session, host, port, user string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if port != "" && port != "22" {
		host += ":" + port
	}
	if !hostRe.MatchString(host) {
		return ""
	}
	if user != "" && !userRe.MatchString(user) {
		user = ""
	}
	// aktive Freigabe vorhanden? Dann nichts tun.
	for _, g := range s.B.active(session) {
		if g.Host == host {
			return ""
		}
	}
	key := session + "|" + host
	s.mu.Lock()
	if s.attempt[key] {
		s.mu.Unlock()
		return "Bergfried: SSH-Freigabe für " + host + " ist bereits angefragt – bitte in der App bestätigen."
	}
	if until, ok := s.cool[key]; ok && s.B.now().Before(until) {
		s.mu.Unlock()
		return "Bergfried: SSH-Freigabe für " + host + " wurde kürzlich nicht erteilt."
	}
	s.attempt[key] = true
	s.mu.Unlock()
	go func() {
		res, err := s.RequestSSH(context.Background(), session, "terminal", host, user, 15, "SSH-Verbindungsversuch in der VM (ssh "+host+")")
		s.mu.Lock()
		delete(s.attempt, key)
		if err != nil || res == nil || res.Verdict != pb.Verdict_VERDICT_ALLOWED {
			s.cool[key] = s.B.now().Add(AttemptCooldown)
		} else {
			delete(s.cool, key)
		}
		s.mu.Unlock()
	}()
	s.audit("ssh.attempt", session, "terminal", fmt.Sprintf("host=%s user=%s", host, user))
	return "Bergfried: SSH-Freigabe für " + host + " angefragt – bitte in der App bestätigen, danach den Befehl wiederholen."
}
