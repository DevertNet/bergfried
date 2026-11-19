package gitaccess

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghapp"
	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// fakeGH stands in for api.github.com: installation lookup, token minting/revoking and a few repo calls.
type fakeGH struct {
	mu      sync.Mutex
	n       int
	minted  []mint
	revoked []string
	merged  []int
	prOpen  bool
	missing map[string]bool // repos without installation
	srv     *httptest.Server
}

type mint struct {
	Token string
	Repos []string
	Perms map[string]string
}

func newFake(t *testing.T) *fakeGH {
	f := &fakeGH{prOpen: true, missing: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGH) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/installation") && r.Method == "GET":
		parts := strings.Split(p, "/")
		if f.missing[parts[2]+"/"+parts[3]] {
			js(404, map[string]string{"message": "Not Found"})
			return
		}
		js(200, map[string]any{"id": 7})
	case p == "/app/installations/7/access_tokens" && r.Method == "POST":
		var b struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.n++
		tok := fmt.Sprintf("ghs_tok%d", f.n)
		f.minted = append(f.minted, mint{tok, b.Repositories, b.Permissions})
		js(201, map[string]any{"token": tok, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	case p == "/installation/token" && r.Method == "DELETE":
		f.revoked = append(f.revoked, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/pulls/5") && r.Method == "GET":
		st := "open"
		if !f.prOpen {
			st = "closed"
		}
		js(200, map[string]any{"number": 5, "title": "Add feature", "state": st, "html_url": "https://github.com/o/r/pull/5",
			"head": map[string]string{"ref": "feature"}, "base": map[string]string{"ref": "main"}})
	case strings.HasSuffix(p, "/pulls/5/merge") && r.Method == "PUT":
		f.merged = append(f.merged, 5)
		js(200, map[string]any{"merged": true, "message": "Pull Request successfully merged"})
	default:
		js(500, map[string]string{"message": "unexpected " + r.Method + " " + p})
	}
}

func (f *fakeGH) snapshot() (m []mint, rev []string, merged []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mint(nil), f.minted...), append([]string(nil), f.revoked...), append([]int(nil), f.merged...)
}

type env struct {
	t   *testing.T
	s   *Service
	st  *store.Store
	f   *fakeGH
	now time.Time
	mu  sync.Mutex
}

func (e *env) clock() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemB := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	as := ghapp.Store{Dir: t.TempDir() + "/ga"}
	if err := as.Save(ghapp.Config{AppID: 1, Slug: "bf"}, pemB); err != nil {
		t.Fatal(err)
	}
	f := newFake(t)
	app := ghapp.NewClient(as)
	app.Base = f.srv.URL
	e := &env{t: t, st: st, f: f, now: time.Now()}
	app.Now = e.clock
	s := NewService(st, app)
	s.Now = e.clock
	s.Client = func(tok string) *github.Client {
		c := github.New(github.StaticToken(tok))
		c.Base = f.srv.URL
		return c
	}
	s.Timeout = 5 * time.Second
	e.s = s
	return e
}

// pending waits for an open request of the given kind and returns it.
func (e *env) pending(kind string) store.Request {
	e.t.Helper()
	for i := 0; i < 200; i++ {
		rs, _ := e.st.ListRequests(true, 10)
		for _, r := range rs {
			if r.Kind == kind {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("no pending", kind)
	return store.Request{}
}

func (e *env) grant(sandbox, repo string, write bool, allow bool, minutes int, userWrite bool) GrantResult {
	e.t.Helper()
	return e.grantMany(sandbox, []string{repo}, write, allow, minutes, userWrite)
}

func (e *env) grantMany(sandbox string, repos []string, write bool, allow bool, minutes int, userWrite bool) GrantResult {
	e.t.Helper()
	ch := make(chan GrantResult, 1)
	go func() {
		r, err := e.s.RequestGrant(context.Background(), sandbox, "work/main", repos, write, 10, "push my branch")
		if err != nil {
			e.t.Error(err)
		}
		ch <- r
	}()
	r := e.pending("gitgrant")
	if err := e.s.Decide(context.Background(), r.ID, allow, minutes, userWrite, ""); err != nil {
		e.t.Fatal(err)
	}
	return <-ch
}

func TestGrantCredentialAndRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r.git"); err != ErrNoGrant {
		t.Fatal("no grant yet:", err)
	}
	g := e.grant("shop", "o/r", true, true, 10, true)
	if !g.Allowed || g.TaskID == 0 {
		t.Fatalf("%+v", g)
	}
	u, tok, err := e.s.Credential(ctx, "shop", "github.com", "o/r.git")
	if err != nil || u != "x-access-token" || tok != "ghs_tok1" {
		t.Fatal(u, tok, err)
	}
	m, _, _ := e.f.snapshot()
	if len(m) != 1 || len(m[0].Repos) != 1 || m[0].Repos[0] != "r" || m[0].Perms["contents"] != "write" || len(m[0].Perms) != 1 {
		t.Fatalf("token must be narrowed to the repo and contents: %+v", m)
	}
	// the same token is handed out again, nothing new is minted
	if _, tok2, _ := e.s.Credential(ctx, "shop", "github.com", "o/r"); tok2 != tok {
		t.Fatal("token must be reused")
	}
	// other repo, other host, other sandbox: nothing
	for _, c := range [][3]string{{"shop", "github.com", "o/other"}, {"shop", "gitlab.com", "o/r"}, {"blog", "github.com", "o/r"}} {
		if _, _, err := e.s.Credential(ctx, c[0], c[1], c[2]); err != ErrNoGrant {
			t.Fatalf("%v must get no credentials: %v", c, err)
		}
	}
	// a foreign sandbox cannot release it and learns nothing
	rel, _ := e.s.Release(ctx, "blog", g.TaskID)
	if rel.Released || !strings.Contains(rel.Message, "No git task") {
		t.Fatalf("%+v", rel)
	}
	rel, _ = e.s.Release(ctx, "shop", g.TaskID)
	if !rel.Released {
		t.Fatalf("%+v", rel)
	}
	if _, rev, _ := e.f.snapshot(); len(rev) != 1 || rev[0] != "ghs_tok1" {
		t.Fatalf("the token must be revoked on GitHub right away: %v", rev)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != ErrNoGrant {
		t.Fatal("released grant must give nothing:", err)
	}
	if rel, _ = e.s.Release(ctx, "shop", g.TaskID); rel.Released {
		t.Fatal("second release must be a no-op")
	}
}

func TestUserCanLowerButNotRaise(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.grant("shop", "o/r", true, true, 5, false) // asked write, user allows read only
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := e.f.snapshot(); m[0].Perms["contents"] != "read" {
		t.Fatalf("%+v", m)
	}
	e2 := newEnv(t)
	e2.grant("shop", "o/r", false, true, 5, true) // asked read, user ticks write: stays read
	_, _, _ = e2.s.Credential(ctx, "shop", "github.com", "o/r")
	if m, _, _ := e2.f.snapshot(); m[0].Perms["contents"] != "read" {
		t.Fatalf("the user cannot raise the level above the request: %+v", m)
	}
}

func TestTwoGrantsShareOneTokenAndShrinkAfterRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.grant("shop", "o/a", true, true, 30, true)
	if _, tok, _ := e.s.Credential(ctx, "shop", "github.com", "o/a"); tok != "ghs_tok1" {
		t.Fatal(tok)
	}
	b := e.grant("shop", "o/b", false, true, 30, false)
	_, tok, err := e.s.Credential(ctx, "shop", "github.com", "o/b")
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := e.f.snapshot()
	if len(m) != 2 || len(m[1].Repos) != 2 || m[1].Perms["contents"] != "write" {
		t.Fatalf("the union of both grants must be one token: %+v", m)
	}
	// the old token is still valid during the grace time (a running push of the other grant must not break)
	_, rev, _ := e.f.snapshot()
	if len(rev) != 0 {
		t.Fatal("not revoked yet:", rev)
	}
	e.s.Sweep(ctx)
	if _, rev, _ = e.f.snapshot(); len(rev) != 0 {
		t.Fatal("grace time not respected:", rev)
	}
	e.advance(Grace + time.Second)
	e.s.Sweep(ctx)
	if _, rev, _ = e.f.snapshot(); len(rev) != 1 || rev[0] != "ghs_tok1" {
		t.Fatalf("the replaced token must be revoked after the grace time: %v", rev)
	}
	// release the write grant: the remaining one is read only and gets a narrower token
	if r, _ := e.s.Release(ctx, "shop", a.TaskID); !r.Released {
		t.Fatal("release a")
	}
	_, tok3, err := e.s.Credential(ctx, "shop", "github.com", "o/b")
	if err != nil || tok3 == tok {
		t.Fatal("a narrower token must be minted:", tok3, err)
	}
	m, _, _ = e.f.snapshot()
	last := m[len(m)-1]
	if len(last.Repos) != 1 || last.Repos[0] != "b" || last.Perms["contents"] != "read" {
		t.Fatalf("%+v", last)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/a"); err != ErrNoGrant {
		t.Fatal("released repo must give nothing")
	}
	_ = b
}

func TestExpiryRevokesTokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.grant("shop", "o/r", true, true, 5, true)
	_, _, _ = e.s.Credential(ctx, "shop", "github.com", "o/r")
	e.advance(6 * time.Minute)
	e.s.Sweep(ctx)
	if _, rev, _ := e.f.snapshot(); len(rev) != 1 {
		t.Fatalf("an expired grant must revoke its token: %v", rev)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != ErrNoGrant {
		t.Fatal("expired grant must give nothing")
	}
	// time never exceeds one hour, however much the user or the assistant asks
	g := e.grant("shop", "o/r", true, true, 600, true)
	gg, _ := e.st.GetGitGrant(g.TaskID)
	if gg.Expires.Sub(e.clock()) > time.Hour+time.Second {
		t.Fatal("grants are limited to 60 minutes:", gg.Expires)
	}
}

func TestEndSandboxRevokesEverything(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.grant("shop", "o/r", true, true, 30, true)
	_, _, _ = e.s.Credential(ctx, "shop", "github.com", "o/r")
	e.s.EndSandbox(ctx, "shop")
	if _, rev, _ := e.f.snapshot(); len(rev) != 1 {
		t.Fatalf("%v", rev)
	}
	if gs, _ := e.st.ListGitGrants("shop", true, e.clock()); len(gs) != 0 {
		t.Fatal("no grant may stay open")
	}
}

func TestDeniedAndNotInstalled(t *testing.T) {
	e := newEnv(t)
	g := e.grant("shop", "o/r", true, false, 0, false)
	if g.Allowed || !strings.Contains(g.Message, "declined") {
		t.Fatalf("%+v", g)
	}
	e.f.missing["o/gone"] = true
	r, err := e.s.RequestGrant(context.Background(), "shop", "", []string{"o/gone"}, true, 5, "x")
	if err != nil || r.Allowed || !strings.Contains(r.Message, "not installed") {
		t.Fatalf("%+v %v", r, err)
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("no card may be created for a repository the app cannot reach")
	}
	if r, _ := e.s.RequestGrant(context.Background(), "shop", "", []string{"not a repo!"}, true, 5, "x"); r.Allowed || r.Message == "" {
		t.Fatalf("%+v", r)
	}
}

func TestRequestTimesOut(t *testing.T) {
	e := newEnv(t)
	e.s.Timeout = 50 * time.Millisecond
	r, err := e.s.RequestGrant(context.Background(), "shop", "", []string{"o/r"}, true, 5, "x")
	if err != nil || !r.Expired {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestTokensNeverReachTheAuditLog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.grant("shop", "o/r", true, true, 5, true)
	_, tok, _ := e.s.Credential(ctx, "shop", "github.com", "o/r")
	_, _ = e.s.Release(ctx, "shop", 1)
	es, _ := e.st.AuditList(100)
	if len(es) == 0 {
		t.Fatal("no audit entries")
	}
	for _, a := range es {
		if strings.Contains(a.Detail, tok) || strings.Contains(a.Detail, "ghs_") {
			t.Fatalf("token in audit log: %+v", a)
		}
	}
}

func TestMergeNeedsApprovalEveryTimeAndShowsGitHubTitle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	run := func(allow bool) ActionRes {
		ch := make(chan ActionRes, 1)
		go func() {
			r, err := e.s.Action(ctx, "shop", "work/main", ActionReq{Action: ActPRMerge, Repo: "o/r", Number: 5, Method: "squash", Reason: "done"})
			if err != nil {
				t.Error(err)
			}
			ch <- r
		}()
		r := e.pending("ghaction")
		var d Detail
		_ = json.Unmarshal([]byte(r.Extra), &d)
		if len(d.Summary) == 0 || !strings.Contains(d.Summary[0], "Add feature") || !strings.Contains(d.Summary[1], "feature → main") {
			t.Fatalf("the card must show the PR as GitHub knows it: %+v", d)
		}
		if err := e.s.Decide(ctx, r.ID, allow, 0, false, "not yet"); err != nil {
			t.Fatal(err)
		}
		return <-ch
	}
	if r := run(false); r.OK || !strings.Contains(r.Message, "declined") || !strings.Contains(r.Message, "not yet") {
		t.Fatalf("%+v", r)
	}
	if _, _, merged := e.f.snapshot(); len(merged) != 0 {
		t.Fatal("nothing may be merged without approval")
	}
	if r := run(true); !r.OK {
		t.Fatalf("%+v", r)
	}
	m, rev, merged := e.f.snapshot()
	if len(merged) != 1 {
		t.Fatal("merge must happen once")
	}
	// every call mints its own narrow token and revokes it right after use
	for _, x := range m {
		if len(x.Repos) != 1 || x.Repos[0] != "r" {
			t.Fatalf("%+v", x)
		}
	}
	if len(rev) != len(m) {
		t.Fatalf("all per-action tokens must be revoked: minted %d revoked %d", len(m), len(rev))
	}
	// a closed PR is refused without bothering the user
	e.f.mu.Lock()
	e.f.prOpen = false
	e.f.mu.Unlock()
	if r, _ := e.s.Action(ctx, "shop", "", ActionReq{Action: ActPRMerge, Repo: "o/r", Number: 5}); r.OK || !strings.Contains(r.Message, "not open") {
		t.Fatalf("%+v", r)
	}
}

func TestReadActionsNeedAGrant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r, _ := e.s.Action(ctx, "shop", "", ActionReq{Action: ActPRView, Repo: "o/r", Number: 5})
	if r.OK || !strings.Contains(r.Message, "request_git_access") {
		t.Fatalf("%+v", r)
	}
	e.grant("shop", "o/r", false, true, 5, false)
	r, _ = e.s.Action(ctx, "shop", "", ActionReq{Action: ActPRView, Repo: "o/r", Number: 5})
	if !r.OK || !strings.Contains(r.DataJSON, "Add feature") {
		t.Fatalf("%+v", r)
	}
	if r, _ := e.s.Action(ctx, "shop", "", ActionReq{Action: "delete_repo", Repo: "o/r"}); r.OK {
		t.Fatal("unknown actions must be refused")
	}
	_ = io.Discard
}

func TestCleanStripsControlCharacters(t *testing.T) {
	if got := clean("a\nb\x1b[31mc\x00", 50); strings.ContainsAny(got, "\n\x1b\x00") {
		t.Fatalf("%q", got)
	}
	if got := clean(strings.Repeat("x", 600), 300); len([]rune(got)) != 301 {
		t.Fatal(len(got))
	}
}

// Several repositories in one request: one card, one decision, one grant (task_id) per repository, one token for all of them.
func TestSeveralReposOneRequestOneDecision(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g := e.grantMany("shop", []string{"o/a", "https://github.com/o/b", "o/c", "O/A"}, true, true, 30, true) // duplicate (case-insensitive) is dropped
	if !g.Allowed || len(g.TaskIDs) != 3 || g.TaskID != g.TaskIDs[0] {
		t.Fatalf("%+v", g)
	}
	for _, id := range g.TaskIDs {
		if !strings.Contains(g.Message, fmt.Sprint(id)) {
			t.Fatalf("message must name every task id: %s", g.Message)
		}
	}
	if rs, _ := e.st.ListRequests(false, 10); len(rs) != 1 {
		t.Fatalf("one request for all repositories, got %d", len(rs))
	}
	for _, r := range []string{"o/a", "o/b", "o/c"} {
		if _, _, err := e.s.Credential(ctx, "shop", "github.com", r+".git"); err != nil {
			t.Fatal(r, err)
		}
	}
	if m, _, _ := e.f.snapshot(); len(m) != 1 || len(m[0].Repos) != 3 || m[0].Perms["contents"] != "write" {
		t.Fatalf("all repositories must share one narrowed token: %+v", m)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/d"); err != ErrNoGrant {
		t.Fatal("a repository that was not requested gets nothing")
	}
	// release all with the ids of the request; a foreign sandbox cannot
	if r, _ := e.s.ReleaseMany(ctx, "blog", g.TaskIDs); r.Released {
		t.Fatalf("%+v", r)
	}
	r, err := e.s.ReleaseMany(ctx, "shop", g.TaskIDs)
	if err != nil || !r.Released {
		t.Fatalf("%+v %v", r, err)
	}
	if gs, _ := e.s.activeGrants("shop"); len(gs) != 0 {
		t.Fatalf("all grants must be ended: %+v", gs)
	}
	if _, rev, _ := e.f.snapshot(); len(rev) != 1 {
		t.Fatalf("the token must be revoked: %v", rev)
	}
}

func TestSeveralReposUserLowersLevelForAll(t *testing.T) {
	e := newEnv(t)
	g := e.grantMany("shop", []string{"o/a", "o/b"}, true, true, 5, false)
	if !g.Allowed {
		t.Fatal(g)
	}
	gs, _ := e.s.activeGrants("shop")
	if len(gs) != 2 || gs[0].Write || gs[1].Write {
		t.Fatalf("read only for every repository: %+v", gs)
	}
}

func TestSeveralReposRefusedBeforeAskingTheUser(t *testing.T) {
	e := newEnv(t)
	e.f.missing["o/b"] = true
	r, err := e.s.RequestGrant(context.Background(), "shop", "main", []string{"o/a", "o/b"}, false, 5, "x")
	if err != nil || r.Allowed || !strings.Contains(r.Message, "o/b") || !strings.Contains(r.Message, "not installed") {
		t.Fatalf("%+v %v", r, err)
	}
	if rs, _ := e.st.ListRequests(false, 10); len(rs) != 0 {
		t.Fatal("the user must not be asked for a request that cannot be granted")
	}
	var many []string
	for i := 0; i <= MaxRepos; i++ {
		many = append(many, fmt.Sprintf("o/r%d", i))
	}
	if r, _ = e.s.RequestGrant(context.Background(), "shop", "main", many, false, 5, "x"); r.Allowed || !strings.Contains(r.Message, "at most") {
		t.Fatalf("%+v", r)
	}
	if r, _ = e.s.RequestGrant(context.Background(), "shop", "main", []string{" ", ""}, false, 5, "x"); r.Allowed || r.Message == "" {
		t.Fatalf("%+v", r)
	}
}
