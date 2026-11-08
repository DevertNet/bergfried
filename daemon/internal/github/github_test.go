package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseRemote(t *testing.T) {
	ok := map[string]string{
		"https://github.com/DevertNet/bergfried.git": "DevertNet/bergfried",
		"https://github.com/DevertNet/bergfried":     "DevertNet/bergfried",
		"git@github.com:DevertNet/bergfried.git":     "DevertNet/bergfried",
		"ssh://git@github.com/o/r.git":               "o/r",
		"https://github.com/o/r/":                    "o/r",
	}
	for in, want := range ok {
		r, err := ParseRemote(in)
		if err != nil || r.String() != want {
			t.Fatalf("%s: %v %v", in, r, err)
		}
	}
	for _, bad := range []string{"", "https://evil.com/o/r.git", "https://user:tok@github.com/o/r.git", "https://github.com/o", "https://github.com/o/r/x",
		"https://github.com/../r", "https://github.com/o/..", "git@github.com:o/r;rm", "file:///etc/passwd", "https://github.com.evil.com/o/r", "https://github.com/o/r name"} {
		if _, err := ParseRemote(bad); err == nil {
			t.Fatalf("%q muss abgelehnt werden", bad)
		}
	}
}

func fake(t *testing.T) (*Client, *http.ServeMux, *[]string) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	var seen []string
	wrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		mux.ServeHTTP(w, r)
	})
	ts.Config.Handler = wrap
	c := New(StaticToken("tok123"))
	c.Base = ts.URL
	return c, mux, &seen
}

func TestClientEndpointsAndAuth(t *testing.T) {
	c, mux, seen := fake(t)
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"default_branch":"trunk"}`)) })
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("branch") != "agent/x" {
			t.Errorf("branch=%q", r.URL.Query().Get("branch"))
		}
		w.Write([]byte(`{"workflow_runs":[{"id":7,"name":"CI","status":"completed","conclusion":"failure","head_branch":"agent/x","head_sha":"abc","html_url":"u","run_number":3,"run_attempt":1,"event":"push"}]}`))
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/7/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jobs":[{"id":9,"run_id":7,"name":"test","status":"completed","conclusion":"failure","steps":[{"name":"Run tests","number":3,"status":"completed","conclusion":"failure"}]}]}`))
	})
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["head"] != "agent/x" || b["base"] != "trunk" || b["title"] != "T" {
			t.Errorf("%v", b)
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"html_url":"https://github.com/o/r/pull/5","number":5}`))
	})
	mux.HandleFunc("POST /repos/o/r/actions/runs/7/rerun-failed-jobs", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	mux.HandleFunc("GET /repos/o/r/actions/jobs/9/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/blob/log", http.StatusFound)
	})
	mux.HandleFunc("GET /blob/log", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("zeile1\n##[error]kaputt\n")) })
	ctx := context.Background()
	repo := Repo{"o", "r"}
	if b, err := c.DefaultBranch(ctx, repo); err != nil || b != "trunk" {
		t.Fatal(b, err)
	}
	runs, err := c.ListRuns(ctx, repo, "agent/x")
	if err != nil || len(runs) != 1 || runs[0].Conclusion != "failure" || !runs[0].Done() {
		t.Fatalf("%+v %v", runs, err)
	}
	jobs, _ := c.ListJobs(ctx, repo, 7)
	if len(jobs) != 1 || jobs[0].Steps[0].Name != "Run tests" {
		t.Fatalf("%+v", jobs)
	}
	pr, err := c.CreatePR(ctx, repo, "agent/x", "trunk", "T", "B", false)
	if err != nil || pr.Number != 5 {
		t.Fatal(pr, err)
	}
	if err := c.RerunFailed(ctx, repo, 7); err != nil {
		t.Fatal(err)
	}
	if l, err := c.JobLogs(ctx, repo, 9, 1<<20); err != nil || !strings.Contains(l, "##[error]kaputt") {
		t.Fatal(l, err)
	}
	for _, s := range *seen {
		if !strings.Contains(s, "auth=Bearer tok123") && !strings.HasPrefix(s, "GET /blob/") {
			t.Fatalf("Token fehlt: %s", s)
		}
	}
}

func TestErrorsAndMissingToken(t *testing.T) {
	c, mux, _ := fake(t)
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := c.DefaultBranch(context.Background(), Repo{"o", "r"})
	var ae *APIError
	if !asAPI(err, &ae) || ae.Status != 404 {
		t.Fatalf("%v", err)
	}
	c.Tokens = StaticToken("")
	if _, err := c.DefaultBranch(context.Background(), Repo{"o", "r"}); err != ErrNoToken {
		t.Fatalf("%v", err)
	}
}

func asAPI(err error, out **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*out = e
	}
	return ok
}

func TestRedactAndExcerpt(t *testing.T) {
	in := "Authorization: Bearer abcdef123456\npassword=hunter22xx\nghp_" + strings.Repeat("a", 30) + "\nAKIAABCDEFGHIJKLMNOP\nhttps://user:pw123456@host/x\n-----BEGIN PRIVATE KEY-----\nMIIE\n-----END PRIVATE KEY-----\nnormaler Text"
	out := Redact(in)
	for _, leak := range []string{"abcdef123456", "hunter22xx", strings.Repeat("a", 30), "AKIAABCDEFGHIJKLMNOP", "pw123456", "MIIE"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q nicht geschwärzt:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "normaler Text") {
		t.Fatal("normaler Text muss bleiben")
	}
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, "2026-11-08T17:55:25.5Z zeile "+strings.Repeat("x", 3))
	}
	lines[100] = "2026-11-08T17:55:25.5Z ##[error]Process completed with exit code 1."
	ex := FailureExcerpt("\ufeff"+strings.Join(lines, "\r\n"), 4000)
	if !strings.Contains(ex, "##[error]Process completed") || strings.Contains(ex, "2026-11-08T") || strings.Count(ex, "\n") > 120 {
		t.Fatalf("Auszug unpassend (%d Zeilen)", strings.Count(ex, "\n"))
	}
	if len(FailureExcerpt(strings.Repeat("y", 100000)+"\n", 2000)) > 2100 {
		t.Fatal("Größenlimit")
	}
}
