package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

type fx struct {
	svc     *Service
	logHits *int32
	rerun   *int32
}

func setup(t *testing.T) *fx {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.AddPush(store.GitPush{Session: "shop", Agent: "main", Repo: "o/r", Branch: "agent/x", SHA: "abc"})
	var logHits, rerun int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"workflow_runs":[
		 {"id":2,"name":"CI","status":"completed","conclusion":"failure","head_branch":"agent/x","head_sha":"new","html_url":"u2","run_number":2},
		 {"id":3,"name":"Lint","status":"completed","conclusion":"success","head_branch":"agent/x","head_sha":"new","html_url":"u3","run_number":2},
		 {"id":1,"name":"CI","status":"completed","conclusion":"success","head_branch":"agent/x","head_sha":"old","html_url":"u1","run_number":1}]}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/2/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jobs":[{"id":20,"run_id":2,"name":"test","status":"completed","conclusion":"failure","steps":[{"name":"Checkout","number":1,"conclusion":"success"},{"name":"Run tests","number":2,"conclusion":"failure"}]}]}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/2", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":2,"name":"CI","status":"completed","conclusion":"failure","head_branch":"agent/x","head_sha":"new"}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/9", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":9,"name":"Fremd","status":"completed","conclusion":"failure","head_branch":"main","head_sha":"zzz"}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/jobs/20", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":20,"run_id":2,"name":"test","status":"completed","conclusion":"failure","head_branch":"agent/x","head_sha":"new"}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/jobs/99", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":99,"run_id":9,"name":"fremd","status":"completed","conclusion":"failure","head_branch":"main","head_sha":"zzz"}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/jobs/20/logs", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&logHits, 1)
		w.Write([]byte("2026-11-08T17:55:25.5Z zeile\n2026-11-08T17:55:26.5Z password=hunter22xx\n2026-11-08T17:55:27.5Z ##[error]Process completed with exit code 1.\n"))
	})
	mux.HandleFunc("POST /repos/o/r/actions/runs/2/rerun-failed-jobs", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&rerun, 1)
		w.WriteHeader(201)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	gh := github.New(github.StaticToken("tok"))
	gh.Base = srv.URL
	svc := NewService(st, gh)
	svc.Timeout = 3 * time.Second
	return &fx{svc: svc, logHits: &logHits, rerun: &rerun}
}

var repo = github.Repo{Owner: "o", Name: "r"}

func (f *fx) pendingID(t *testing.T, kind string) int64 {
	for i := 0; i < 200; i++ {
		rs, _ := f.svc.S.ListRequests(true, 10)
		for _, r := range rs {
			if r.Kind == kind {
				return r.ID
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("keine offene Anfrage " + kind)
	return 0
}

func TestStatusOnlyForPushedBranchAndFailedStep(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	res, _ := f.svc.Status(ctx, "shop", repo, "agent/x")
	if res.State != "failure" || len(res.Runs) != 2 {
		t.Fatalf("%+v", res)
	}
	var failed *pb.CIRun
	for _, r := range res.Runs {
		if r.Name == "CI" {
			failed = r
		}
		if r.Sha != "new" {
			t.Fatal("nur der neueste Commit zählt")
		}
	}
	if failed == nil || len(failed.Jobs) != 1 || failed.Jobs[0].FailedStep != "Run tests" {
		t.Fatalf("%+v", failed)
	}
	if res, _ := f.svc.Status(ctx, "shop", repo, "main"); res.State != "none" || !strings.Contains(res.Message, "nicht über request_git_push") {
		t.Fatalf("fremder Branch: %+v", res)
	}
	if res, _ := f.svc.Status(ctx, "andere", repo, "agent/x"); res.State != "none" {
		t.Fatal("andere Session hat keinen Zugriff")
	}
}

func TestSummarize(t *testing.T) {
	r := func(status, concl string) github.Run { return github.Run{Status: status, Conclusion: concl} }
	for name, c := range map[string]struct {
		in   []github.Run
		want string
	}{
		"leer":        {nil, "none"},
		"läuft":       {[]github.Run{r("completed", "success"), r("in_progress", "")}, "pending"},
		"grün":        {[]github.Run{r("completed", "success"), r("completed", "skipped")}, "success"},
		"rot":         {[]github.Run{r("completed", "success"), r("completed", "failure")}, "failure"},
		"abgebrochen": {[]github.Run{r("completed", "cancelled")}, "failure"},
	} {
		if got := Summarize(c.in); got != c.want {
			t.Errorf("%s: %s != %s", name, got, c.want)
		}
	}
}

func TestLogsNeedApprovalRedactAndSessionGrant(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ch := make(chan *pb.CILogsResult, 1)
	go func() { r, _ := f.svc.Logs(ctx, "shop", "main", repo, 20); ch <- r }()
	id := f.pendingID(t, "cilogs")
	if atomic.LoadInt32(f.logHits) != 0 {
		t.Fatal("vor der Bestätigung dürfen keine Logs geladen werden")
	}
	if _, err := f.svc.Decide(id, Decision{Allow: true, ForSession: true}); err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED || !strings.Contains(res.Log, "##[error]Process completed") || strings.Contains(res.Log, "hunter22xx") || strings.Contains(res.Log, "2026-11-08T") {
		t.Fatalf("%+v", res)
	}
	// Session-Freigabe: zweiter Aufruf ohne Rückfrage
	res2, _ := f.svc.Logs(ctx, "shop", "main", repo, 20)
	if res2.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("%+v", res2)
	}
	if rs, _ := f.svc.S.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("mit Session-Freigabe darf keine neue Anfrage entstehen")
	}
	// andere Session: weiterhin Rückfrage/Ablehnung wegen fehlendem Push
	if r, _ := f.svc.Logs(ctx, "andere", "main", repo, 20); r.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatalf("%+v", r)
	}
}

func TestLogsDeniedAndForeignJob(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ch := make(chan *pb.CILogsResult, 1)
	go func() { r, _ := f.svc.Logs(ctx, "shop", "main", repo, 20); ch <- r }()
	id := f.pendingID(t, "cilogs")
	if _, err := f.svc.Decide(id, Decision{Allow: false}); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; r.Verdict != pb.Verdict_VERDICT_DENIED || r.Log != "" || atomic.LoadInt32(f.logHits) != 0 {
		t.Fatalf("abgelehnt: keine Logs: %+v", r)
	}
	// Job von einem Branch, den die Session nicht gepusht hat (main)
	if r, _ := f.svc.Logs(ctx, "shop", "main", repo, 99); r.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(r.Message, "nicht zu einem Branch") {
		t.Fatalf("%+v", r)
	}
}

func TestRerunAlwaysAsksAndOnlyOwnBranches(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if r, _ := f.svc.Rerun(ctx, "shop", "main", repo, 9); r.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatalf("fremder Branch: %+v", r)
	}
	ch := make(chan *pb.CIRerunResult, 1)
	go func() { r, _ := f.svc.Rerun(ctx, "shop", "main", repo, 2); ch <- r }()
	id := f.pendingID(t, "cirerun")
	if atomic.LoadInt32(f.rerun) != 0 {
		t.Fatal("vor der Bestätigung nichts wiederholen")
	}
	if _, err := f.svc.Decide(id, Decision{Allow: true, ForSession: true}); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; r.Verdict != pb.Verdict_VERDICT_ALLOWED || atomic.LoadInt32(f.rerun) != 1 {
		t.Fatalf("%+v", r)
	}
	// auch mit Session-Freigabe: erneut fragen
	ch2 := make(chan *pb.CIRerunResult, 1)
	go func() { r, _ := f.svc.Rerun(ctx, "shop", "main", repo, 2); ch2 <- r }()
	id2 := f.pendingID(t, "cirerun")
	if _, err := f.svc.Decide(id2, Decision{Allow: false}); err != nil {
		t.Fatal(err)
	}
	if r := <-ch2; r.Verdict != pb.Verdict_VERDICT_DENIED || atomic.LoadInt32(f.rerun) != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestWatcherStateForAgent(t *testing.T) {
	f := setup(t)
	w := NewWatcher(f.svc.S, f.svc.GH)
	w.Poll(context.Background())
	st, ok := w.For("shop", "main")
	if !ok || st.State != "failure" || st.URL != "u2" || st.Branch != "agent/x" {
		t.Fatalf("%+v %v", st, ok)
	}
	if _, ok := w.For("shop", "anderer"); ok {
		t.Fatal("Agent ohne Push hat keinen CI-Zustand")
	}
}
