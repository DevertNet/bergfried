package lima

import (
	"os"
	"context"
	"strings"
	"testing"
)

type fake struct{ calls [][]string; out string }

func (f *fake) Run(_ context.Context, a ...string) ([]byte, error) {
	f.calls = append(f.calls, a)
	return []byte(f.out), nil
}

func TestCreateArgsExactlyTwoMountsNoHome(t *testing.T) {
	a := CreateArgs("bf-x", "vm/bergfried.yaml", "/s/in", "/s/out")
	n := 0
	for i, v := range a {
		if v == "--mount" {
			n++
			if strings.Contains(a[i+1], "/Users/") && !strings.HasPrefix(a[i+1], "/s/") {
				t.Fatal("unerwarteter Mount")
			}
		}
	}
	if n != 2 {
		t.Fatalf("mounts=%d", n)
	}
	if a[len(a)-3] != "/s/out:w" && !strings.Contains(strings.Join(a, " "), "/s/out:w") {
		t.Fatal("out muss beschreibbar sein")
	}
	if strings.Contains(strings.Join(a, " "), "/s/in:w") {
		t.Fatal("in muss read-only sein")
	}
}

func TestList(t *testing.T) {
	f := &fake{out: `{"name":"a","status":"Running","cpus":4}` + "\n" + `{"name":"b","status":"Stopped"}` + "\n"}
	l, err := Client{f}.List(context.Background())
	if err != nil || len(l) != 2 || l[0].Status != "Running" {
		t.Fatalf("%v %v", l, err)
	}
}

func TestRepinHostKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	d := home + "/vm1"
	_ = os.MkdirAll(d, 0o755)
	_ = os.WriteFile(d+"/ssh.known_hosts", []byte("x"), 0o600)
	if err := RepinHostKey("vm1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d + "/ssh.known_hosts"); err == nil {
		t.Fatal("Datei muss weg sein")
	}
	if err := RepinHostKey("vm1"); err != nil {
		t.Fatal("zweiter Aufruf darf nicht scheitern")
	}
}
