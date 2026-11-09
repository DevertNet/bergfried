package index

import (
	"os"
	"strings"
	"sync"
)

// Gate holds back the first index build until the app has explained it. Building the index reads the home folder, which makes macOS ask
// for access to Documents, Desktop, Downloads and so on, and an ad-hoc signed build gets asked again after every update. The gate is
// closed per daemon version: the app opens it (Open) after showing the explanation, and the version is remembered in a file so a
// plain daemon restart of the same version does not ask the user again. A daemon that does not run from the app bundle is never gated.
type Gate struct {
	ackPath string
	version string
	once    sync.Once
	ch      chan struct{}
	mu      sync.Mutex
	waiting bool
}

// NewGate returns a gate. enforce=false (daemon not started from the app) or an acknowledgement of this version opens it at once.
func NewGate(ackPath, version string, enforce bool) *Gate {
	g := &Gate{ackPath: ackPath, version: version, ch: make(chan struct{})}
	b, _ := os.ReadFile(ackPath)
	if !enforce || strings.TrimSpace(string(b)) == version {
		g.once.Do(func() { close(g.ch) })
		return g
	}
	g.waiting = true
	return g
}

// Wait is closed once the index may be built.
func (g *Gate) Wait() <-chan struct{} { return g.ch }

// Waiting reports whether the app still has to confirm.
func (g *Gate) Waiting() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting
}

// Open records the acknowledgement for this version and releases the index build.
func (g *Gate) Open() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.waiting {
		return nil
	}
	if err := os.WriteFile(g.ackPath, []byte(g.version+"\n"), 0o600); err != nil {
		return err
	}
	g.waiting = false
	g.once.Do(func() { close(g.ch) })
	return nil
}
