package access

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

type fakeAsker struct {
	svc   *Service
	allow bool
	calls int
}

func (f *fakeAsker) Ask(r store.Request) {
	f.calls++
	go func() { _, _ = f.svc.Decide(r.ID, f.allow, true, "") }()
}

func env(t *testing.T, allow bool) (*Service, *fakeAsker, string) {
	t.Helper()
	home := t.TempDir()
	data := t.TempDir()
	write := func(p, c string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(home, p)), 0o755)
		if err := os.WriteFile(filepath.Join(home, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Documents/a.txt", "hallo")
	write("proj/main.go", "package main")
	write("proj/.env", "SECRET=1")
	write(".ssh/id_rsa", "KEY")
	_ = os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(home, "proj", "sshlink"))
	st, err := store.Open(filepath.Join(data, "db"))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := index.Build(home, index.DefaultBlocklist())
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAsker{allow: allow}
	svc := New(st, ix, data, fa)
	fa.svc = svc
	svc.Timeout = 2 * time.Second
	return svc, fa, data
}

func TestAllowedFileIsCopiedIntoSharedIn(t *testing.T) {
	svc, _, data := env(t, true)
	res, err := svc.Request(context.Background(), "shop", "main", false, "Documents/a.txt", "brauche ich")
	if err != nil || res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("%v %v", res, err)
	}
	want := filepath.Join(data, "sandboxes/shop/shared/in/shop/Documents/a.txt")
	if res.VmPath != want {
		t.Fatalf("%s != %s", res.VmPath, want)
	}
	if b, _ := os.ReadFile(want); string(b) != "hallo" {
		t.Fatal("Inhalt")
	}
}

func TestDeniedCopiesNothing(t *testing.T) {
	svc, _, data := env(t, false)
	res, _ := svc.Request(context.Background(), "shop", "main", false, "Documents/a.txt", "")
	if res.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatal(res)
	}
	if _, err := os.Stat(filepath.Join(data, "sandboxes")); err == nil {
		t.Fatal("nothing may have been copied")
	}
}

func TestBlockedPathNeverAsksUser(t *testing.T) {
	svc, fa, _ := env(t, true)
	for _, p := range []string{".ssh/id_rsa", "proj/.env", "proj/sshlink/id_rsa", "../x", ".SSH/id_rsa"} {
		res, _ := svc.Request(context.Background(), "shop", "main", false, p, "bitte")
		if res.Verdict == pb.Verdict_VERDICT_ALLOWED {
			t.Fatalf("%s must never be approved", p)
		}
	}
	if fa.calls != 0 {
		t.Fatalf("user was asked %d times, this must never happen for blocked paths", fa.calls)
	}
	if rs, _ := svc.S.ListRequests(false, 10); len(rs) == 0 {
		t.Fatal("blocked requests must be logged")
	}
}

func TestFolderSkipsBlockedAndSymlinks(t *testing.T) {
	svc, _, data := env(t, true)
	res, _ := svc.Request(context.Background(), "shop", "main", true, "proj", "")
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatal(res)
	}
	root := filepath.Join(data, "sandboxes/shop/shared/in/shop/proj")
	if _, err := os.Stat(filepath.Join(root, "main.go")); err != nil {
		t.Fatal("main.go fehlt")
	}
	for _, bad := range []string{".env", "sshlink", "sshlink/id_rsa"} {
		if _, err := os.Lstat(filepath.Join(root, bad)); err == nil {
			t.Fatalf("%s must not be copied", bad)
		}
	}
}

func TestKindMismatch(t *testing.T) {
	svc, fa, _ := env(t, true)
	if r, _ := svc.Request(context.Background(), "s", "a", false, "proj", ""); r.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatal("folder via request_file")
	}
	if r, _ := svc.Request(context.Background(), "s", "a", true, "Documents/a.txt", ""); r.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatal("file via request_folder")
	}
	if fa.calls != 0 {
		t.Fatal("no prompt for the wrong kind")
	}
}

type silentAsker struct{}

func (silentAsker) Ask(store.Request) {}

func TestTimeoutExpires(t *testing.T) {
	svc, _, _ := env(t, true)
	svc.Asker = silentAsker{}
	svc.Timeout = 100 * time.Millisecond
	res, _ := svc.Request(context.Background(), "s", "a", false, "Documents/a.txt", "")
	if res.Verdict != pb.Verdict_VERDICT_EXPIRED {
		t.Fatal(res)
	}
	if rs, _ := svc.S.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("expired request must not stay pending")
	}
}

func TestLargeNeedsSecondConfirmation(t *testing.T) {
	svc, _, _ := env(t, true)
	svc.Asker = silentAsker{}
	id, _ := svc.S.AddRequest(store.Request{Sandbox: "s", Kind: "folder", Path: "big", Status: "pending", Bytes: LargeBytes + 1, Files: 1})
	svc.waiters[id] = make(chan decision, 1)
	if _, err := svc.Decide(id, true, false, ""); err == nil || !strings.Contains(err.Error(), "second confirmation") {
		t.Fatalf("without confirm_large it must fail: %v", err)
	}
	if _, err := svc.Decide(id, true, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(id, true, true, ""); err == nil {
		t.Fatal("a second decision must fail")
	}
}

func TestSanitizeAssistantText(t *testing.T) {
	got := sanitize("a\x00b\x1b[31mc"+strings.Repeat("x", 600), 50)
	if strings.ContainsAny(got, "\x00\x1b") || len([]rune(got)) > 51 {
		t.Fatalf("%q", got)
	}
}

type denyNoteAsker struct {
	svc  *Service
	note string
}

func (a *denyNoteAsker) Ask(r store.Request) {
	go func() { _, _ = a.svc.Decide(r.ID, false, false, a.note) }()
}

func TestDenyNoteReachesAssistant(t *testing.T) {
	svc, _, _ := env(t, true)
	svc.Asker = &denyNoteAsker{svc: svc, note: "use Documents/b.txt instead"}
	res, err := svc.Request(context.Background(), "shop", "main", false, "Documents/a.txt", "")
	if err != nil || res.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatalf("%v %v", res, err)
	}
	if !strings.Contains(res.Message, "use Documents/b.txt instead") {
		t.Fatalf("note missing: %q", res.Message)
	}
}
