package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghtoken"
	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/sandbox"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	"github.com/DevertNet/bergfried/daemon/internal/update"
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
	s := &Server{M: &sandbox.Manager{L: lima.Client{R: fakeLima{}}, S: st, Home: dir, Actor: "app"}, Ix: ix,
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
		t.Fatalf("removing the fixed blocklist must fail, code=%d", code)
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
		if code, _ := do(t, "POST", ts.URL+"/v1/sandboxes/create", string(b)); code != 400 {
			t.Fatalf("%q: code %d", n, code)
		}
	}
}

func TestJobLifecycleAndAuditSearch(t *testing.T) {
	s, ts := srv(t)
	code, body := do(t, "POST", ts.URL+"/v1/sandboxes/start", `{"name":"nix"}`)
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
	if j.State != "failed" || !strings.Contains(j.Error, "does not exist") {
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
		t.Fatal("formula injection in the CSV")
	}
}

func TestGitHubLink(t *testing.T) {
	_, ts := srv(t)
	code, body := do(t, "GET", ts.URL+"/v1/github/link?sandbox=shop", "")
	if code != 200 || !strings.Contains(body, "personal-access-tokens/new") || !strings.Contains(body, "name%3Dbergfried-shop") && !strings.Contains(body, "name=bergfried-shop") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := do(t, "GET", ts.URL+"/v1/github/link?sandbox=x%3By", ""); code != 400 {
		t.Fatal("invalid name")
	}
}

func TestGitHubDecideNeverEchoesToken(t *testing.T) {
	const tok = "github_pat_11ABCDEFG0abcdefghijklmnop_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Write([]byte(`{"default_branch":"main"}`))
	}))
	defer gh.Close()
	s, ts := srv(t)
	_ = s.M.S.AddSandbox(store.Sandbox{Name: "shop", VM: "bergfried-shop", TemplateVersion: 1})
	var installed string
	s.GH = ghtoken.NewService(s.M.S, ghtoken.Store{Home: t.TempDir()}, func(_ context.Context, _, t string) error { installed = t; return nil })
	s.GH.Client = func(token string) *github.Client {
		c := github.New(github.StaticToken(token))
		c.Base = gh.URL
		return c
	}
	done := make(chan bool, 1)
	go func() { ok, _, _ := s.GH.Request(context.Background(), "shop", "main", "o/r", "clone"); done <- ok }()
	var id int64
	for i := 0; i < 200 && id == 0; i++ {
		if rs, _ := s.M.S.ListRequests(true, 5); len(rs) > 0 {
			id = rs[0].ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == 0 {
		t.Fatal("no request")
	}
	_, list := do(t, "GET", ts.URL+"/v1/requests", "")
	if !strings.Contains(list, `"github"`) || !strings.Contains(list, `"repo":"o/r"`) || !strings.Contains(list, `"has_token":false`) {
		t.Fatalf("request lacks details: %s", list)
	}
	code, body := do(t, "POST", ts.URL+"/v1/github/decide", fmt.Sprintf(`{"id":%d,"allow":true,"token":"wrong-but-long-enough-0123456789"}`, id))
	if code != 409 || strings.Contains(body, "wrong-but") {
		t.Fatalf("%d %s", code, body)
	}
	code, body = do(t, "POST", ts.URL+"/v1/github/decide", fmt.Sprintf(`{"id":%d,"allow":true,"token":%q}`, id, tok))
	if code != 200 || strings.Contains(body, tok) {
		t.Fatalf("%d %s", code, body)
	}
	if !<-done || installed != tok {
		t.Fatal("token was not written into the VM")
	}
	_, sandboxes := do(t, "GET", ts.URL+"/v1/sandboxes", "")
	if !strings.Contains(sandboxes, `"has_github_token":true`) || strings.Contains(sandboxes, tok) {
		t.Fatalf("%s", sandboxes)
	}
	_, list = do(t, "GET", ts.URL+"/v1/requests?all=1", "")
	_, audit := do(t, "GET", ts.URL+"/v1/audit?q=ghtoken", "")
	if strings.Contains(list, tok) || strings.Contains(audit, tok) || !strings.Contains(audit, "ghtoken.allowed") {
		t.Fatalf("Token in Liste/Protokoll: %s %s", list, audit)
	}
}

func TestUpdateEndpoints(t *testing.T) {
	s, ts := srv(t)
	if code, _ := do(t, "GET", ts.URL+"/v1/update", ""); code != 404 {
		t.Fatalf("without update service: %d", code)
	}
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"v9.9.9","body":"notes","html_url":"u","assets":[]},{"tag_name":"v9.9.10-dev.4","prerelease":true,"assets":[]}]`))
	}))
	defer gh.Close()
	s.Upd = update.New("o/r", "0.0.0-dev", t.TempDir())
	s.Upd.API = gh.URL
	s.Upd.BundlePath = func() (string, error) { return "", fmt.Errorf("not running from an app bundle") }
	code, body := do(t, "GET", ts.URL+"/v1/update?refresh=1", "")
	if code != 200 || !strings.Contains(body, `"latest":"9.9.9"`) || !strings.Contains(body, `"updatable":false`) {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"channel":"stable"`) {
		t.Fatalf("channel missing: %s", body)
	}
	// switching the channel: validated, persisted, re-checked (the dev pre-release is newer than 9.9.9)
	if code, _ := do(t, "PUT", ts.URL+"/v1/update/channel", `{"channel":"beta"}`); code != 400 {
		t.Fatalf("unknown channel must be refused: %d", code)
	}
	code, body = do(t, "PUT", ts.URL+"/v1/update/channel", `{"channel":"dev"}`)
	if code != 200 || !strings.Contains(body, `"channel":"dev"`) || !strings.Contains(body, `"latest":"9.9.10-dev.4"`) {
		t.Fatalf("%d %s", code, body)
	}
	if _, st := do(t, "GET", ts.URL+"/v1/status", ""); strings.Contains(st, "update_available") {
		t.Fatalf("a release without assets must not be offered: %s", st)
	}
	// install outside an app bundle fails as a job with a clear message
	code, body = do(t, "POST", ts.URL+"/v1/update/install", "{}")
	if code != 202 {
		t.Fatalf("%d %s", code, body)
	}
	var j Job
	_ = json.Unmarshal([]byte(body), &j)
	for i := 0; i < 100; i++ {
		_, b := do(t, "GET", fmt.Sprintf("%s/v1/jobs/%d", ts.URL, j.ID), "")
		_ = json.Unmarshal([]byte(b), &j)
		if j.State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j.State != "failed" || !strings.Contains(j.Error, "app bundle") {
		t.Fatalf("%+v", j)
	}
}

func TestCreateRejectsUnknownRuntime(t *testing.T) {
	_, ts := srv(t)
	code, _ := do(t, "POST", ts.URL+"/v1/sandboxes/create", `{"name":"shop","runtime":"codex"}`)
	if code != 400 {
		t.Fatalf("unknown runtime must be rejected, got %d", code)
	}
}

func TestSandboxesReportAgentRuntimes(t *testing.T) {
	s, ts := srv(t)
	_ = s.M.S.AddSandbox(store.Sandbox{Name: "shop", VM: "bergfried-shop", TemplateVersion: 1, Created: time.Now(), OpenCodePort: 42123})
	_ = s.M.S.AddAgent(store.Agent{Sandbox: "shop", Name: "main", Worktree: "~/work/main", ComposeProject: "shop-main", Runtime: "claude", Created: time.Now()})
	_ = s.M.S.AddAgent(store.Agent{Sandbox: "shop", Name: "api", Worktree: "~/work/api", ComposeProject: "shop-api", Runtime: "opencode", Created: time.Now()})
	code, body := do(t, "GET", ts.URL+"/v1/sandboxes", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	for _, want := range []string{`"runtime":"claude"`, `"runtime":"opencode"`, `"opencode_port":42123`, `"opencode_logged_in"`, `"claude_installed"`, `"opencode_installed"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s missing in %s", want, body)
		}
	}
}
