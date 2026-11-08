package ci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
	svc := NewService(st, func(session string) (*github.Client, error) {
		if session != "shop" {
			return nil, github.ErrNoToken
		}
		gh := github.New(github.StaticToken("tok"))
		gh.Base = srv.URL
		return gh, nil
	})
	return &fx{svc: svc, logHits: &logHits, rerun: &rerun}
}

var repo = github.Repo{Owner: "o", Name: "r"}

func TestStatusAndFailedStep(t *testing.T) {
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
	if res, _ := f.svc.Status(ctx, "andere", repo, "agent/x"); res.State != "none" || !strings.Contains(res.Message, "request_github_token") {
		t.Fatalf("Session ohne Token: %+v", res)
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

func TestLogsWithoutApprovalRedacted(t *testing.T) {
	f := setup(t)
	res, _ := f.svc.Logs(context.Background(), "shop", "main", repo, 20)
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED || !strings.Contains(res.Log, "##[error]Process completed") || strings.Contains(res.Log, "hunter22xx") || strings.Contains(res.Log, "2026-11-08T") {
		t.Fatalf("%+v", res)
	}
	if atomic.LoadInt32(f.logHits) != 1 {
		t.Fatal("Logs nicht geladen")
	}
	if rs, _ := f.svc.S.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("CI darf keine Anfrage erzeugen")
	}
	if r, _ := f.svc.Logs(context.Background(), "andere", "main", repo, 20); r.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(r.Message, "request_github_token") {
		t.Fatalf("%+v", r)
	}
}

func TestRerunWithoutApproval(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if r, _ := f.svc.Rerun(ctx, "shop", "main", repo, 2); r.Verdict != pb.Verdict_VERDICT_ALLOWED || atomic.LoadInt32(f.rerun) != 1 {
		t.Fatalf("%+v", r)
	}
	if r, _ := f.svc.Rerun(ctx, "andere", "main", repo, 2); r.Verdict != pb.Verdict_VERDICT_DENIED || atomic.LoadInt32(f.rerun) != 1 {
		t.Fatalf("%+v", r)
	}
}
