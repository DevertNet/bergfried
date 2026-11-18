package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

type failRunner struct{ calls int }

func (r *failRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	r.calls++
	return nil, errors.New("limactl must not be started")
}

// Reconcile takes the VM list from Daemon.VMs when it is set, so the 5 s loop shares the monitor's cache.
func TestReconcileUsesVMsHook(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &failRunner{}
	called := 0
	d := &Daemon{St: st, L: lima.Client{R: r}, VMs: func(ctx context.Context) ([]lima.Instance, error) {
		called++
		return nil, nil
	}}
	if err := d.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called != 1 || r.calls != 0 {
		t.Fatalf("hook calls=%d, limactl calls=%d", called, r.calls)
	}
	d.VMs = nil
	if err := d.Reconcile(context.Background()); err == nil || r.calls != 1 {
		t.Fatalf("without the hook Reconcile asks limactl (err=%v, calls=%d)", err, r.calls)
	}
}
