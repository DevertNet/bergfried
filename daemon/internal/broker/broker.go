// Package broker: ssh-agent protocol for VMs. The broker holds no keys; it signs through the
// Mac's real agent (upstream) and only when an active grant matches sandbox, key and
// target host key (destination constraints like OpenSSH >= 8.9, session-bind@openssh.com).
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

var errDenied = errors.New("ssh broker: no matching grant")

// Upstream is the real agent (keychain, ssh-agent, 1Password ...).
type Upstream interface {
	List() ([]*agent.Key, error)
	Sign(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error)
}

// SocketUpstream reconnects on every call (survives agent restarts).
type SocketUpstream struct{ Path string }

func (u SocketUpstream) with(f func(agent.ExtendedAgent) error) error {
	c, err := net.DialTimeout("unix", u.Path, 3*time.Second)
	if err != nil {
		return fmt.Errorf("upstream agent not reachable: %w", err)
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
	// ConfirmEach asks after each connection for grants with per_connection; nil = refuse.
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
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
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
	sshSessionID []byte
	hostKeyFP    string
	forwarding   bool
}

// Conn is the agent of a single client connection (implements agent.ExtendedAgent).
type Conn struct {
	b       *Broker
	sandbox string
	mu      sync.Mutex
	bound   *binding
}

func (b *Broker) NewConn(sandbox string) *Conn { return &Conn{b: b, sandbox: sandbox} }

// Serve serves a connection until it ends.
func (c *Conn) Serve(rw io.ReadWriter) error { return agent.ServeAgent(c, rw) }

func (c *Conn) List() ([]*agent.Key, error) {
	gs := c.b.active(c.sandbox)
	if len(gs) == 0 {
		return nil, nil // without a grant: empty list
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
		return nil, errors.New("too short")
	}
	n := binary.BigEndian.Uint32(data)
	if uint64(n)+4 > uint64(len(data)) {
		return nil, errors.New("invalid length")
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
		return c.deny("no session-bind (client too old or host key unknown)", fp)
	}
	if bd.forwarding {
		return c.deny("agent forwarding is not allowed", fp)
	}
	sid, err := parseSSHSessionID(data)
	if err != nil || !bytes.Equal(sid, bd.sshSessionID) {
		return c.deny("signature data does not match the bound connection", fp)
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
		return c.deny("target host "+bd.hostKeyFP+" not approved", fp)
	}
	if match.PerConn {
		if c.b.ConfirmEach == nil || !c.b.ConfirmEach(*match, bd.hostKeyFP) {
			return c.deny("per-connection confirmation refused", fp)
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

// Extension: session-bind@openssh.com – host key of the target with a signature over the SSH session ID.
func (c *Conn) Extension(extType string, contents []byte) ([]byte, error) {
	if extType != sessionBindExt {
		return nil, agent.ErrExtensionUnsupported
	}
	var m struct {
		HostKey      []byte
		SSHSessionID []byte
		Signature    []byte
		IsForwarding bool
	}
	if err := ssh.Unmarshal(contents, &m); err != nil {
		return nil, errors.New("session-bind invalid")
	}
	hk, err := ssh.ParsePublicKey(m.HostKey)
	if err != nil {
		return nil, errors.New("session-bind: invalid host key")
	}
	var s struct {
		Format string
		Blob   []byte
	}
	if err := ssh.Unmarshal(m.Signature, &s); err != nil {
		return nil, errors.New("session-bind: invalid signature")
	}
	if err := hk.Verify(m.SSHSessionID, &ssh.Signature{Format: s.Format, Blob: s.Blob}); err != nil {
		c.b.audit("ssh.bind_invalid", c.sandbox, "host key signature wrong")
		return nil, errors.New("session-bind: Verifikation fehlgeschlagen")
	}
	c.mu.Lock()
	// Forwarding chain: once a bind is "forwarding" the connection stays locked.
	fw := m.IsForwarding || (c.bound != nil && c.bound.forwarding)
	c.bound = &binding{sshSessionID: m.SSHSessionID, hostKeyFP: ssh.FingerprintSHA256(hk), forwarding: fw}
	c.mu.Unlock()
	return nil, nil
}

// Write operations are never allowed: the VM cannot insert keys.
func (c *Conn) Add(agent.AddedKey) error       { return errors.New("not allowed") }
func (c *Conn) Remove(ssh.PublicKey) error     { return errors.New("not allowed") }
func (c *Conn) RemoveAll() error               { return errors.New("not allowed") }
func (c *Conn) Lock([]byte) error              { return errors.New("not allowed") }
func (c *Conn) Unlock([]byte) error            { return errors.New("not allowed") }
func (c *Conn) Signers() ([]ssh.Signer, error) { return nil, errors.New("not allowed") }
