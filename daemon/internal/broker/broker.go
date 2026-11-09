// Package broker: ssh-agent-Protokoll für VMs. Der Broker hält keine Keys; er signiert über den
// echten Agent des Macs (Upstream) und nur, wenn eine aktive Freigabe zu Sandbox, Key und
// Ziel-Host-Key passt (Destination Constraints analog OpenSSH >= 8.9, session-bind@openssh.com).
package broker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const sessionBindExt = "session-bind@openssh.com"

var errDenied = errors.New("ssh-broker: keine passende Freigabe")

// Upstream ist der echte Agent (Schlüsselbund, ssh-agent, 1Password ...).
type Upstream interface {
	List() ([]*agent.Key, error)
	Sign(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error)
}

// SocketUpstream verbindet pro Aufruf neu (übersteht Agent-Neustarts).
type SocketUpstream struct{ Path string }

func (u SocketUpstream) with(f func(agent.ExtendedAgent) error) error {
	c, err := net.DialTimeout("unix", u.Path, 3*time.Second)
	if err != nil {
		return fmt.Errorf("Upstream-Agent nicht erreichbar: %w", err)
	}
	defer c.Close()
	return f(agent.NewClient(c))
}
func (u SocketUpstream) List() (ks []*agent.Key, err error) {
	err = u.with(func(a agent.ExtendedAgent) (e error) { ks, e = a.List(); return })
	return
}
func (u SocketUpstream) Sign(k ssh.PublicKey, d []byte, fl agent.SignatureFlags) (sig *ssh.Signature, err error) {
	err = u.with(func(a agent.ExtendedAgent) (e error) { sig, e = a.SignWithFlags(k, d, fl); return })
	return
}

type Broker struct {
	St  *store.Store
	Up  Upstream
	Now func() time.Time
	// ConfirmEach fragt bei Freigaben mit per_connection nach jeder Verbindung; nil = ablehnen.
	ConfirmEach func(g store.SSHGrant, hostKeyFP string) bool
}

func (b *Broker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Broker) audit(event, sandbox, detail string) {
	if err := b.St.Audit("broker", event, sandbox, "", detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
	}
}

func (b *Broker) active(sandbox string) []store.SSHGrant {
	gs, err := b.St.ListGrants(true, b.now())
	if err != nil {
		return nil
	}
	var out []store.SSHGrant
	for _, g := range gs {
		if g.Sandbox == sandbox {
			out = append(out, g)
		}
	}
	return out
}

type binding struct {
	sshSessionID  []byte
	hostKeyFP  string
	forwarding bool
}

// Conn ist der Agent einer einzelnen Client-Verbindung (implementiert agent.ExtendedAgent).
type Conn struct {
	b       *Broker
	sandbox string
	mu      sync.Mutex
	bound   *binding
}

func (b *Broker) NewConn(sandbox string) *Conn { return &Conn{b: b, sandbox: sandbox} }

// Serve bedient eine Verbindung bis zum Ende.
func (c *Conn) Serve(rw io.ReadWriter) error { return agent.ServeAgent(c, rw) }

func (c *Conn) List() ([]*agent.Key, error) {
	gs := c.b.active(c.sandbox)
	if len(gs) == 0 {
		return nil, nil // ohne Freigabe: leere Liste
	}
	want := map[string]bool{}
	for _, g := range gs {
		want[g.KeyFP] = true
	}
	ks, err := c.b.Up.List()
	if err != nil {
		c.b.audit("ssh.list_failed", c.sandbox, err.Error())
		return nil, nil
	}
	var out []*agent.Key
	for _, k := range ks {
		if want[ssh.FingerprintSHA256(k)] {
			out = append(out, k)
		}
	}
	return out, nil
}

func (c *Conn) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	return c.SignWithFlags(key, data, 0)
}

func parseSSHSessionID(data []byte) ([]byte, error) {
	if len(data) < 4 {
		return nil, errors.New("zu kurz")
	}
	n := binary.BigEndian.Uint32(data)
	if uint64(n)+4 > uint64(len(data)) {
		return nil, errors.New("ungültige Länge")
	}
	return data[4 : 4+n], nil
}

func (c *Conn) deny(reason, keyFP string) (*ssh.Signature, error) {
	c.b.audit("ssh.sign_denied", c.sandbox, fmt.Sprintf("key=%s: %s", keyFP, reason))
	return nil, errDenied
}

func (c *Conn) SignWithFlags(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	fp := ssh.FingerprintSHA256(key)
	c.mu.Lock()
	bd := c.bound
	c.mu.Unlock()
	if bd == nil {
		return c.deny("kein session-bind (Client zu alt oder Host-Key unbekannt)", fp)
	}
	if bd.forwarding {
		return c.deny("Agent-Weiterleitung ist nicht erlaubt", fp)
	}
	sid, err := parseSSHSessionID(data)
	if err != nil || !bytes.Equal(sid, bd.sshSessionID) {
		return c.deny("Signaturdaten passen nicht zur gebundenen Verbindung", fp)
	}
	var match *store.SSHGrant
	for _, g := range c.b.active(c.sandbox) {
		if g.KeyFP != fp {
			continue
		}
		for _, h := range g.HostKeys {
			if h == bd.hostKeyFP {
				g := g
				match = &g
			}
		}
	}
	if match == nil {
		return c.deny("Ziel-Host "+bd.hostKeyFP+" nicht freigegeben", fp)
	}
	if match.PerConn {
		if c.b.ConfirmEach == nil || !c.b.ConfirmEach(*match, bd.hostKeyFP) {
			return c.deny("Einzelbestätigung abgelehnt", fp)
		}
	}
	sig, err := c.b.Up.Sign(key, data, flags)
	if err != nil {
		c.b.audit("ssh.sign_failed", c.sandbox, err.Error())
		return nil, errors.New("ssh-broker: Signatur fehlgeschlagen")
	}
	c.b.audit("ssh.sign", c.sandbox, fmt.Sprintf("grant=%d host=%s key=%s", match.ID, match.Host, fp))
	return sig, nil
}

// Extension: session-bind@openssh.com – Host-Key des Ziels mit Signatur über die SSH-Session-ID.
func (c *Conn) Extension(extType string, contents []byte) ([]byte, error) {
	if extType != sessionBindExt {
		return nil, agent.ErrExtensionUnsupported
	}
	var m struct {
		HostKey      []byte
		SSHSessionID    []byte
		Signature    []byte
		IsForwarding bool
	}
	if err := ssh.Unmarshal(contents, &m); err != nil {
		return nil, errors.New("session-bind ungültig")
	}
	hk, err := ssh.ParsePublicKey(m.HostKey)
	if err != nil {
		return nil, errors.New("session-bind: Host-Key ungültig")
	}
	var s struct {
		Format string
		Blob   []byte
	}
	if err := ssh.Unmarshal(m.Signature, &s); err != nil {
		return nil, errors.New("session-bind: Signatur ungültig")
	}
	if err := hk.Verify(m.SSHSessionID, &ssh.Signature{Format: s.Format, Blob: s.Blob}); err != nil {
		c.b.audit("ssh.bind_invalid", c.sandbox, "Signatur des Host-Keys falsch")
		return nil, errors.New("session-bind: Verifikation fehlgeschlagen")
	}
	c.mu.Lock()
	// Weiterleitungs-Kette: sobald ein Bind "forwarding" ist, bleibt die Verbindung gesperrt.
	fw := m.IsForwarding || (c.bound != nil && c.bound.forwarding)
	c.bound = &binding{sshSessionID: m.SSHSessionID, hostKeyFP: ssh.FingerprintSHA256(hk), forwarding: fw}
	c.mu.Unlock()
	return nil, nil
}

// Schreibende Operationen sind nie erlaubt: die VM kann keine Keys einlagern.
func (c *Conn) Add(agent.AddedKey) error       { return errors.New("nicht erlaubt") }
func (c *Conn) Remove(ssh.PublicKey) error     { return errors.New("nicht erlaubt") }
func (c *Conn) RemoveAll() error               { return errors.New("nicht erlaubt") }
func (c *Conn) Lock([]byte) error              { return errors.New("nicht erlaubt") }
func (c *Conn) Unlock([]byte) error            { return errors.New("nicht erlaubt") }
func (c *Conn) Signers() ([]ssh.Signer, error) { return nil, errors.New("nicht erlaubt") }
