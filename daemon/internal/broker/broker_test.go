package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type kr struct{ a agent.ExtendedAgent }

func (k kr) List() ([]*agent.Key, error) { return k.a.List() }
func (k kr) Sign(key ssh.PublicKey, d []byte, fl agent.SignatureFlags) (*ssh.Signature, error) {
	return k.a.SignWithFlags(key, d, fl)
}

func newKey(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, s
}

type fx struct {
	b       *Broker
	userKey ssh.Signer
	other   ssh.Signer
	host    ssh.Signer
	host2   ssh.Signer
	now     time.Time
}

func setup(t *testing.T) *fx {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	ring := agent.NewKeyring().(agent.ExtendedAgent)
	f := &fx{now: time.Now()}
	for _, p := range []**ssh.Signer{} {
		_ = p
	}
	pk, us := newKey(t)
	pk2, os2 := newKey(t)
	_, h1 := newKey(t)
	_, h2 := newKey(t)
	f.userKey, f.other, f.host, f.host2 = us, os2, h1, h2
	_ = ring.Add(agent.AddedKey{PrivateKey: pk})
	_ = ring.Add(agent.AddedKey{PrivateKey: pk2})
	f.b = &Broker{St: st, Up: kr{ring}, Now: func() time.Time { return f.now }}
	return f
}

func (f *fx) grant(t *testing.T, session string, mins int, perConn bool) int64 {
	id, err := f.b.St.AddGrant(store.SSHGrant{Session: session, Host: "srv", KeyFP: ssh.FingerprintSHA256(f.userKey.PublicKey()),
		User: "claude", HostKeys: []string{ssh.FingerprintSHA256(f.host.PublicKey())}, Expires: f.now.Add(time.Duration(mins) * time.Minute), PerConn: perConn})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// client verbindet sich wie ssh: eigener Agent-Client gegen Conn.
func (f *fx) client(session string) agent.ExtendedAgent {
	c, s := net.Pipe()
	go f.b.NewConn(session).Serve(s)
	return agent.NewClient(c)
}

func bind(t *testing.T, a agent.ExtendedAgent, host ssh.Signer, sid []byte, forwarding bool, signWith ssh.Signer) error {
	sig, err := signWith.Sign(nil, sid)
	if err != nil {
		t.Fatal(err)
	}
	m := struct {
		HostKey, SessionID, Signature []byte
		IsForwarding                  bool
	}{host.PublicKey().Marshal(), sid, ssh.Marshal(struct {
		Format string
		Blob   []byte
	}{sig.Format, sig.Blob}), forwarding}
	_, err = a.Extension(sessionBindExt, ssh.Marshal(m))
	return err
}

func authData(sid []byte) []byte {
	return append(ssh.Marshal(struct{ S string }{string(sid)}), 50, 0, 0, 0, 6, 'c', 'l', 'a', 'u', 'd', 'e')
}

func TestNoGrantNoKeysNoSignature(t *testing.T) {
	f := setup(t)
	a := f.client("shop")
	if ks, _ := a.List(); len(ks) != 0 {
		t.Fatal("ohne Freigabe muss die Liste leer sein")
	}
	sid := []byte("sid-1")
	_ = bind(t, a, f.host, sid, false, f.host)
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("ohne Freigabe darf nichts signiert werden")
	}
}

func TestSignOnlyWithGrantAndMatchingHost(t *testing.T) {
	f := setup(t)
	f.grant(t, "shop", 10, false)
	a := f.client("shop")
	ks, _ := a.List()
	if len(ks) != 1 || ssh.FingerprintSHA256(ks[0]) != ssh.FingerprintSHA256(f.userKey.PublicKey()) {
		t.Fatalf("nur der gewählte Key darf sichtbar sein: %v", ks)
	}
	sid := []byte("sid-2")
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("ohne session-bind muss abgelehnt werden (fail closed)")
	}
	if err := bind(t, a, f.host, sid, false, f.host); err != nil {
		t.Fatal(err)
	}
	sig, err := a.Sign(f.userKey.PublicKey(), authData(sid))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.userKey.PublicKey().Verify(authData(sid), sig); err != nil {
		t.Fatal("Signatur muss gültig sein:", err)
	}
	if _, err := a.Sign(f.other.PublicKey(), authData(sid)); err == nil {
		t.Fatal("anderer Key darf nicht signieren")
	}
}

func TestWrongHostForwardingAndForgedBind(t *testing.T) {
	f := setup(t)
	f.grant(t, "shop", 10, false)
	sid := []byte("sid-3")
	a := f.client("shop")
	_ = bind(t, a, f.host2, sid, false, f.host2) // anderer Host
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("nicht freigegebener Host")
	}
	a = f.client("shop")
	if err := bind(t, a, f.host, sid, false, f.host2); err == nil { // gefälschte Signatur
		t.Fatal("gefälschtes session-bind muss scheitern")
	}
	a = f.client("shop")
	_ = bind(t, a, f.host, sid, true, f.host) // Weiterleitung
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("Agent-Weiterleitung muss abgelehnt werden")
	}
	a = f.client("shop")
	_ = bind(t, a, f.host, sid, false, f.host)
	if _, err := a.Sign(f.userKey.PublicKey(), authData([]byte("andere-session"))); err == nil {
		t.Fatal("Daten einer anderen Verbindung dürfen nicht signiert werden")
	}
}

func TestExpiryRevokeAndOtherSession(t *testing.T) {
	f := setup(t)
	id := f.grant(t, "shop", 10, false)
	sid := []byte("sid-4")
	a := f.client("shop")
	_ = bind(t, a, f.host, sid, false, f.host)
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err != nil {
		t.Fatal(err)
	}
	// andere Session
	b := f.client("other")
	_ = bind(t, b, f.host, sid, false, f.host)
	if _, err := b.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("Freigabe gilt nur für die eigene Session")
	}
	// Ablauf
	f.now = f.now.Add(11 * time.Minute)
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("nach Ablauf muss abgelehnt werden")
	}
	if ks, _ := a.List(); len(ks) != 0 {
		t.Fatal("nach Ablauf leere Liste")
	}
	// Verlängern, dann Entziehen
	_ = f.b.St.SetGrantExpiry(id, f.now.Add(time.Hour))
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err != nil {
		t.Fatal("verlängert muss wieder gehen:", err)
	}
	_ = f.b.St.RevokeGrant(id)
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("nach Entzug muss abgelehnt werden")
	}
}

func TestPerConnectionConfirm(t *testing.T) {
	f := setup(t)
	f.grant(t, "shop", 10, true)
	sid := []byte("sid-5")
	a := f.client("shop")
	_ = bind(t, a, f.host, sid, false, f.host)
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("ohne ConfirmEach muss abgelehnt werden")
	}
	f.b.ConfirmEach = func(store.SSHGrant, string) bool { return false }
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err == nil {
		t.Fatal("abgelehnt")
	}
	f.b.ConfirmEach = func(store.SSHGrant, string) bool { return true }
	if _, err := a.Sign(f.userKey.PublicKey(), authData(sid)); err != nil {
		t.Fatal(err)
	}
}

func TestVMCannotManageKeys(t *testing.T) {
	f := setup(t)
	f.grant(t, "shop", 10, false)
	a := f.client("shop")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := a.Add(agent.AddedKey{PrivateKey: priv}); err == nil {
		t.Fatal("Add muss scheitern")
	}
	if err := a.RemoveAll(); err == nil {
		t.Fatal("RemoveAll muss scheitern")
	}
	if err := a.Lock([]byte("x")); err == nil {
		t.Fatal("Lock muss scheitern")
	}
}
