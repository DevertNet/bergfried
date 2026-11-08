package lima

import (
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
