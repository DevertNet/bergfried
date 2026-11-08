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
	res, err := s.RequestSSH(context.Background(), "shop", "main", "Example.com", 15, "deploy")
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
	res, _ := s.RequestSSH(context.Background(), "shop", "main", "new.example.com", 10, "")
	if res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("neuer Host-Key ohne Bestätigung darf nicht freigegeben werden")
	}
	ask.d.TrustUnknownHost = true
	s.Timeout = 2 * time.Second
	res, _ = s.RequestSSH(context.Background(), "shop", "main", "new.example.com", 10, "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("mit Bestätigung muss es gehen: %v", res)
	}
}

func TestInvalidHostsAndUsersRejected(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	for _, h := range []string{"", "a b", "host;rm", "-oProxyCommand=x", "host\nHost *", "a/../b", "$(id)", strings.Repeat("a", 300)} {
		res, _ := s.RequestSSH(context.Background(), "shop", "main", h, 5, "")
		if res.Verdict != pb.Verdict_VERDICT_DENIED {
			t.Fatalf("%q muss abgelehnt werden", h)
		}
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: fp, User: "root\nHost *"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("ungültiger Benutzername darf nie in die Config")
	}
}

func TestDecideRejectsBadKeyAndDuration(t *testing.T) {
	s, f, _ := svcFx(t, "known_hosts", []string{"SHA256:x"})
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: 5, KeyFP: "SHA256:gibtsnicht"}}
	s.Timeout = 300 * time.Millisecond
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
		t.Fatal("Key außerhalb des Upstream-Agents")
	}
	fp := ssh.FingerprintSHA256(f.userKey.PublicKey())
	s.Asker = &autoAsker{s, Decision{Allow: true, Minutes: MaxMinutes + 1, KeyFP: fp}}
	if res, _ := s.RequestSSH(context.Background(), "shop", "main", "ok.example.com", 5, ""); res.Verdict == pb.Verdict_VERDICT_ALLOWED {
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
