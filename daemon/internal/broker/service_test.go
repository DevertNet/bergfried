package broker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"golang.org/x/crypto/ssh"
)

type fakeHooks struct {
	mu            sync.Mutex
	applied, kill int
}

func (h *fakeHooks) Apply(context.Context, string, []store.SSHGrant) error {
	h.mu.Lock()
	h.applied++
	h.mu.Unlock()
	return nil
}
func (h *fakeHooks) KillSSH(context.Context, string) error {
	h.mu.Lock()
	h.kill++
	h.mu.Unlock()
	return nil
}

type autoAsker struct {
	s *Service
	d Decision
}

func (a *autoAsker) AskSSH(r store.Request, _ SSHInfo) {
	d := a.d
	d.ID = r.ID
	go func() { _, _ = a.s.DecideSSH(d) }()
}

func svcFx(t *testing.T, source string, hostFPs []string) (*Service, *fx, *fakeHooks) {
	f := setup(t)
	hc, _ := LoadHostConfig("")
	h := &fakeHooks{}
	s := NewService(f.b, func(context.Context, string) ([]string, string, error) { return hostFPs, source, nil }, h, nil, hc)
	s.Timeout = 2 * time.Second
	return s, f, h
}

func TestRequestAllowedCreatesGrantAndExpiryCleansUp(t *testing.T) {
	hostFP := "SHA256:abc"
	s, f, h := svcFx(t, "known_hosts", []string{hostFP})
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 30, KeyFP: ssh.FingerprintSHA256(f.userKey.PublicKey()), User: "claude"}}
	res, err := s.RequestSSH(context.Background(), "shop", "main", "Example.com", "", 15, "deploy")
	if err != nil || res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("%v %v", res, err)
	}
	gs, _ := f.b.St.ListGrants(true, f.now)
	if len(gs) != 1 || gs[0].Host != "example.com" || gs[0].HostKeys[0] != hostFP || gs[0].Agent != "main" {
		t.Fatalf("%+v", gs)
	}
	if h.applied == 0 {
		t.Fatal("VM config must be written")
	}
	f.now = f.now.Add(31 * time.Minute)
	s.Sweep(context.Background())
	if h.kill != 1 {
		t.Fatalf("on expiry ssh processes must be terminated (kill=%d)", h.kill)
	}
	s.Sweep(context.Background())
	if h.kill != 1 {
		t.Fatal("clean up only once")
	}
}

func TestUnknownHostKeyNeedsExplicitTrust(t *testing.T) {
	s, f, _ := svcFx(t, "keyscan", []string{"SHA256:new"})
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	ask := &autoAsker{s, Decision{Allow: true, Minutes: 10, KeyFP: fp}}
	s.Asker = ask
	s.Timeout = 300 * time.Millisecond
	res, _ := s.RequestSSH(context.Background(), "shop", "main", "new.example.com", "", 10, "")
	if res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("a new host key without confirmation must not be approved")
	}
	ask.d.TrustUnknownHost = true
	s.Timeout = 2 * time.Second
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "new.example.com", "", 10, "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("with confirmation it must work: %v", res)
	}
}

func TestInvalidHostsAndUsersRejected(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	for _, h := range []string{"", "a b", "host;rm", "-oProxyCommand=x", "host\nHost *", "a/../b", "$(id)", strings.Repeat("a", 300)} {
		res, _ := s.RequestSSH(context.Background(), "shop", "main", h, "", 5, "")
		if res.Verdict != pb.Verdict_VERDICT_DENIED {
			t.Fatalf("%q must be refused", h)
		}
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, User: "root\nHost *"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("an invalid user name must never reach the config")
	}
}

func TestDecideRejectsBadKeyAndDuration(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: "SHA256:gibtsnicht"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("key outside the upstream agent")
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: MaxMinutes + 1, KeyFP: fp}}
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("illegal duration")
	}
}

func TestExtendAndRevoke(t *testing.T) {
	s, f, h := svcFx(t, "known_hosts", []string{"SHA256:x"})
	id := f.grant(t, "shop", 5, false)
	exp, err := s.Extend(context.Background(), id, 60)
	if err != nil || exp.Before(f.now.Add(60*time.Minute)) {
		t.Fatalf("%v %v", exp, err)
	}
	if err := s.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if h.kill != 1 {
		t.Fatal("revocation must terminate ssh")
	}
	if _, err := s.Extend(context.Background(), id, 5); err == nil {
		t.Fatal("a revoked grant must not be extended")
	}
}

func TestRenderSSHConfigIsSafe(t *testing.T) {
	out := RenderSSHConfig([]store.SSHGrant{
		{Host: "example.com", User: "claude"},
		{Host: "srv.example.com:2222", User: "deploy"},
		{Host: "evil\nHost *", User: "x"},
		{Host: "ok.example.com", User: "root\nProxyCommand evil"},
	})
	if strings.Contains(out, "evil") || strings.Contains(out, "ProxyCommand") {
		t.Fatalf("unsafe values in the config:\n%s", out)
	}
	if !strings.Contains(out, "Host example.com") || !strings.Contains(out, "Port 2222") {
		t.Fatalf("\n%s", out)
	}
}

func TestParseKeyLines(t *testing.T) {
	_, s := newKey(t)
	line := "example.com " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
	got := parseKeyLines([]byte("# Host example.com found: line 3\n" + line + "\n"))
	if len(got) != 1 || got[0] != ssh.FingerprintSHA256(s.PublicKey()) {
		t.Fatalf("%v", got)
	}
}

func TestAgentRequestedUserIsUsedUnlessUserOverrides(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	// without user input the agent's wish applies
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp}}
	res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "deploy", 5, "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED || res.User != "deploy" {
		t.Fatalf("%v", res)
	}
	// user input takes precedence
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, User: "claude"}}
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "deploy", 5, "")
	if res.User != "claude" {
		t.Fatalf("user input must win: %v", res)
	}
	// without both: host default
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp}}
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, "")
	if res.User == "" {
		t.Fatalf("Standard-User erwartet: %v", res)
	}
}

func TestInvalidRequestedUserRejected(t *testing.T) {
	s, _, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	for _, u := range []string{"Root", "root\nHost *", "a b", "-oProxyCommand=x", "$(id)", "x;y"} {
		res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", u, 5, "")
		if res.Verdict != pb.Verdict_VERDICT_DENIED {
			t.Fatalf("%q must be refused", u)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestAttemptCreatesRequestDedupesAndCoolsDown(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	pending := func() int {
		rs, _ := f.b.St.ListRequests(true, 50)
		n := 0
		for _, r := range rs {
			if r.Kind == "ssh" && r.Path == "srv.example.com" {
				n++
			}
		}
		return n
	}
	// without a grant: a request is created, ssh stays untouched
	if msg := att(s, "shop", "srv.example.com", "22", "deploy"); !strings.Contains(msg, "requested") {
		t.Fatalf("%q", msg)
	}
	waitFor(t, "open request", func() bool { return pending() == 1 })
	rs, _ := f.b.St.ListRequests(true, 5)
	if rs[0].Agent != "terminal" || !strings.Contains(rs[0].Reason, "connection attempt") {
		t.Fatalf("%+v", rs[0])
	}
	// second attempt: no second request
	if msg := att(s, "shop", "srv.example.com", "22", "deploy"); !strings.Contains(msg, "already requested") {
		t.Fatalf("%q", msg)
	}
	time.Sleep(100 * time.Millisecond)
	if pending() != 1 {
		t.Fatal("Duplikat")
	}
	// deny -> cooldown
	if _, err := s.DecideSSH(Decision{ID: rs[0].ID, Allow: false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "request finished", func() bool { return pending() == 0 })
	time.Sleep(100 * time.Millisecond)
	if msg := att(s, "shop", "srv.example.com", "22", ""); !strings.Contains(msg, "was not granted recently") {
		t.Fatalf("cooldown expected: %q", msg)
	}
	f.now = f.now.Add(AttemptCooldown + time.Second)
	if msg := att(s, "shop", "srv.example.com", "22", ""); !strings.Contains(msg, "requested") {
		t.Fatalf("ask again after the cooldown: %q", msg)
	}
	waitFor(t, "new request", func() bool { return pending() == 1 })
}

func TestAttemptIgnoresGrantedAndInvalid(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	f.grant(t, "shop", 10, false) // Host "srv"
	if msg := att(s, "shop", "srv", "22", ""); msg != "" {
		t.Fatalf("report nothing with an active grant: %q", msg)
	}
	for _, h := range []string{"", "a b", "host;rm", "$(id)", "-oProxyCommand=x"} {
		if msg := att(s, "shop", h, "22", "x"); msg != "" {
			t.Fatalf("%q: %q", h, msg)
		}
	}
	// anderer Port ergibt anderen Host-Eintrag
	if msg := att(s, "shop", "srv", "2222", "claude"); !strings.Contains(msg, "srv:2222") {
		t.Fatalf("%q", msg)
	}
	// an invalid user is discarded, the request is still created
	if msg := att(s, "other", "ok.example.com", "22", "Root\nx"); !strings.Contains(msg, "requested") {
		t.Fatalf("%q", msg)
	}
}

func att(s *Service, sandbox, host, port, user string) string {
	_, m := s.Attempt(context.Background(), sandbox, host, port, user, false)
	return m
}

func TestAttemptWaitReturnsDecision(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	run := func(d Decision) (string, string) {
		s.Asker = &autoAsker{s, d}
		return s.Attempt(context.Background(), "wait", "w.example.com", "22", "", true)
	}
	st, msg := run(Decision{Allow: true, Minutes: 5, KeyFP: fp})
	if st != "allowed" || !strings.Contains(msg, "granted") {
		t.Fatalf("%s %q", st, msg)
	}
	// active grant: granted immediately, without waiting
	if st, _ := s.Attempt(context.Background(), "wait", "w.example.com", "22", "", true); st != "granted" {
		t.Fatalf("granted erwartet, bekam %s", st)
	}
	// Ablehnung
	s2, _, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s2.Asker = &autoAsker{s2, Decision{Allow: false}}
	if st, msg := s2.Attempt(context.Background(), "wait", "d.example.com", "22", "", true); st != "denied" || !strings.Contains(msg, "not granted") {
		t.Fatalf("%s %q", st, msg)
	}
}

func TestAttemptWaitAbortAndSharedWaiters(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s.Asker = silentAsker2{}
	s.Timeout = 5 * time.Second
	// abort (Ctrl-C) ends only the wait, the request stays open
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan string, 1)
	go func() { st, _ := s.Attempt(ctx, "wait", "a.example.com", "22", "", true); res <- st }()
	waitFor(t, "open request", func() bool {
		rs, _ := f.b.St.ListRequests(true, 10)
		return len(rs) == 1
	})
	cancel()
	if st := <-res; st != "aborted" {
		t.Fatalf("%s", st)
	}
	rs, _ := f.b.St.ListRequests(true, 10)
	if len(rs) != 1 {
		t.Fatal("request must stay open")
	}
	// two waiters share the same request and get the same answer
	out := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() { st, _ := s.Attempt(context.Background(), "wait", "a.example.com", "22", "", true); out <- st }()
	}
	time.Sleep(100 * time.Millisecond)
	if rs, _ = f.b.St.ListRequests(true, 10); len(rs) != 1 {
		t.Fatalf("no second request expected: %d", len(rs))
	}
	if _, err := s.DecideSSH(Decision{ID: rs[0].ID, Allow: false}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if st := <-out; st != "denied" {
			t.Fatalf("%s", st)
		}
	}
}

type silentAsker2 struct{}

func (silentAsker2) AskSSH(store.Request, SSHInfo) {}

func TestPerConnectionRefusedWithoutConfirmPath(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, PerConn: true}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("without ConfirmEach no per-connection grant may be created")
	}
	f.b.ConfirmEach = func(store.SSHGrant, string) bool { return true }
	s.Timeout = 2 * time.Second
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("with ConfirmEach it must work: %v", res)
	}
}
