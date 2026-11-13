package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/store"
)

func newSvcFx(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	dir := setup(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	return NewService(st, dir), st, dir
}

// waitSkillRequests waits until at least n skill requests are pending and
// returns them.
func waitSkillRequests(t *testing.T, st *store.Store, n int) []store.Request {
	t.Helper()
	for i := 0; i < 300; i++ {
		rs, err := st.ListRequests(true, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Request
		for _, r := range rs {
			if r.Kind == kindSkill {
				out = append(out, r)
			}
		}
		if len(out) >= n {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fewer than %d open skill requests", n)
	return nil
}

func waitSkillRequest(t *testing.T, st *store.Store) store.Request {
	t.Helper()
	return waitSkillRequests(t, st, 1)[0]
}

type answer struct {
	res Result
	err error
}

func propose(s *Service, sandbox string, cs []Change, reason string) chan answer {
	ch := make(chan answer, 1)
	go func() {
		r, err := s.Request(context.Background(), sandbox, "main", reason, cs)
		ch <- answer{r, err}
	}()
	return ch
}

func openSkillCount(t *testing.T, st *store.Store) int {
	t.Helper()
	rs, err := st.ListRequests(true, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rs {
		if r.Kind == kindSkill {
			n++
		}
	}
	return n
}

func countAudit(t *testing.T, st *store.Store, event string) int {
	t.Helper()
	es, err := st.AuditList(200)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		if e.Event == event {
			n++
		}
	}
	return n
}

func countRequests(t *testing.T, st *store.Store) int {
	t.Helper()
	rs, err := st.ListRequests(false, 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(rs)
}

func hasAudit(t *testing.T, st *store.Store, event string) store.AuditEntry {
	t.Helper()
	es, err := st.AuditList(200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Event == event {
			return e
		}
	}
	t.Fatalf("audit entry %q missing", event)
	return store.AuditEntry{}
}

func TestServiceProposalShowsUpAndAllowApplies(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	writeSkillFile(t, dir, "demo/SKILL.md", "old\n")
	writeSkillFile(t, dir, "other/gone.txt", "x\ny\n")
	cs := []Change{
		{Path: "demo/SKILL.md", Content: []byte("new\n")},
		{Path: "demo/new.txt", Content: []byte("one\ntwo\n")},
		{Path: "other/gone.txt", Delete: true},
	}
	ch := propose(svc, "shop", cs, "improve the demo skill")

	req := waitSkillRequest(t, st)
	if req.Kind != kindSkill || req.Status != "pending" || req.Sandbox != "shop" || req.Worktree != "main" {
		t.Fatalf("request = %+v", req)
	}
	if req.Path != "demo, other" {
		t.Fatalf("Path = %q, want %q", req.Path, "demo, other")
	}
	if req.Files != 3 {
		t.Fatalf("Files = %d, want 3", req.Files)
	}
	if want := int64(len("new\n") + len("one\ntwo\n")); req.Bytes != want {
		t.Fatalf("Bytes = %d, want %d", req.Bytes, want)
	}
	if req.Reason != "improve the demo skill" {
		t.Fatalf("Reason = %q", req.Reason)
	}

	pv, err := DetailOf(req.Extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv) != 3 {
		t.Fatalf("DetailOf returned %d entries, want 3", len(pv))
	}
	byPath := map[string]FileDetail{}
	for _, d := range pv {
		byPath[d.Path] = d
	}
	if d := byPath["demo/SKILL.md"]; d.Op != "modify" || !strings.Contains(d.Diff, "-old\n") || !strings.Contains(d.Diff, "+new\n") {
		t.Fatalf("SKILL.md detail = %+v", d)
	}
	if d := byPath["demo/new.txt"]; d.Op != "create" || !strings.Contains(d.Diff, "+one\n") || !strings.Contains(d.Diff, "+two\n") {
		t.Fatalf("new.txt detail = %+v", d)
	}
	if d := byPath["other/gone.txt"]; d.Op != "delete" || !strings.Contains(d.Diff, "-x\n") || !strings.Contains(d.Diff, "-y\n") {
		t.Fatalf("gone.txt detail = %+v", d)
	}
	if _, err := DetailOf("not json"); err == nil {
		t.Fatal("DetailOf must reject unparsable Extra")
	}

	if status, err := svc.Decide(context.Background(), req.ID, true, ""); err != nil || status != "allowed" {
		t.Fatalf("Decide = %q, %v", status, err)
	}
	a := <-ch
	if a.err != nil || !a.res.Allowed || !strings.Contains(a.res.Message, "Approved and applied") {
		t.Fatalf("requester got %+v", a)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "demo", "SKILL.md")); err != nil || string(b) != "new\n" {
		t.Fatalf("SKILL.md = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "demo", "new.txt")); err != nil || string(b) != "one\ntwo\n" {
		t.Fatalf("new.txt = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Fatalf("other folder was not pruned: %v", err)
	}
	if got, err := st.GetRequest(req.ID); err != nil || got.Status != "allowed" {
		t.Fatalf("row = %+v, %v", got, err)
	}
	hasAudit(t, st, "skills.requested")
	hasAudit(t, st, "skills.allowed")
}

func TestServiceDenyChangesNothingAndPassesNote(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	ch := propose(svc, "shop", []Change{{Path: "demo/new.txt", Content: []byte("x\n")}}, "")
	req := waitSkillRequest(t, st)

	if status, err := svc.Decide(context.Background(), req.ID, false, "use the staging folder instead\x01\u200b"); err != nil || status != "denied" {
		t.Fatalf("Decide = %q, %v", status, err)
	}
	a := <-ch
	if a.err != nil || a.res.Allowed {
		t.Fatalf("requester got %+v", a)
	}
	if !strings.Contains(a.res.Message, "declined") || !strings.Contains(a.res.Message, "use the staging folder instead") {
		t.Fatalf("message = %q", a.res.Message)
	}
	if strings.ContainsAny(a.res.Message, "\x01\u200b") {
		t.Fatalf("message carries control characters: %q", a.res.Message)
	}
	if _, err := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("denied proposal touched the folder: %v", err)
	}
	if got, _ := st.GetRequest(req.ID); got.Status != "denied" {
		t.Fatalf("status = %q, want denied", got.Status)
	}
	hasAudit(t, st, "skills.denied")
}

func TestServiceReasonIsCleaned(t *testing.T) {
	svc, st, _ := newSvcFx(t)
	reason := "line\x00 one\u200b\u2028\nline two"
	ch := propose(svc, "shop", []Change{{Path: "demo/a.txt", Content: []byte("x\n")}}, reason)
	req := waitSkillRequest(t, st)
	if req.Reason != "line one\nline two" {
		t.Fatalf("Reason = %q", req.Reason)
	}
	if _, err := svc.Decide(context.Background(), req.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	<-ch

	ch = propose(svc, "shop", []Change{{Path: "demo/b.txt", Content: []byte("y\n")}}, strings.Repeat("x", 600))
	req = waitSkillRequest(t, st)
	if n := len([]rune(req.Reason)); n != maxReasonRunes {
		t.Fatalf("long Reason has %d runes, want %d", n, maxReasonRunes)
	}
	if _, err := svc.Decide(context.Background(), req.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	<-ch
}

func TestServiceInvalidProposals(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	tests := []struct {
		name    string
		changes []Change
	}{
		{"bad path", []Change{{Path: "../evil/x", Content: []byte("x")}}},
		{"control character", []Change{{Path: "demo/a\x01b", Content: []byte("x")}}},
		{"control character in content", []Change{{Path: "demo/a.txt", Content: []byte("x\x01y")}}},
		{"zero width space", []Change{{Path: "demo/a\u200bb", Content: []byte("x")}}},
		{"zero width space in content", []Change{{Path: "demo/z.txt", Content: []byte("x\u200by")}}},
		{"too large", []Change{{Path: "demo/a.txt", Content: []byte(strings.Repeat("a", MaxFileBytes+1))}}},
		{"reserved skill", []Change{{Path: "synced/x", Content: []byte("x")}}},
	}
	for _, tt := range tests {
		res, err := svc.Request(context.Background(), "shop", "main", "", tt.changes)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if res.Allowed || !strings.HasPrefix(res.Message, "Invalid proposal:") {
			t.Errorf("%s: result = %+v, want an invalid proposal message", tt.name, res)
		}
	}
	if n := countRequests(t, st); n != 0 {
		t.Fatalf("%d rows were stored for invalid proposals", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); !os.IsNotExist(err) {
		t.Fatalf("invalid proposal wrote outside the folder: %v", err)
	}
}

func TestServiceUnchangedProposal(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	writeSkillFile(t, dir, "demo/a.txt", "same\n")
	res, err := svc.Request(context.Background(), "shop", "main", "", []Change{{Path: "demo/a.txt", Content: []byte("same\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed || res.Message != "Nothing to change: the files already have this content." {
		t.Fatalf("result = %+v", res)
	}
	if n := countRequests(t, st); n != 0 {
		t.Fatalf("%d rows were stored for an unchanged proposal", n)
	}
}

func TestServiceTooManyOpenProposals(t *testing.T) {
	svc, st, _ := newSvcFx(t)
	chs := make([]chan answer, 0, maxOpenProposals)
	for i := 0; i < maxOpenProposals; i++ {
		chs = append(chs, propose(svc, "shop", []Change{{Path: "demo/f" + string(rune('a'+i)) + ".txt", Content: []byte("x")}}, ""))
	}
	open := waitSkillRequests(t, st, maxOpenProposals)
	if len(open) != maxOpenProposals {
		t.Fatalf("%d open proposals, want %d", len(open), maxOpenProposals)
	}

	res, err := svc.Request(context.Background(), "shop", "main", "", []Change{{Path: "demo/one-more.txt", Content: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed || res.Message != "Too many open proposals for this sandbox; wait for the user to decide." {
		t.Fatalf("result = %+v", res)
	}
	for _, r := range open {
		if _, err := svc.Decide(context.Background(), r.ID, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, ch := range chs {
		if a := <-ch; a.err != nil || a.res.Allowed {
			t.Fatalf("requester got %+v", a)
		}
	}
	if n := countRequests(t, st); n != maxOpenProposals {
		t.Fatalf("%d rows, want %d", n, maxOpenProposals)
	}
}

func TestServiceTimeout(t *testing.T) {
	svc, st, _ := newSvcFx(t)
	svc.Timeout = 50 * time.Millisecond
	res, err := svc.Request(context.Background(), "shop", "main", "", []Change{{Path: "demo/a.txt", Content: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Allowed || res.Message != "No answer, request expired." {
		t.Fatalf("result = %+v", res)
	}
	rs, err := st.ListRequests(false, 10)
	if err != nil || len(rs) != 1 || rs[0].Status != "expired" {
		t.Fatalf("requests = %+v, %v", rs, err)
	}
	hasAudit(t, st, "skills.expired")
}

func TestServiceApplyFailure(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	writeSkillFile(t, dir, "demo/a.txt", "one\n")
	ch := propose(svc, "shop", []Change{{Path: "demo/a.txt", Content: []byte("two\n")}}, "")
	req := waitSkillRequest(t, st)

	// Someone changed the file between proposal and decision.
	writeSkillFile(t, dir, "demo/a.txt", "changed\n")
	status, err := svc.Decide(context.Background(), req.ID, true, "")
	if err == nil || !strings.Contains(err.Error(), "changed since the proposal") {
		t.Fatalf("Decide = %q, %v", status, err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	a := <-ch
	if a.err != nil || a.res.Allowed || !strings.Contains(a.res.Message, "applying failed") || !strings.Contains(a.res.Message, "Propose again") {
		t.Fatalf("requester got %+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); string(b) != "changed\n" {
		t.Fatalf("file changed despite the failure: %q", b)
	}
	if got, _ := st.GetRequest(req.ID); got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	hasAudit(t, st, "skills.apply_failed")
}

func TestServiceDecideRejectsWrongRequests(t *testing.T) {
	svc, st, _ := newSvcFx(t)

	nonSkill, err := st.AddRequest(store.Request{Sandbox: "shop", Kind: "ghtoken", Path: "o/r", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(context.Background(), nonSkill, true, ""); err == nil {
		t.Fatal("Decide must reject a non-skill request")
	}

	ch := propose(svc, "shop", []Change{{Path: "demo/a.txt", Content: []byte("x")}}, "")
	req := waitSkillRequest(t, st)
	if _, err := svc.Decide(context.Background(), req.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	<-ch
	if _, err := svc.Decide(context.Background(), req.ID, true, ""); err == nil {
		t.Fatal("Decide must reject a closed request")
	}

	orphan, err := st.AddRequest(store.Request{Sandbox: "shop", Kind: kindSkill, Path: "demo", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Decide(context.Background(), orphan, false, ""); err == nil {
		t.Fatal("Decide must reject a request without a waiter")
	}
}

func TestServiceConcurrentDecisions(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	ch1 := propose(svc, "shop", []Change{{Path: "demo/one.txt", Content: []byte("one\n")}}, "")
	ch2 := propose(svc, "shop", []Change{{Path: "demo/two.txt", Content: []byte("two\n")}}, "")
	open := waitSkillRequests(t, st, 2)

	var wg sync.WaitGroup
	errs := make([]error, len(open))
	for i, r := range open {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			status, err := svc.Decide(context.Background(), id, true, "")
			if err == nil && status != "allowed" {
				err = fmt.Errorf("status %q, want allowed", status)
			}
			errs[i] = err
		}(i, r.ID)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("decision %d: %v", i, err)
		}
	}
	for _, ch := range []chan answer{ch1, ch2} {
		if a := <-ch; a.err != nil || !a.res.Allowed {
			t.Fatalf("requester got %+v", a)
		}
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "demo", name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	if n := openSkillCount(t, st); n != 0 {
		t.Fatalf("%d proposals still open", n)
	}
}

func TestServiceParallelDecisionsSameRequest(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	ch := propose(svc, "shop", []Change{{Path: "demo/a.txt", Content: []byte("only once\n")}}, "")
	req := waitSkillRequest(t, st)

	type decision struct {
		status string
		err    error
	}
	results := make([]decision, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, err := svc.Decide(context.Background(), req.ID, true, "")
			results[i] = decision{status, err}
		}(i)
	}
	wg.Wait()

	allowed, failed := 0, 0
	for _, r := range results {
		switch {
		case r.err != nil:
			failed++
		case r.status == "allowed":
			allowed++
		}
	}
	if allowed != 1 || failed != 1 {
		t.Fatalf("decisions = %+v, want exactly one allowed and one error", results)
	}
	a := <-ch
	if a.err != nil || !a.res.Allowed {
		t.Fatalf("requester got %+v", a)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); err != nil || string(b) != "only once\n" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if got, _ := st.GetRequest(req.ID); got.Status != "allowed" {
		t.Fatalf("status = %q, want allowed", got.Status)
	}
	if n := countAudit(t, st, "skills.allowed"); n != 1 {
		t.Fatalf("%d allowed audit entries, want 1", n)
	}
}

func TestServiceTimeoutDuringDecision(t *testing.T) {
	svc, st, dir := newSvcFx(t)
	svc.Timeout = 150 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.beforeApply = func() {
		close(entered)
		<-release
	}
	ch := propose(svc, "shop", []Change{{Path: "demo/a.txt", Content: []byte("kept\n")}}, "")
	req := waitSkillRequest(t, st)

	type decision struct {
		status string
		err    error
	}
	done := make(chan decision, 1)
	go func() {
		status, err := svc.Decide(context.Background(), req.ID, true, "")
		done <- decision{status, err}
	}()
	<-entered
	// The timer fires while Decide holds the claim and waits for the result.
	time.Sleep(3 * svc.Timeout)
	close(release)

	d := <-done
	if d.err != nil || d.status != "allowed" {
		t.Fatalf("Decide = %+v", d)
	}
	a := <-ch
	if a.err != nil || !a.res.Allowed {
		t.Fatalf("requester got %+v, want the real result", a)
	}
	if got, _ := st.GetRequest(req.ID); got.Status != "allowed" {
		t.Fatalf("status = %q, want allowed", got.Status)
	}
	if n := countAudit(t, st, "skills.expired"); n != 0 {
		t.Fatalf("%d expired audit entries, want 0", n)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "demo", "a.txt")); err != nil || string(b) != "kept\n" {
		t.Fatalf("file = %q, %v", b, err)
	}
}
