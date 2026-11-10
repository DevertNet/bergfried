package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Keys that are not loaded in the upstream ssh-agent can still be granted: the daemon reads the private key file in ~/.ssh,
// decrypts it with a passphrase the user types for this one grant, and keeps the decrypted key in its own memory until the grant
// ends (expiry, revocation, daemon stop). The key is never added to the user's ssh-agent, never written to disk, and the
// passphrase is neither stored nor logged. See docs/SECURITY.md.

const maxKeyFileBytes = 64 << 10

// KeyEntry describes a key the user can pick in a grant.
type KeyEntry struct {
	Fingerprint, Comment, Type string
	Loaded                     bool // in the upstream agent: signed there, no passphrase needed
	Encrypted                  bool // file key that needs a passphrase
}

type localKey struct {
	path  string
	entry KeyEntry
}

// scanLocalKeys finds key pairs in dir: <name>.pub with a regular (non-symlink) private key file <name> next to it.
func scanLocalKeys(dir string) []localKey {
	pubs, _ := filepath.Glob(filepath.Join(dir, "*.pub"))
	var out []localKey
	for _, p := range pubs {
		priv := strings.TrimSuffix(p, ".pub")
		fi, err := os.Lstat(priv)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxKeyFileBytes {
			continue
		}
		pb, err := os.ReadFile(p)
		if err != nil || len(pb) > maxKeyFileBytes {
			continue
		}
		pk, comment, _, _, err := ssh.ParseAuthorizedKey(pb)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(priv)
		if err != nil {
			continue
		}
		_, perr := ssh.ParseRawPrivateKey(raw)
		var missing *ssh.PassphraseMissingError
		enc := errors.As(perr, &missing)
		if perr != nil && !enc {
			continue // not a key we can use
		}
		if comment == "" {
			comment = filepath.Base(priv)
		}
		out = append(out, localKey{path: priv, entry: KeyEntry{Fingerprint: ssh.FingerprintSHA256(pk), Comment: comment, Type: pk.Type(), Encrypted: enc}})
	}
	return out
}

// ErrWrongPassphrase is returned for a wrong passphrase (the request stays open).
var ErrWrongPassphrase = errors.New("wrong passphrase")

// loadLocalSigner decrypts the key file with the passphrase and checks that it is the key with the wanted fingerprint.
// The caller must wipe the passphrase afterwards.
func loadLocalSigner(path, wantFP string, passphrase []byte) (*localSigner, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var key any
	if len(passphrase) > 0 {
		key, err = ssh.ParseRawPrivateKeyWithPassphrase(raw, passphrase)
	} else {
		key, err = ssh.ParseRawPrivateKey(raw)
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("this key needs a passphrase")
		}
		if errors.Is(err, x509.IncorrectPasswordError) || strings.Contains(err.Error(), "decryption password incorrect") {
			return nil, ErrWrongPassphrase
		}
		return nil, fmt.Errorf("cannot read the key: %w", err)
	}
	sg, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return nil, err
	}
	if ssh.FingerprintSHA256(sg.PublicKey()) != wantFP {
		return nil, errors.New("the key file does not match the selected key")
	}
	ls := &localSigner{Signer: sg}
	switch k := key.(type) {
	case *ed25519.PrivateKey:
		ls.wipe = func() { clear(*k) }
	case ed25519.PrivateKey:
		ls.wipe = func() { clear(k) }
	}
	return ls, nil
}

// localSigner is a decrypted key held in memory for one grant. wipe zeroes what Go lets us zero (ed25519 keys);
// other key types are only dropped and left to the garbage collector.
type localSigner struct {
	ssh.Signer
	wipe func()
}

func (l *localSigner) sign(data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if as, ok := l.Signer.(ssh.AlgorithmSigner); ok && l.PublicKey().Type() == ssh.KeyAlgoRSA {
		algo := ssh.KeyAlgoRSA
		switch {
		case flags&agent.SignatureFlagRsaSha512 != 0:
			algo = ssh.KeyAlgoRSASHA512
		case flags&agent.SignatureFlagRsaSha256 != 0:
			algo = ssh.KeyAlgoRSASHA256
		}
		return as.SignWithAlgorithm(rand.Reader, data, algo)
	}
	return l.Signer.Sign(rand.Reader, data)
}

// localStore holds the decrypted keys by grant id.
type localStore struct {
	mu sync.Mutex
	m  map[int64]*localSigner
}

func (b *Broker) setLocal(id int64, s *localSigner) {
	b.local.mu.Lock()
	defer b.local.mu.Unlock()
	if b.local.m == nil {
		b.local.m = map[int64]*localSigner{}
	}
	b.local.m[id] = s
}

func (b *Broker) localSigner(id int64) *localSigner {
	b.local.mu.Lock()
	defer b.local.mu.Unlock()
	return b.local.m[id]
}

// DropLocal removes and wipes the decrypted key of a grant.
func (b *Broker) DropLocal(id int64) {
	b.local.mu.Lock()
	defer b.local.mu.Unlock()
	if s := b.local.m[id]; s != nil && s.wipe != nil {
		s.wipe()
	}
	delete(b.local.m, id)
}

// SweepLocal drops the decrypted keys of grants that are no longer active (expired or revoked).
func (b *Broker) SweepLocal() {
	gs, err := b.St.ListGrants(true, b.now())
	if err != nil {
		return
	}
	active := map[int64]bool{}
	for _, g := range gs {
		active[g.ID] = true
	}
	b.local.mu.Lock()
	var gone []int64
	for id := range b.local.m {
		if !active[id] {
			gone = append(gone, id)
		}
	}
	b.local.mu.Unlock()
	for _, id := range gone {
		b.DropLocal(id)
		b.audit("ssh.key_unloaded", "", fmt.Sprintf("grant=%d (decrypted key removed from memory)", id))
	}
}
