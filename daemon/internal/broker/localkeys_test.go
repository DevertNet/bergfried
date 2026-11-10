package broker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"golang.org/x/crypto/ssh"
)

// writeKeyPair writes <dir>/<name> (private, OpenSSH format, optionally encrypted) and <dir>/<name>.pub.
func writeKeyPair(t *testing.T, dir, name, passphrase string) (ssh.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var blk *pem.Block
	var err error
	if passphrase != "" {
		blk, err = ssh.MarshalPrivateKeyWithPassphrase(priv, name, []byte(passphrase))
	} else {
		blk, err = ssh.MarshalPrivateKey(priv, name)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	spk, _ := ssh.NewPublicKey(pub)
	if err := os.WriteFile(filepath.Join(dir, name+".pub"), ssh.MarshalAuthorizedKey(spk), 0o644); err != nil {
		t.Fatal(err)
	}
	return spk, priv
}

func TestScanLocalKeys(t *testing.T) {
	dir := t.TempDir()
	encPub, _ := writeKeyPair(t, dir, "id_enc", "s3cret")
	plainPub, _ := writeKeyPair(t, dir, "id_plain", "")
	// public key without a private file, and a symlinked private file: both ignored
	orphan, _, _ := ed25519.GenerateKey(rand.Reader)
	op, _ := ssh.NewPublicKey(orphan)
	_ = os.WriteFile(filepath.Join(dir, "orphan.pub"), ssh.MarshalAuthorizedKey(op), 0o644)
	writeKeyPair(t, dir, "real", "")
	_ = os.Remove(filepath.Join(dir, "linked"))
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	_ = os.Rename(filepath.Join(dir, "real.pub"), filepath.Join(dir, "linked.pub"))
	got := map[string]KeyEntry{}
	for _, k := range scanLocalKeys(dir) {
		got[k.entry.Fingerprint] = k.entry
	}
	if len(got) != 2 {
		t.Fatalf("want the two real key pairs, got %v", got)
	}
	if e := got[ssh.FingerprintSHA256(encPub)]; !e.Encrypted || e.Loaded {
		t.Fatalf("encrypted key: %+v", e)
	}
	if e := got[ssh.FingerprintSHA256(plainPub)]; e.Encrypted || e.Loaded {
		t.Fatalf("plain key: %+v", e)
	}
}

func TestLoadLocalSignerPassphraseAndWipe(t *testing.T) {
	dir := t.TempDir()
	pub, _ := writeKeyPair(t, dir, "id", "s3cret")
	fp := ssh.FingerprintSHA256(pub)
	if _, err := loadLocalSigner(filepath.Join(dir, "id"), fp, nil); err == nil || !strings.Contains(err.Error(), "needs a passphrase") {
		t.Fatalf("missing passphrase: %v", err)
	}
	if _, err := loadLocalSigner(filepath.Join(dir, "id"), fp, []byte("wrong")); err != ErrWrongPassphrase {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if _, err := loadLocalSigner(filepath.Join(dir, "id"), "SHA256:other", []byte("s3cret")); err == nil {
		t.Fatal("a key file that does not match the fingerprint must be refused")
	}
	ls, err := loadLocalSigner(filepath.Join(dir, "id"), fp, []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("data to sign")
	sig, err := ls.sign(data, 0)
	if err != nil || pub.Verify(data, sig) != nil {
		t.Fatalf("signature must verify: %v", err)
	}
	ls.wipe()
	if sig, err := ls.sign(data, 0); err == nil && pub.Verify(data, sig) == nil {
		t.Fatal("after wipe the key must no longer produce valid signatures")
	}
}

// chanAsker hands the pending request to the test, which decides it itself.
type chanAsker struct{ ch chan store.Request }

func (a chanAsker) AskSSH(r store.Request, _ SSHInfo) { a.ch <- r }

func TestPassphraseKeyGrantLifecycle(t *testing.T) {
	f := setup(t)
	hostFP := ssh.FingerprintSHA256(f.host.PublicKey())
	hc, _ := LoadHostConfig("")
	s := NewService(f.b, func(context.Context, string) ([]string, string, error) { return []string{hostFP}, "known_hosts", nil }, &fakeHooks{}, nil, hc)
	s.Timeout = 5 * time.Second
	dir := t.TempDir()
	s.SSHDir = dir
	pub, _ := writeKeyPair(t, dir, "id_work", "s3cret")
	fp := ssh.FingerprintSHA256(pub)

	// the key is offered although it is not in the agent
	ks, _ := s.KeysAll()
	var entry *KeyEntry
	for i := range ks {
		if ks[i].Fingerprint == fp {
			entry = &ks[i]
		}
	}
	if entry == nil || entry.Loaded || !entry.Encrypted {
		t.Fatalf("file key must be listed as not loaded + encrypted: %+v", ks)
	}
	agentKeysBefore, _ := f.b.Up.List()

	ch := make(chan store.Request, 1)
	s.Asker = chanAsker{ch}
	done := make(chan *pb.SSHAccessResult, 1)
	go func() {
		res, _ := s.RequestSSH(context.Background(), "shop", "main", "example.com", "", 15, "deploy")
		done <- res
	}()
	r := <-ch
	// wrong passphrase: refused, request stays open
	pw := []byte("wrong")
	if _, err := s.DecideSSH(Decision{ID: r.ID, Allow: true, Minutes: 30, KeyFP: fp, User: "claude", Passphrase: pw}); err != ErrWrongPassphrase {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if string(pw) == "wrong" {
		t.Fatal("the passphrase slice must be zeroed after use")
	}
	// missing passphrase for an encrypted key
	if _, err := s.DecideSSH(Decision{ID: r.ID, Allow: true, Minutes: 30, KeyFP: fp, User: "claude"}); err == nil {
		t.Fatal("an encrypted key needs a passphrase")
	}
	if _, err := s.DecideSSH(Decision{ID: r.ID, Allow: true, Minutes: 30, KeyFP: fp, User: "claude", Passphrase: []byte("s3cret")}); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("verdict %v", res.Verdict)
	}
	gs, _ := f.b.St.ListGrants(true, f.b.now())
	if len(gs) != 1 || f.b.localSigner(gs[0].ID) == nil {
		t.Fatalf("grant with a decrypted key in memory expected: %+v", gs)
	}

	// ssh in the VM: the key appears in the broker's agent and signs; the user's real agent is untouched
	a := f.client("shop")
	if ks, _ := a.List(); len(ks) != 1 || ssh.FingerprintSHA256(ks[0]) != fp {
		t.Fatalf("list: %v", ks)
	}
	sid := []byte("sid-pass")
	if err := bind(t, a, f.host, sid, false, f.host); err != nil {
		t.Fatal(err)
	}
	sig, err := a.Sign(pub, authData(sid))
	if err != nil || pub.Verify(authData(sid), sig) != nil {
		t.Fatalf("sign: %v", err)
	}
	if after, _ := f.b.Up.List(); len(after) != len(agentKeysBefore) {
		t.Fatal("the key must never be added to the upstream agent")
	}

	// neither passphrase nor key material in the audit log
	entries, _ := f.b.St.AuditList(100)
	for _, e := range entries {
		if strings.Contains(e.Detail, "s3cret") || strings.Contains(e.Detail, "wrong") {
			t.Fatalf("passphrase in the audit log: %+v", e)
		}
	}

	// expiry: signing stops at once, the sweep removes the decrypted key and audits it
	f.now = f.now.Add(time.Hour)
	a2 := f.client("shop")
	_ = bind(t, a2, f.host, []byte("sid-late"), false, f.host)
	if _, err := a2.Sign(pub, authData([]byte("sid-late"))); err == nil {
		t.Fatal("an expired grant must not sign")
	}
	s.Sweep(context.Background())
	if f.b.localSigner(gs[0].ID) != nil {
		t.Fatal("the decrypted key must be dropped after expiry")
	}
	if _, err := s.Extend(context.Background(), gs[0].ID, 10); err == nil {
		t.Fatal("extending a grant whose decrypted key is gone must ask for the passphrase again")
	}
}

func TestRevokeDropsDecryptedKey(t *testing.T) {
	f := setup(t)
	ls := &localSigner{Signer: f.userKey, wipe: func() {}}
	id := f.grant(t, "shop", 5, false)
	f.b.setLocal(id, ls)
	hc, _ := LoadHostConfig("")
	s := NewService(f.b, nil, &fakeHooks{}, nil, hc)
	if err := s.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.b.localSigner(id) != nil {
		t.Fatal("revoking must drop the decrypted key")
	}
}
