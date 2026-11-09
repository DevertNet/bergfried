// fakegithub: local GitHub stand-in for development and end-to-end tests (not for production).
// Offers on one port: the needed REST endpoints (repo, pull request, Actions runs/jobs/logs, rerun, /user) and a real
// git smart HTTP server (git http-backend) with basic auth (password = token). CI state via PUT /_fake/state, log via GET /_fake/log.
//
//	fakegithub -addr 127.0.0.1:8099 -root /path/to/repos -token secret
//	A bare repo must live under <root>/<owner>/<repo>.git (git init --bare; git config http.receivepack true).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type state struct {
	Runs []map[string]any            `json:"runs"`
	Jobs map[string][]map[string]any `json:"jobs"` // runID -> Jobs
	Logs map[string]string           `json:"logs"` // jobID -> Text
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "Adresse")
	root := flag.String("root", "", "root directory of the bare repos (<owner>/<repo>.git)")
	token := flag.String("token", "", "expected token/password")
	flag.Parse()
	if *root == "" || *token == "" {
		log.Fatal("-root and -token are required")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		log.Fatal(err)
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")

	var mu sync.Mutex
	st := state{Jobs: map[string][]map[string]any{}, Logs: map[string]string{}}
	var events []string
	record := func(f string, a ...any) { mu.Lock(); events = append(events, fmt.Sprintf(f, a...)); mu.Unlock() }

	mux := http.NewServeMux()
	js := func(w http.ResponseWriter, v any, code int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	authed := func(r *http.Request) bool {
		if h := r.Header.Get("Authorization"); h == "Bearer "+*token {
			return true
		}
		_, p, ok := r.BasicAuth()
		return ok && p == *token
	}
	api := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !authed(r) {
				js(w, map[string]string{"message": "Bad credentials"}, 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /user", api(func(w http.ResponseWriter, r *http.Request) { js(w, map[string]string{"login": "tester"}, 200) }))
	mux.HandleFunc("GET /repos/{o}/{r}", api(func(w http.ResponseWriter, r *http.Request) { js(w, map[string]string{"default_branch": "main"}, 200) }))
	mux.HandleFunc("POST /repos/{o}/{r}/pulls", api(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		record("PR %v -> %v: %v", b["head"], b["base"], b["title"])
		js(w, map[string]any{"html_url": "https://github.com/" + r.PathValue("o") + "/" + r.PathValue("r") + "/pull/1", "number": 1}, 201)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs", api(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var runs []map[string]any
		for _, run := range st.Runs {
			if b := r.URL.Query().Get("branch"); b == "" || run["head_branch"] == b {
				runs = append(runs, run)
			}
		}
		js(w, map[string]any{"total_count": len(runs), "workflow_runs": runs}, 200)
	}))
	findRun := func(id string) map[string]any {
		for _, run := range st.Runs {
			if fmt.Sprint(int64(run["id"].(float64))) == id {
				return run
			}
		}
		return nil
	}
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs/{id}", api(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if run := findRun(r.PathValue("id")); run != nil {
			js(w, run, 200)
			return
		}
		js(w, map[string]string{"message": "Not Found"}, 404)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/runs/{id}/jobs", api(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		js(w, map[string]any{"jobs": st.Jobs[r.PathValue("id")]}, 200)
	}))
	mux.HandleFunc("POST /repos/{o}/{r}/actions/runs/{id}/rerun-failed-jobs", api(func(w http.ResponseWriter, r *http.Request) {
		record("RERUN run=%s", r.PathValue("id"))
		js(w, map[string]string{}, 201)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/jobs/{id}", api(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for _, jobs := range st.Jobs {
			for _, j := range jobs {
				if fmt.Sprint(int64(j["id"].(float64))) == r.PathValue("id") {
					js(w, j, 200)
					return
				}
			}
		}
		js(w, map[string]string{"message": "Not Found"}, 404)
	}))
	mux.HandleFunc("GET /repos/{o}/{r}/actions/jobs/{id}/logs", api(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/_blob/"+r.PathValue("id"), http.StatusFound)
	}))
	mux.HandleFunc("GET /_blob/{id}", func(w http.ResponseWriter, r *http.Request) {
		// Note: with real GitHub the redirect points to another domain (Go then drops the Authorization header);
		// here it is the same host, so the header comes along – irrelevant for the stand-in.
		mu.Lock()
		text := st.Logs[r.PathValue("id")]
		mu.Unlock()
		io.WriteString(w, text)
	})
	mux.HandleFunc("PUT /_fake/state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		n := state{}
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			js(w, map[string]string{"message": err.Error()}, 400)
			return
		}
		st = n
		js(w, map[string]string{"ok": "1"}, 200)
	})
	mux.HandleFunc("GET /_fake/log", func(w http.ResponseWriter, r *http.Request) { mu.Lock(); defer mu.Unlock(); js(w, events, 200) })
	gitH := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1", "HOME=" + *root}}
	mux.HandleFunc("/{o}/{rp}/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.PathValue("rp"), ".git") {
			http.NotFound(w, r)
			return
		}
		if _, p, ok := r.BasicAuth(); !ok || p != *token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(401)
			return
		}
		gitH.ServeHTTP(w, r)
	})
	log.Printf("fakegithub auf http://%s (Repos: %s)", *addr, *root)
	_ = strconv.Itoa
	log.Fatal(http.ListenAndServe(*addr, mux))
}
