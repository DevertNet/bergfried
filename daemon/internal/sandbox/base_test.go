package sandbox

import (
	"context"
	"errors"
	"fmt"
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
