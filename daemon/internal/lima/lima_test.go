package lima

import (
	"context"
	"os"
	"strings"
	"testing"
)

type fake struct {
	calls [][]string
	out   string
}

func (f *fake) Run(_ context.Context, a ...string) ([]byte, error) {
	f.calls = append(f.calls, a)
	return []byte(f.out), nil
}

func TestCreateArgsExactlyThreeMountsNoHome(t *testing.T) {
	a := CreateArgs("bf-x", "vm/bergfried.yaml", "/s/in", "/s/out", "/s/skills")
	n := 0
	for i, v := range a {
		if v == "--mount" {
			n++
			if strings.Contains(a[i+1], "/Users/") && !strings.HasPrefix(a[i+1], "/s/") {
				t.Fatal("unerwarteter Mount")
			}
		}
	}
	if n != 3 {
		t.Fatalf("mounts=%d", n)
	}
	joined := strings.Join(a, " ")
	if !strings.Contains(joined, "/s/out:w") {
		t.Fatal("out must be writable")
	}
	if strings.Contains(joined, "/s/in:w") {
		t.Fatal("in must be read-only")
	}
	if !strings.Contains(joined, "/s/skills") {
		t.Fatal("skills must be mounted")
	}
	if strings.Contains(joined, "/s/skills:w") {
		t.Fatal("skills must be read-only")
	}
}

func TestMountArgs(t *testing.T) {
	got := strings.Join(MountArgs("/s/in", "/s/out", "/s/skills"), " ")
	want := "--mount-only /s/in --mount-only /s/out:w --mount-only /s/skills"
	if got != want {
		t.Fatalf("MountArgs = %q, want %q", got, want)
	}
}

func TestEditMountsArgs(t *testing.T) {
	f := &fake{}
	if err := (Client{f}).EditMounts(context.Background(), "vm1", "/s/in", "/s/out", "/s/skills"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls=%v", f.calls)
	}
	got := strings.Join(f.calls[0], " ")
	want := "edit --tty=false vm1 --mount-only /s/in --mount-only /s/out:w --mount-only /s/skills"
	if got != want {
		t.Fatalf("EditMounts = %q, want %q", got, want)
	}
}

func TestHasMount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	d := home + "/vm1"
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `mounts:
- location: /s/in
  mountPoint: /s/in
  writable: false
- location: /s/out
  mountPoint: /s/out
  writable: true
- location: /s/skills
  mountPoint: /s/skills
- location: /s/other
  mountPoint: /s/other
  writable: false
`
	if err := os.WriteFile(d+"/lima.yaml", []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		location string
		writable bool
		want     bool
	}{
		{"read-only present", "/s/in", false, true},
		{"read-only present, but writable asked", "/s/in", true, false},
		{"writable present", "/s/out", true, true},
		{"writable present, but read-only asked", "/s/out", false, false},
		{"skills read-only", "/s/skills", false, true},
		{"skills asked writable", "/s/skills", true, false},
		{"absent", "/s/missing", false, false},
	}
	for _, tt := range tests {
		got, err := HasMount("vm1", tt.location, tt.writable)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("%s: HasMount(%q, %v) = %v, want %v", tt.name, tt.location, tt.writable, got, tt.want)
		}
	}
	if _, err := HasMount("no-such-file", "/s/in", false); err == nil {
		t.Fatal("missing lima.yaml must be an error")
	}
	if _, err := HasMount("no-such-dir", "/s/in", false); err == nil {
		t.Fatal("missing instance dir must be an error")
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
		t.Fatal("file must be gone")
	}
	if err := RepinHostKey("vm1"); err != nil {
		t.Fatal("a second call must not fail")
	}
}
