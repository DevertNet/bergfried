package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// baseFake simulates limactl for the base VM: status of the base instance ("" = absent) and an optional failing start.
type baseFake struct {
	base      string
	baseName  string
	startFail bool
	calls     []string
}

func (f *baseFake) Run(_ context.Context, a ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(a, " "))
	switch a[0] {
	case "list":
		if f.base == "" {
			return nil, nil
		}
		return []byte(fmt.Sprintf(`{"name":%q,"status":%q}`+"\n", f.baseName, f.base)), nil
	case "delete":
		f.base = ""
	case "create":
		f.base = "Stopped"
	case "start":
		if f.startFail {
			f.base = "Running"
			return nil, errors.New("probe failed")
		}
		f.base = "Running"
	case "stop":
		f.base = "Stopped"
	}
	return nil, nil
}

func baseMgr(t *testing.T, f *baseFake) *Manager {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{L: lima.Client{R: f}, S: st, Actor: "t", Template: "t.yaml"}
	f.baseName = m.BaseName()
	return m
}

func has(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestBuildBaseStartUsesLongTimeout(t *testing.T) {
	f := &baseFake{}
	if err := baseMgr(t, f).BuildBase(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !has(f.calls, "start --tty=false --timeout=45m0s ") {
		t.Fatalf("base start must pass a long --timeout: %v", f.calls)
	}
}

func TestBuildBaseKeepsStoppedBase(t *testing.T) {
	f := &baseFake{base: "Stopped"}
	if err := baseMgr(t, f).BuildBase(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if has(f.calls, "create") || has(f.calls, "delete") {
		t.Fatalf("a stopped base must be kept: %v", f.calls)
	}
}

func TestBuildBaseRebuildsLeftoverRunningBase(t *testing.T) {
	f := &baseFake{base: "Running"} // "cannot clone a running instance"
	if err := baseMgr(t, f).BuildBase(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if !has(f.calls, "delete") || !has(f.calls, "create") || f.base != "Stopped" {
		t.Fatalf("leftover must be removed and rebuilt, base=%q calls=%v", f.base, f.calls)
	}
}

func TestBuildBaseFailureRemovesHalfBuiltVM(t *testing.T) {
	f := &baseFake{startFail: true}
	err := baseMgr(t, f).BuildBase(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "probe failed") || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("error: %v", err)
	}
	if f.base != "" {
		t.Fatalf("the half-built base must be deleted, status %q", f.base)
	}
}

func TestBuildBaseFailureKeepsLogs(t *testing.T) {
	limaHome := t.TempDir()
	t.Setenv("LIMA_HOME", limaHome)
	f := &baseFake{startFail: true}
	m := baseMgr(t, f)
	m.Home = t.TempDir()
	vmDir := filepath.Join(limaHome, m.BaseName())
	if err := os.MkdirAll(vmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "ha.stderr.log"), []byte("hostagent said something\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := m.BuildBase(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "logs were saved to") {
		t.Fatalf("error: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(m.BaseFailureDir(), "*.log"))
	if len(files) != 1 {
		t.Fatalf("saved logs: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "probe failed") || !strings.Contains(string(b), "hostagent said something") || !strings.Contains(string(b), "cloud-init") {
		t.Fatalf("log content:\n%s", b)
	}
}
