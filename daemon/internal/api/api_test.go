package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/session"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

type fakeLima struct{}

func (fakeLima) Run(context.Context, ...string) ([]byte, error) { return []byte(""), nil }

func srv(t *testing.T) (*Server, *httptest.Server) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "d"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	ix, _ := index.Build(home, index.DefaultBlocklist())
	s := &Server{M: &session.Manager{L: lima.Client{R: fakeLima{}}, S: st, Home: dir, Actor: "app"}, Ix: ix,
		BlocklistFile: filepath.Join(dir, "bl.json")}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func do(t *testing.T, method, url, body string) (int, string) {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		b.Write(buf[:n])
		if e != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"=1+1": "'=1+1", "+x": "'+x", "-x": "'-x", "@SUM": "'@SUM", "normal": "normal", "": ""} {
		if csvSafe(in) != want {
			t.Fatalf("%q -> %q", in, csvSafe(in))
		}
	}
}

func TestBlocklistMandatoryEntriesProtected(t *testing.T) {
	_, ts := srv(t)
	code, _ := do(t, "PUT", ts.URL+"/v1/blocklist", `{"paths":["Private"],"names":[".env"]}`)
	if code != 400 {
		t.Fatalf("Entfernen der festen Sperrliste muss scheitern, code=%d", code)
	}
	bl := index.DefaultBlocklist()
	bl.Paths = append(bl.Paths, "Private")
	b, _ := json.Marshal(bl)
	if code, body := do(t, "PUT", ts.URL+"/v1/blocklist", string(b)); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	_, body := do(t, "GET", ts.URL+"/v1/blocklist", "")
	if !strings.Contains(body, "Private") {
		t.Fatal("Eintrag fehlt")
	}
}

func TestInvalidNamesRejected(t *testing.T) {
	_, ts := srv(t)
	for _, n := range []string{"", "A", "a b", "a;rm", "../x"} {
		b, _ := json.Marshal(map[string]string{"name": n})
		if code, _ := do(t, "POST", ts.URL+"/v1/sessions/create", string(b)); code != 400 {
			t.Fatalf("%q: code %d", n, code)
		}
	}
}

func TestJobLifecycleAndAuditSearch(t *testing.T) {
	s, ts := srv(t)
	code, body := do(t, "POST", ts.URL+"/v1/sessions/start", `{"name":"nix"}`)
	if code != 202 {
		t.Fatal(code, body)
	}
	var j Job
	_ = json.Unmarshal([]byte(body), &j)
	for i := 0; i < 50; i++ {
		_, b := do(t, "GET", ts.URL+"/v1/jobs/1", "")
		_ = json.Unmarshal([]byte(b), &j)
		if j.State != "running" {
			break
		}
	}
	if j.State != "failed" || !strings.Contains(j.Error, "existiert nicht") {
		t.Fatalf("%+v", j)
	}
	_ = s.M.S.Audit("t", "request.created", "shop", "main", "id=1 =cmd|calc")
	_, out := do(t, "GET", ts.URL+"/v1/audit?q=shop", "")
	if !strings.Contains(out, "request.created") {
		t.Fatal(out)
	}
	_, csv := do(t, "GET", ts.URL+"/v1/audit.csv?q=shop", "")
	if !strings.Contains(csv, "'=cmd|calc") && !strings.Contains(csv, "=cmd|calc") {
		t.Fatal(csv)
	}
	if strings.Contains(csv, ",=cmd") {
		t.Fatal("Formel-Injektion im CSV")
	}
}

func TestCIFixOnlyWhenLoggedInAndIdle(t *testing.T) {
	s, ts := srv(t)
	_ = s.M.S.AddSession(store.Session{Name: "shop", VM: "bergfried-shop", TemplateVersion: 1})
	s.Mon = NewMonitor(s.M)
	post := func() (int, string) {
		return do(t, "POST", ts.URL+"/v1/agents/ci-fix", `{"session":"shop","agent":"main"}`)
	}
	if code, _ := post(); code != 409 {
		t.Fatalf("ohne Abfrage-Ergebnis: %d", code)
	}
	no, yes := false, true
	s.Mon.state["shop"] = &SessionState{LoggedIn: &no, Agents: map[string]AgentState{"main": {Window: true, Running: true, Idle: true}}}
	if code, body := post(); code != 409 || !strings.Contains(body, "nicht eingeloggt") {
		t.Fatalf("nicht eingeloggt muss abgelehnt werden: %d %s", code, body)
	}
	s.Mon.state["shop"] = &SessionState{LoggedIn: &yes, Agents: map[string]AgentState{"main": {Window: true, Running: true, Idle: false}}}
	if code, _ := post(); code != 409 {
		t.Fatalf("beschäftigter Agent: %d", code)
	}
	if code, _ := do(t, "POST", ts.URL+"/v1/agents/ci-fix", `{"session":"x;y","agent":"main"}`); code != 400 {
		t.Fatal("ungültiger Name")
	}
}

type fakeKeys struct {
	tok string
}

func (f *fakeKeys) Token() (string, error) {
	if f.tok == "" {
		return "", github.ErrNoToken
	}
	return f.tok, nil
}
func (f *fakeKeys) Set(t string) error { f.tok = t; return nil }
func (f *fakeKeys) Delete() error      { f.tok = ""; return nil }

func TestGitHubTokenLifecycle(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" && r.Header.Get("Authorization") == "Bearer gut-token" {
			w.Write([]byte(`{"login":"tester"}`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer gh.Close()
	s, ts := srv(t)
	keys := &fakeKeys{}
	s.Keys, s.GHBase, s.LoginFile = keys, gh.URL, filepath.Join(t.TempDir(), "login")
	if _, body := do(t, "GET", ts.URL+"/v1/github", ""); !strings.Contains(body, `"has_token":false`) {
		t.Fatal(body)
	}
	if code, body := do(t, "PUT", ts.URL+"/v1/github/token", `{"token":"falsch"}`); code != 400 || !strings.Contains(body, "ungültig") || keys.tok != "" {
		t.Fatalf("ungültiges Token darf nicht gespeichert werden: %d %s %q", code, body, keys.tok)
	}
	if code, body := do(t, "PUT", ts.URL+"/v1/github/token", `{"token":"gut-token"}`); code != 200 || !strings.Contains(body, "tester") || keys.tok != "gut-token" {
		t.Fatalf("%d %s", code, body)
	}
	if _, body := do(t, "GET", ts.URL+"/v1/github", ""); !strings.Contains(body, `"has_token":true`) || !strings.Contains(body, "tester") || strings.Contains(body, "gut-token") {
		t.Fatalf("Status darf das Token nie enthalten: %s", body)
	}
	if b, _ := os.ReadFile(s.LoginFile); strings.Contains(string(b), "gut-token") {
		t.Fatal("Token in der Login-Datei")
	}
	if _, out := do(t, "GET", ts.URL+"/v1/audit?q=github", ""); strings.Contains(out, "gut-token") {
		t.Fatal("Token im Protokoll")
	}
	do(t, "DELETE", ts.URL+"/v1/github/token", "")
	if keys.tok != "" {
		t.Fatal("Token muss entfernt sein")
	}
}
