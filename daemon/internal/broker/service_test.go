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
		t.Fatal("VM-Config muss geschrieben werden")
	}
	f.now = f.now.Add(31 * time.Minute)
	s.Sweep(context.Background())
	if h.kill != 1 {
		t.Fatalf("bei Ablauf müssen ssh-Prozesse beendet werden (kill=%d)", h.kill)
	}
	s.Sweep(context.Background())
	if h.kill != 1 {
		t.Fatal("nur einmal aufräumen")
	}
}

func TestUnknownHostKeyNeedsExplicitTrust(t *testing.T) {
	s, f, _ := svcFx(t, "keyscan", []string{"SHA256:neu"})
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	ask := &autoAsker{s, Decision{Allow: true, Minutes: 10, KeyFP: fp}}
	s.Asker = ask
	s.Timeout = 300 * time.Millisecond
	res, _ := s.RequestSSH(context.Background(), "shop", "main", "new.example.com", "", 10, "")
	if res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("neuer Host-Key ohne Bestätigung darf nicht freigegeben werden")
	}
	ask.d.TrustUnknownHost = true
	s.Timeout = 2 * time.Second
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "new.example.com", "", 10, "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("mit Bestätigung muss es gehen: %v", res)
	}
}

func TestInvalidHostsAndUsersRejected(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	for _, h := range []string{"", "a b", "host;rm", "-oProxyCommand=x", "host\nHost *", "a/../b", "$(id)", strings.Repeat("a", 300)} {
		res, _ := s.RequestSSH(context.Background(), "shop", "main", h, "", 5, "")
		if res.Verdict != pb.Verdict_VERDICT_DENIED {
			t.Fatalf("%q muss abgelehnt werden", h)
		}
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, User: "root\nHost *"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("ungültiger Benutzername darf nie in die Config")
	}
}

func TestDecideRejectsBadKeyAndDuration(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: "SHA256:gibtsnicht"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("Key außerhalb des Upstream-Agents")
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: MaxMinutes + 1, KeyFP: fp}}
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("unzulässige Dauer")
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
		t.Fatal("Entzug muss ssh beenden")
	}
	if _, err := s.Extend(context.Background(), id, 5); err == nil {
		t.Fatal("entzogene Freigabe darf nicht verlängert werden")
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
		t.Fatalf("unsichere Werte in der Config:\n%s", out)
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
	// ohne Eingabe des Nutzers gilt der Wunsch des Agents
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp}}
	res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "deploy", 5, "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED || res.User != "deploy" {
		t.Fatalf("%v", res)
	}
	// Eingabe des Nutzers hat Vorrang
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, User: "claude"}}
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "deploy", 5, "")
	if res.User != "claude" {
		t.Fatalf("Nutzer-Eingabe muss gewinnen: %v", res)
	}
	// ohne beides: Standard des Hosts
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
			t.Fatalf("%q muss abgelehnt werden", u)
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
	t.Fatalf("Zeitüberschreitung: %s", what)
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
	// ohne Freigabe: Anfrage entsteht, ssh bleibt unberührt
	if msg := att(s, "shop", "srv.example.com", "22", "deploy"); !strings.Contains(msg, "angefragt") {
		t.Fatalf("%q", msg)
	}
	waitFor(t, "offene Anfrage", func() bool { return pending() == 1 })
	rs, _ := f.b.St.ListRequests(true, 5)
	if rs[0].Agent != "terminal" || !strings.Contains(rs[0].Reason, "Verbindungsversuch") {
		t.Fatalf("%+v", rs[0])
	}
	// zweiter Versuch: keine zweite Anfrage
	if msg := att(s, "shop", "srv.example.com", "22", "deploy"); !strings.Contains(msg, "bereits angefragt") {
		t.Fatalf("%q", msg)
	}
	time.Sleep(100 * time.Millisecond)
	if pending() != 1 {
		t.Fatal("Duplikat")
	}
	// ablehnen -> Abkühlzeit
	if _, err := s.DecideSSH(Decision{ID: rs[0].ID, Allow: false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Anfrage erledigt", func() bool { return pending() == 0 })
	time.Sleep(100 * time.Millisecond)
	if msg := att(s, "shop", "srv.example.com", "22", ""); !strings.Contains(msg, "kürzlich nicht erteilt") {
		t.Fatalf("Abkühlzeit erwartet: %q", msg)
	}
	f.now = f.now.Add(AttemptCooldown + time.Second)
	if msg := att(s, "shop", "srv.example.com", "22", ""); !strings.Contains(msg, "angefragt") {
		t.Fatalf("nach der Abkühlzeit wieder anfragen: %q", msg)
	}
	waitFor(t, "neue Anfrage", func() bool { return pending() == 1 })
}

func TestAttemptIgnoresGrantedAndInvalid(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	f.grant(t, "shop", 10, false) // Host "srv"
	if msg := att(s, "shop", "srv", "22", ""); msg != "" {
		t.Fatalf("bei aktiver Freigabe nichts melden: %q", msg)
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
	// ungültiger Benutzer wird verworfen, die Anfrage entsteht trotzdem
	if msg := att(s, "other", "ok.example.com", "22", "Root\nx"); !strings.Contains(msg, "angefragt") {
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
	if st != "allowed" || !strings.Contains(msg, "erteilt") {
		t.Fatalf("%s %q", st, msg)
	}
	// aktive Freigabe: sofort granted, ohne zu warten
	if st, _ := s.Attempt(context.Background(), "wait", "w.example.com", "22", "", true); st != "granted" {
		t.Fatalf("granted erwartet, bekam %s", st)
	}
	// Ablehnung
	s2, _, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s2.Asker = &autoAsker{s2, Decision{Allow: false}}
	if st, msg := s2.Attempt(context.Background(), "wait", "d.example.com", "22", "", true); st != "denied" || !strings.Contains(msg, "nicht erteilt") {
		t.Fatalf("%s %q", st, msg)
	}
}

func TestAttemptWaitAbortAndSharedWaiters(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s.Asker = silentAsker2{}
	s.Timeout = 5 * time.Second
	// Abbruch (Strg-C) beendet nur das Warten, die Anfrage bleibt offen
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan string, 1)
	go func() { st, _ := s.Attempt(ctx, "wait", "a.example.com", "22", "", true); res <- st }()
	waitFor(t, "offene Anfrage", func() bool {
		rs, _ := f.b.St.ListRequests(true, 10)
		return len(rs) == 1
	})
	cancel()
	if st := <-res; st != "aborted" {
		t.Fatalf("%s", st)
	}
	rs, _ := f.b.St.ListRequests(true, 10)
	if len(rs) != 1 {
		t.Fatal("Anfrage muss offen bleiben")
	}
	// zwei Wartende teilen sich dieselbe Anfrage und bekommen dieselbe Antwort
	out := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() { st, _ := s.Attempt(context.Background(), "wait", "a.example.com", "22", "", true); out <- st }()
	}
	time.Sleep(100 * time.Millisecond)
	if rs, _ = f.b.St.ListRequests(true, 10); len(rs) != 1 {
		t.Fatalf("keine zweite Anfrage erwartet: %d", len(rs))
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
		t.Fatal("ohne ConfirmEach darf keine Einzelbestätigungs-Freigabe entstehen")
	}
	f.b.ConfirmEach = func(store.SSHGrant, string) bool { return true }
	s.Timeout = 2 * time.Second
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", "", 5, ""); res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("mit ConfirmEach muss es gehen: %v", res)
	}
}
