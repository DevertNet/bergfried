package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// OpenCodeTarget is the stamp target of the sandbox-level OpenCode service.
const OpenCodeTarget = "opencode"

// ClaudeTarget is the stamp target of Claude in a worktree.
func ClaudeTarget(worktree string) string { return "claude:" + worktree }

// mcpHashKey identifies a binary version: path, size and modification time.
type mcpHashKey struct {
	path string
	size int64
	mod  int64
}

// The binary hash is cached (Manager is copied by value in places, so the cache cannot live in it).
var (
	mcpHashMu    sync.Mutex
	mcpHashKeyAt mcpHashKey
	mcpHashSum   string
)

// mcpHash returns the hex sha256 of the binary at path, re-hashing it only when path, size or
// modification time changed.
func mcpHash(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	k := mcpHashKey{path: path, size: fi.Size(), mod: fi.ModTime().UnixNano()}
	mcpHashMu.Lock()
	defer mcpHashMu.Unlock()
	if mcpHashSum != "" && mcpHashKeyAt == k {
		return mcpHashSum, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	mcpHashKeyAt, mcpHashSum = k, hex.EncodeToString(h.Sum(nil))
	return mcpHashSum, nil
}

// CurrentStamp hashes what would be installed right now: the guidance text for this sandbox and
// the bergfried-mcp binary.
func (m *Manager) CurrentStamp(sandbox string) (store.HelperStamp, error) {
	outDir := filepath.Join(m.Home, "sandboxes", sandbox, "shared", "out")
	g := sha256.Sum256([]byte(guidance(outDir)))
	bin := McpBin()
	mcp, err := mcpHash(bin)
	if err != nil {
		return store.HelperStamp{}, fmt.Errorf("bergfried-mcp not readable (%s): %w", bin, err)
	}
	return store.HelperStamp{Guidance: hex.EncodeToString(g[:]), MCP: mcp}, nil
}

// RestartReasons turns the stored stamp into the reasons why an assistant should be restarted
// ("guidance" and/or "mcp", in this order). Without a stamp the reason is "unknown".
func RestartReasons(have bool, stored, current store.HelperStamp) []string {
	if !have {
		return []string{"unknown"}
	}
	out := []string{}
	if stored.Guidance != current.Guidance {
		out = append(out, "guidance")
	}
	if stored.MCP != current.MCP {
		out = append(out, "mcp")
	}
	return out
}

// recordStamp stores the current stamp after an assistant was really started. Failures are only
// audited: a missing stamp merely shows a harmless hint later.
func (m *Manager) recordStamp(sandbox, target string) {
	st, err := m.CurrentStamp(sandbox)
	if err != nil {
		m.audit("helpers.stamp_failed", sandbox, "", err.Error())
		return
	}
	if err := m.S.SetHelperStamp(sandbox, target, st); err != nil {
		m.audit("helpers.stamp_failed", sandbox, "", err.Error())
	}
}
