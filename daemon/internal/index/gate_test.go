package index

import (
	"path/filepath"
	"testing"
	"time"
)

func isOpen(g *Gate) bool {
	select {
	case <-g.Wait():
		return true
	case <-time.After(20 * time.Millisecond):
		return false
	}
}

func TestGateWaitsUntilOpenedAndRemembersVersion(t *testing.T) {
	ack := filepath.Join(t.TempDir(), "index-ack")
	g := NewGate(ack, "1.0.0", true)
	if !g.Waiting() || isOpen(g) {
		t.Fatal("a new version must wait for the app")
	}
	if err := g.Open(); err != nil {
		t.Fatal(err)
	}
	if g.Waiting() || !isOpen(g) {
		t.Fatal("gate must be open after Open")
	}
	if g2 := NewGate(ack, "1.0.0", true); g2.Waiting() || !isOpen(g2) {
		t.Fatal("same version after a restart must not ask again")
	}
	if g3 := NewGate(ack, "1.0.1", true); !g3.Waiting() || isOpen(g3) {
		t.Fatal("a new version (after an update) must wait again")
	}
}

func TestGateNotEnforcedIsOpen(t *testing.T) {
	g := NewGate(filepath.Join(t.TempDir(), "index-ack"), "1.0.0", false)
	if g.Waiting() || !isOpen(g) {
		t.Fatal("a daemon outside the app bundle must not be gated")
	}
}
