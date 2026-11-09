package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

const tplA = "minimumLimaVersion: 2.0.0\nvmType: vz\nmounts: []\nportForwards:\n- guestSocket: /run/user/{{.UID}}/bergfried.sock\n  hostSocket: x/bergfried.sock\n  reverse: true\n- guestSocket: /run/user/{{.UID}}/bergfried-agent.sock\n  hostSocket: x/ssh-agent.sock\n  reverse: true\n"

func newStore(t *testing.T, seed string) TemplateStore {
	t.Helper()
	d := t.TempDir()
	sp := filepath.Join(d, "seed.yaml")
	if err := os.WriteFile(sp, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := TemplateStore{Dir: filepath.Join(d, "template"), Seed: sp}
	if err := ts.Ensure(); err != nil {
		t.Fatal(err)
	}
	return ts
}

func setSeed(t *testing.T, ts TemplateStore, content string) {
	t.Helper()
	if err := os.WriteFile(ts.Seed, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptSeedReplacesUneditedCopy(t *testing.T) {
	ts := newStore(t, tplA)
	rev := ts.Rev()
	setSeed(t, ts, tplA+"# newer\n")
	ok, err := ts.AdoptSeed()
	if err != nil || !ok {
		t.Fatalf("adopted=%v err=%v", ok, err)
	}
	if cur, _ := ts.Read(); cur != tplA+"# newer\n" {
		t.Fatalf("copy not replaced: %q", cur)
	}
	if ts.Rev() != rev+1 {
		t.Fatalf("revision %d, want %d", ts.Rev(), rev+1)
	}
	if ok, _ := ts.AdoptSeed(); ok {
		t.Fatal("second run must be a no-op")
	}
}

func TestAdoptSeedKeepsEditedCopy(t *testing.T) {
	ts := newStore(t, tplA)
	if err := os.WriteFile(ts.Path(), []byte(tplA+"# my edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSeed(t, ts, tplA+"# newer\n")
	ok, err := ts.AdoptSeed()
	if err != nil || ok {
		t.Fatalf("adopted=%v err=%v, edited copy must stay", ok, err)
	}
	if cur, _ := ts.Read(); cur != tplA+"# my edit\n" {
		t.Fatalf("edited copy changed: %q", cur)
	}
	if !ts.SeedNewer() {
		t.Fatal("SeedNewer must warn about the edited copy")
	}
	if err := ts.Reset(); err != nil {
		t.Fatal(err)
	}
	if ts.SeedNewer() {
		t.Fatal("no warning after reset")
	}
}

func TestAdoptSeedRecognisesKnownOldTemplate(t *testing.T) {
	ts := newStore(t, tplA)
	_ = os.Remove(ts.seedHashPath()) // install from before seed.sha256 existed
	knownSeedHashes[sumHex([]byte(tplA))] = true
	defer delete(knownSeedHashes, sumHex([]byte(tplA)))
	setSeed(t, ts, tplA+"# newer\n")
	if ok, err := ts.AdoptSeed(); err != nil || !ok {
		t.Fatalf("adopted=%v err=%v", ok, err)
	}
}

func TestAdoptSeedUnknownCopyWithoutRecordIsKept(t *testing.T) {
	ts := newStore(t, tplA)
	_ = os.Remove(ts.seedHashPath())
	setSeed(t, ts, tplA+"# newer\n")
	if ok, _ := ts.AdoptSeed(); ok {
		t.Fatal("unknown copy without record must not be replaced")
	}
}
