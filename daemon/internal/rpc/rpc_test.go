package rpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
