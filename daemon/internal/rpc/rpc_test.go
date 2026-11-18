package rpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghapp"
	"github.com/DevertNet/bergfried/daemon/internal/gitaccess"
	"github.com/DevertNet/bergfried/daemon/internal/skills"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"google.golang.org/grpc/metadata"
)

func TestListenUnixPerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "d", "x.sock")
	l, err := ListenUnix(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v", fi.Mode().Perm())
	}
	l2, err := ListenUnix(p) // old socket is replaced
	if err != nil {
		t.Fatal(err)
	}
	l2.Close()
}

func TestFolderHeaderValidated(t *testing.T) {
	mk := func(v string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs(FolderHeader, v))
	}
	if folderFrom(mk("shop/main")) != "shop/main" {
		t.Fatal("valid name")
	}
	for _, bad := range []string{"Evil\nname", "a b", "../x", "", "x'; rm"} {
		if folderFrom(mk(bad)) != "unknown" {
			t.Fatalf("%q", bad)
		}
	}
	if folderFrom(context.Background()) != "unknown" {
		t.Fatal("without header")
	}
}

func skillServer(t *testing.T) (*sandboxServer, *skills.Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	svc := skills.NewService(st, dir)
	return &sandboxServer{sandbox: "shop", skills: svc, st: st}, svc, st, dir
}

func pendingSkill(t *testing.T, st *store.Store) store.Request {
	t.Helper()
	for i := 0; i < 300; i++ {
		rs, err := st.ListRequests(true, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			if r.Kind == "skill" {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending skill request")
	return store.Request{}
}

type skillAnswer struct {
	res *pb.SkillProposalResult
	err error
}

func proposeSkill(s *sandboxServer, ch []*pb.SkillChange) chan skillAnswer {
	out := make(chan skillAnswer, 1)
	go func() {
		r, err := s.ProposeSkillChange(context.Background(), &pb.SkillProposalRequest{Reason: "improve the demo skill", Changes: ch})
		out <- skillAnswer{r, err}
	}()
	return out
}

func TestProposeSkillChangeAllowed(t *testing.T) {
	s, svc, st, dir := skillServer(t)
	ch := proposeSkill(s, []*pb.SkillChange{{Path: "demo/SKILL.md", Content: "new content\n"}})
	req := pendingSkill(t, st)
	if status, err := svc.Decide(context.Background(), req.ID, true, ""); err != nil || status != "allowed" {
		t.Fatalf("Decide = %q, %v", status, err)
	}
	a := <-ch
	if a.err != nil || a.res.Verdict != pb.Verdict_VERDICT_ALLOWED || !strings.Contains(a.res.Message, "Approved") {
		t.Fatalf("result = %+v, %v", a.res, a.err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "demo", "SKILL.md"))
	if err != nil || string(b) != "new content\n" {
		t.Fatalf("file = %q, %v", b, err)
	}
}

func TestProposeSkillChangeDenied(t *testing.T) {
	s, svc, st, _ := skillServer(t)
	ch := proposeSkill(s, []*pb.SkillChange{{Path: "demo/SKILL.md", Content: "new content\n"}})
	req := pendingSkill(t, st)
	if status, err := svc.Decide(context.Background(), req.ID, false, "not now"); err != nil || status != "denied" {
		t.Fatalf("Decide = %q, %v", status, err)
	}
	a := <-ch
	if a.err != nil || a.res.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(a.res.Message, "not now") {
		t.Fatalf("result = %+v, %v", a.res, a.err)
	}
}

func TestProposeSkillChangeExpired(t *testing.T) {
	s, svc, _, _ := skillServer(t)
	svc.Timeout = 20 * time.Millisecond
	r, err := s.ProposeSkillChange(context.Background(), &pb.SkillProposalRequest{
		Changes: []*pb.SkillChange{{Path: "demo/SKILL.md", Content: "new content\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != pb.Verdict_VERDICT_EXPIRED || !strings.Contains(r.Message, "expired") {
		t.Fatalf("result = %+v", r)
	}
}

func TestProposeSkillChangeInvalidProposal(t *testing.T) {
	s, _, _, _ := skillServer(t)
	r, err := s.ProposeSkillChange(context.Background(), &pb.SkillProposalRequest{Reason: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(r.Message, "Invalid proposal") {
		t.Fatalf("result = %+v", r)
	}
}

func TestProposeSkillChangeWithoutService(t *testing.T) {
	s := &sandboxServer{sandbox: "shop"}
	if _, err := s.ProposeSkillChange(context.Background(), &pb.SkillProposalRequest{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v", err)
	}
}

// The sandbox is derived from the socket: a sandbox cannot use or end the git grants of another one.
func TestGitCredentialAndReleaseAreScopedToTheSandbox(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	gid, err := st.AddGitGrant(store.GitGrant{Sandbox: "shop", Repo: "o/r", Write: true, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// no app configured: the helper gets no credentials (not an error), tools say so
	gs := gitaccess.NewService(st, ghapp.NewClient(ghapp.Store{Dir: t.TempDir()}))
	blog := &sandboxServer{sandbox: "blog", git: gs, st: st}
	r, err := blog.GitCredential(context.Background(), &pb.GitCredentialRequest{Host: "github.com", Path: "o/r.git"})
	if err != nil || r.Password != "" || r.Username != "" || r.Message == "" {
		t.Fatalf("%+v %v", r, err)
	}
	rel, err := blog.ReleaseGitAccess(context.Background(), &pb.GitReleaseRequest{TaskId: gid})
	if err != nil || rel.Released {
		t.Fatalf("a foreign sandbox must not end the grant: %+v %v", rel, err)
	}
	if g, _ := st.GetGitGrant(gid); g.Ended != 0 {
		t.Fatal("grant of another sandbox was ended")
	}
	if _, err := (&sandboxServer{sandbox: "shop", st: st}).RequestGitAccess(context.Background(), &pb.GitAccessRequest{Repo: "o/r"}); err == nil {
		t.Fatal("without the git service the request must fail clearly")
	}
}

// A refused request has no expiry: the zero time must not leak out as a huge negative number.
func TestRequestGitAccessRefusedHasNoExpiry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	gs := gitaccess.NewService(st, ghapp.NewClient(ghapp.Store{Dir: t.TempDir()})) // no app configured
	r, err := (&sandboxServer{sandbox: "shop", git: gs, st: st}).RequestGitAccess(context.Background(), &pb.GitAccessRequest{Repo: "o/r", Write: true})
	if err != nil || r.Verdict != pb.Verdict_VERDICT_DENIED || r.ExpiresUnix != 0 || r.TaskId != 0 || !strings.Contains(r.Message, "No GitHub App") {
		t.Fatalf("%+v %v", r, err)
	}
}
