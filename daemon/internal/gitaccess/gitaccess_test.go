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
	created []string // heads of opened pull requests
	prOpen  bool
	missing map[string]bool // repos without installation
	noWF    bool            // the app does not have the workflows permission: minting it is refused
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
		if _, wf := b.Permissions["workflows"]; wf && f.noWF {
			js(422, map[string]string{"message": "The permissions requested are not granted to this installation."})
			return
		}
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
	case strings.HasSuffix(p, "/pulls") && r.Method == "POST":
		var b struct{ Head string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.created = append(f.created, b.Head)
		js(201, map[string]any{"number": 6, "title": "New", "state": "open", "html_url": "https://github.com/o/r/pull/6",
			"head": map[string]string{"ref": b.Head}, "base": map[string]string{"ref": "main"}})
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
		r, err := e.s.RequestGrant(context.Background(), sandbox, "work/main", repos, write, false, 10, "push my branch")
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
	r, err := e.s.RequestGrant(context.Background(), "shop", "", []string{"o/gone"}, true, false, 5, "x")
	if err != nil || r.Allowed || !strings.Contains(r.Message, "not installed") {
		t.Fatalf("%+v %v", r, err)
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("no card may be created for a repository the app cannot reach")
	}
	if r, _ := e.s.RequestGrant(context.Background(), "shop", "", []string{"not a repo!"}, true, false, 5, "x"); r.Allowed || r.Message == "" {
		t.Fatalf("%+v", r)
	}
}

func TestRequestTimesOut(t *testing.T) {
	e := newEnv(t)
	e.s.Timeout = 50 * time.Millisecond
	r, err := e.s.RequestGrant(context.Background(), "shop", "", []string{"o/r"}, true, false, 5, "x")
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
	r, err := e.s.RequestGrant(context.Background(), "shop", "main", []string{"o/a", "o/b"}, false, false, 5, "x")
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
	if r, _ = e.s.RequestGrant(context.Background(), "shop", "main", many, false, false, 5, "x"); r.Allowed || !strings.Contains(r.Message, "at most") {
		t.Fatalf("%+v", r)
	}
	if r, _ = e.s.RequestGrant(context.Background(), "shop", "main", []string{" ", ""}, false, false, 5, "x"); r.Allowed || r.Message == "" {
		t.Fatalf("%+v", r)
	}
}

// ---- full repo access (windows) ----

func (e *env) action(sandbox string, a ActionReq) ActionRes {
	e.t.Helper()
	r, err := e.s.Action(context.Background(), sandbox, "work/main", a)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func TestWindowAllowsPushPRAndCIButNotMergeWithoutAsking(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 120, Opts{}); err != nil {
		t.Fatal(err)
	}
	// push access without any request
	if _, tok, err := e.s.Credential(ctx, "shop", "github.com", "o/r.git"); err != nil || tok == "" {
		t.Fatal("window must give credentials:", err)
	}
	if m, _, _ := e.f.snapshot(); m[0].Perms["contents"] != "write" {
		t.Fatalf("a window includes write access: %+v", m)
	}
	// a git grant request is answered at once with the window's id and nothing is shown to the user
	g, err := e.s.RequestGrant(ctx, "shop", "work/main", []string{"o/r"}, false, false, 10, "x")
	if err != nil || !g.Allowed || g.TaskID == 0 || !strings.Contains(g.Message, "full repo access") {
		t.Fatalf("%+v %v", g, err)
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatalf("no request may be created inside a window: %+v", rs)
	}
	// CI re-run needs no question
	if r := e.action("shop", ActionReq{Action: ActCIRerun, Repo: "o/r", RunID: 9}); r.Expired || strings.Contains(r.Message, "declined") {
		t.Fatalf("%+v", r)
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatalf("no request for CI re-run inside a window: %+v", rs)
	}
	// merge without the tick still asks
	done := make(chan ActionRes, 1)
	go func() { done <- e.action("shop", ActionReq{Action: ActPRMerge, Repo: "o/r", Number: 5}) }()
	r := e.pending("ghaction")
	if err := e.s.Decide(ctx, r.ID, false, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.OK {
		t.Fatalf("%+v", res)
	}
	if _, _, merged := e.f.snapshot(); len(merged) != 0 {
		t.Fatal("merge needs approval without the merge tick")
	}
}

func TestWindowWithMergeMergesWithoutAsking(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.OpenWindow(context.Background(), "shop", "o/r", 60, Opts{Merge: true}); err != nil {
		t.Fatal(err)
	}
	if r := e.action("shop", ActionReq{Action: ActPRMerge, Repo: "o/r", Number: 5, Method: "squash"}); !r.OK {
		t.Fatalf("%+v", r)
	}
	if _, _, merged := e.f.snapshot(); len(merged) != 1 {
		t.Fatal("merge must happen")
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("no request expected")
	}
}

func TestWindowIsPerRepoAndPerSandbox(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 60, Opts{Merge: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.OpenWindow(ctx, "shop", "o/second", 60, Opts{}); err != nil { // several windows in one sandbox
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"shop", "github.com", "o/r"}, {"shop", "github.com", "o/second"}} {
		if _, _, err := e.s.Credential(ctx, c[0], c[1], c[2]); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	// other repo, other sandbox: nothing
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/third"); err != ErrNoGrant {
		t.Fatal(err)
	}
	if _, _, err := e.s.Credential(ctx, "blog", "github.com", "o/r"); err != ErrNoGrant {
		t.Fatal(err)
	}
	// a request that is only partly covered still asks
	ch := make(chan GrantResult, 1)
	go func() {
		g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/r", "o/third"}, true, false, 10, "x")
		ch <- g
	}()
	rq := e.pending("gitgrant")
	_ = e.s.Decide(ctx, rq.ID, false, 0, false, "")
	if g := <-ch; g.Allowed {
		t.Fatal("must have asked")
	}
	// opening the same repo again replaces the window (one per repo)
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 30, Opts{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	gs, _ := e.st.ListGitGrants("shop", true, e.clock())
	for _, g := range gs {
		if g.Window && g.Repo == "o/r" {
			n++
			if g.Merge {
				t.Fatal("the new window replaced the merge setting")
			}
		}
	}
	if n != 1 {
		t.Fatalf("one window per repo, got %d", n)
	}
}

func TestWindowEndsAndAssistantCannotRelease(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, err := e.s.OpenWindow(ctx, "shop", "o/r", 180, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	rel, _ := e.s.Release(ctx, "shop", w.ID)
	if rel.Released || !strings.Contains(rel.Message, "only the user") {
		t.Fatalf("the assistant must not end a window: %+v", rel)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != nil {
		t.Fatal("window must survive the release attempt:", err)
	}
	// longer than 60 minutes: the token is replaced on demand, access stays
	e.advance(100 * time.Minute)
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != nil {
		t.Fatal("window must still work after the first token expired:", err)
	}
	if m, _, _ := e.f.snapshot(); len(m) < 2 {
		t.Fatal("a fresh token must have been minted")
	}
	// the user revokes: immediately over
	if err := e.s.Revoke(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != ErrNoGrant {
		t.Fatal(err)
	}
	// expiry
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 60, Opts{Merge: true}); err != nil {
		t.Fatal(err)
	}
	e.advance(61 * time.Minute)
	e.s.Sweep(ctx)
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != ErrNoGrant {
		t.Fatal("expired window must give nothing:", err)
	}
}

func TestWindowLimitsAndRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, err := e.s.OpenWindow(ctx, "shop", "o/r", 100000, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if max := e.clock().Add(MaxWindowMinutes * time.Minute); w.Expires.After(max.Add(time.Second)) {
		t.Fatalf("a window is limited to 24 hours: %v", w.Expires)
	}
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 0, Opts{}); err == nil {
		t.Fatal("a window needs a duration")
	}
	e.f.mu.Lock()
	e.f.missing["o/none"] = true
	e.f.mu.Unlock()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/none", 60, Opts{}); err == nil {
		t.Fatal("no window for a repo without the app")
	}
	if _, err := e.s.OpenWindow(ctx, "shop", "not a repo", 60, Opts{}); err == nil {
		t.Fatal("bad repo")
	}
}

func TestRequestAllowedAsWindow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ch := make(chan GrantResult, 1)
	go func() {
		g, _ := e.s.RequestGrant(ctx, "shop", "work/main", []string{"o/r", "o/s"}, false, false, 10, "big refactor")
		ch <- g
	}()
	r := e.pending("gitgrant")
	if err := e.s.DecideWindow(ctx, r.ID, 240, true, ""); err != nil {
		t.Fatal(err)
	}
	g := <-ch
	if !g.Allowed || len(g.TaskIDs) != 2 {
		t.Fatalf("%+v", g)
	}
	gs, _ := e.st.ListGitGrants("shop", true, e.clock())
	if len(gs) != 2 {
		t.Fatal(gs)
	}
	for _, x := range gs {
		if !x.Window || !x.Write || !x.Merge || x.Expires.Before(e.clock().Add(239*time.Minute)) {
			t.Fatalf("both repos need a 4 hour write window with merge: %+v", x)
		}
	}
	// a single action cannot be turned into a window
	go func() { _, _ = e.s.Action(ctx, "blog", "", ActionReq{Action: ActCIRerun, Repo: "o/r", RunID: 3}) }()
	a := e.pending("ghaction")
	if err := e.s.DecideWindow(ctx, a.ID, 60, false, ""); err == nil {
		t.Fatal("actions are decided once, not as windows")
	}
	_ = e.s.Decide(ctx, a.ID, false, 0, false, "")
}

func TestExtendGrantAndWindow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g := e.grant("shop", "o/r", true, true, 20, true)
	near := func(d, want time.Duration) bool { return d > want-2*time.Second && d < want+2*time.Second } // the database keeps seconds
	exp, err := e.s.Extend(ctx, g.TaskID, 15)
	if err != nil || !near(exp.Sub(g.Expires), 15*time.Minute) {
		t.Fatalf("%v %v", exp, err)
	}
	// a normal grant never ends later than 60 minutes from now
	exp, err = e.s.Extend(ctx, g.TaskID, 240)
	if err != nil || exp.After(e.clock().Add(MaxMinutes*time.Minute+time.Second)) {
		t.Fatalf("grant limited to 60 minutes from now: %v %v", exp, err)
	}
	if _, err := e.s.Extend(ctx, g.TaskID, 5); err == nil {
		t.Fatal("at the maximum: must say so")
	}
	// a window can grow up to 24 hours from now
	w, _ := e.s.OpenWindow(ctx, "shop", "o/w", 60, Opts{})
	exp, err = e.s.Extend(ctx, w.ID, 480)
	if err != nil || !near(exp.Sub(w.Expires), 480*time.Minute) {
		t.Fatalf("%v %v", exp, err)
	}
	exp, _ = e.s.Extend(ctx, w.ID, 100000)
	if exp.After(e.clock().Add(MaxWindowMinutes*time.Minute + time.Second)) {
		t.Fatal("window limited to 24 hours from now")
	}
	// ended or expired grants cannot be extended; bad input is refused
	if _, err := e.s.Extend(ctx, w.ID, 0); err == nil {
		t.Fatal("zero minutes")
	}
	_ = e.s.Revoke(ctx, w.ID)
	if _, err := e.s.Extend(ctx, w.ID, 10); err == nil {
		t.Fatal("revoked grant must not be extendable")
	}
	e.advance(61 * time.Minute)
	if _, err := e.s.Extend(ctx, g.TaskID, 10); err == nil {
		t.Fatal("expired grant must not be extendable")
	}
	if _, err := e.s.Extend(ctx, 9999, 10); err == nil {
		t.Fatal("unknown id")
	}
}

// ---- workflow files ----

func TestWorkflowsOnlyForTheTickedRepoAndWithItsOwnToken(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/ci", 60, Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	e.grant("shop", "o/plain", true, true, 20, true) // same owner, no workflows
	_, tokCI, err := e.s.Credential(ctx, "shop", "github.com", "o/ci")
	if err != nil {
		t.Fatal(err)
	}
	_, tokPlain, err := e.s.Credential(ctx, "shop", "github.com", "o/plain")
	if err != nil {
		t.Fatal(err)
	}
	if tokCI == tokPlain {
		t.Fatal("the workflows permission must not reach the other repo: two tokens")
	}
	m, _, _ := e.f.snapshot()
	if len(m) != 2 {
		t.Fatalf("%+v", m)
	}
	for _, x := range m {
		isCI := len(x.Repos) == 1 && x.Repos[0] == "ci"
		_, hasWF := x.Perms["workflows"]
		if isCI != hasWF || (isCI && x.Perms["workflows"] != "write") || (!isCI && (len(x.Repos) != 1 || x.Repos[0] != "plain")) {
			t.Fatalf("workflows must be in the token of o/ci only: %+v", x)
		}
	}
	// both are reused
	if _, again, _ := e.s.Credential(ctx, "shop", "github.com", "o/ci"); again != tokCI {
		t.Fatal("token of the workflows class must be reused")
	}
	// the audit log says so and never shows a token
	evs, _ := e.st.AuditList(100)
	seen := false
	for _, a := range evs {
		if strings.Contains(a.Detail, "ghs_") {
			t.Fatal("token in audit log")
		}
		if a.Event == "git.token" && strings.Contains(a.Detail, "workflows=write") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("audit must say that a token with workflows was minted")
	}
}

func TestWorkflowsNeedWriteAndTheUsersTick(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// asked write + workflows ticked, user lowers to read only: workflows is dropped with it
	ch := make(chan GrantResult, 1)
	go func() { g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/r"}, true, false, 10, "x"); ch <- g }()
	r := e.pending("gitgrant")
	if err := e.s.DecideOpts(ctx, r.ID, true, 10, false, "", Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	<-ch
	gs, _ := e.st.ListGitGrants("shop", true, e.clock())
	if len(gs) != 1 || gs[0].Write || gs[0].Workflows {
		t.Fatalf("read-only grant must not carry workflows: %+v", gs)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/r"); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := e.f.snapshot(); len(m[0].Perms) != 1 {
		t.Fatalf("contents only: %+v", m[0].Perms)
	}
	// without the tick a write grant has no workflows
	e.grant("shop", "o/w", true, true, 10, true)
	e.s.Credential(ctx, "shop", "github.com", "o/w")
	for _, x := range func() []mint { m, _, _ := e.f.snapshot(); return m }() {
		if _, ok := x.Perms["workflows"]; ok {
			t.Fatalf("no workflows without the user's tick: %+v", x)
		}
	}
	// ticked on a normal write grant
	ch2 := make(chan GrantResult, 1)
	go func() { g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/wf"}, true, false, 10, "x"); ch2 <- g }()
	r2 := e.pending("gitgrant")
	if err := e.s.DecideOpts(ctx, r2.ID, true, 10, true, "", Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	<-ch2
	gs, _ = e.st.ListGitGrants("shop", true, e.clock())
	found := false
	for _, g := range gs {
		if g.Repo == "o/wf" && g.Write && g.Workflows {
			found = true
		}
	}
	if !found {
		t.Fatal("the ticked write grant must carry workflows")
	}
}

func TestWorkflowsMissingPermissionGivesAHint(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.f.mu.Lock()
	e.f.noWF = true
	e.f.mu.Unlock()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/ci", 60, Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.s.Credential(ctx, "shop", "github.com", "o/ci")
	if err == nil || !strings.Contains(err.Error(), "Workflows: Read and write") {
		t.Fatalf("the user needs to learn which permission is missing: %v", err)
	}
	// a repo without the tick is not affected
	if _, err := e.s.OpenWindow(ctx, "shop", "o/other", 60, Opts{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/other"); err != nil {
		t.Fatal("repos without workflows must keep working:", err)
	}
}

func TestReleasingTheWorkflowsGrantRevokesItsTokenOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	w, _ := e.s.OpenWindow(ctx, "shop", "o/ci", 60, Opts{Workflows: true})
	e.grant("shop", "o/plain", true, true, 20, true)
	_, tokCI, _ := e.s.Credential(ctx, "shop", "github.com", "o/ci")
	_, tokPlain, _ := e.s.Credential(ctx, "shop", "github.com", "o/plain")
	if err := e.s.Revoke(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Minute) // grace time
	e.s.Sweep(ctx)
	_, rev, _ := e.f.snapshot()
	revoked := map[string]bool{}
	for _, r := range rev {
		revoked[r] = true
	}
	if !revoked[tokCI] {
		t.Fatalf("the workflows token must be revoked: %v", rev)
	}
	_ = tokPlain
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/ci"); err != ErrNoGrant {
		t.Fatal("o/ci must be over:", err)
	}
	if _, _, err := e.s.Credential(ctx, "shop", "github.com", "o/plain"); err != nil {
		t.Fatal("o/plain must keep working:", err)
	}
}

// ---- the assistant asks for workflow files ----

func TestAssistantAsksForWorkflowsUserDecides(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ch := make(chan GrantResult, 1)
	go func() {
		g, _ := e.s.RequestGrant(ctx, "shop", "work/main", []string{"o/ci"}, true, true, 10, "fix the pipeline")
		ch <- g
	}()
	r := e.pending("gitgrant")
	var d Detail
	_ = json.Unmarshal([]byte(r.Extra), &d)
	if !d.Workflows || !d.Write {
		t.Fatalf("the card must show that workflow files were asked for: %+v", d)
	}
	// nothing is granted until the user decides; the user's answer carries the final choice
	if gs, _ := e.st.ListGitGrants("shop", true, e.clock()); len(gs) != 0 {
		t.Fatal("no grant before the decision")
	}
	if err := e.s.DecideOpts(ctx, r.ID, true, 10, true, "", Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	if g := <-ch; !g.Allowed {
		t.Fatalf("%+v", g)
	}
	gs, _ := e.st.ListGitGrants("shop", true, e.clock())
	if len(gs) != 1 || !gs[0].Write || !gs[0].Workflows {
		t.Fatalf("%+v", gs)
	}
	// the user can also refuse the workflow part but allow the push
	ch2 := make(chan GrantResult, 1)
	go func() { g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/two"}, true, true, 10, "x"); ch2 <- g }()
	r2 := e.pending("gitgrant")
	if err := e.s.DecideOpts(ctx, r2.ID, true, 10, true, "", Opts{}); err != nil {
		t.Fatal(err)
	}
	<-ch2
	gs, _ = e.st.ListGitGrants("shop", true, e.clock())
	for _, g := range gs {
		if g.Repo == "o/two" && (g.Workflows || !g.Write) {
			t.Fatalf("push yes, workflows no: %+v", g)
		}
	}
}

func TestWorkflowsRequestWithoutWriteIsIgnored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	go func() { _, _ = e.s.RequestGrant(ctx, "shop", "", []string{"o/r"}, false, true, 10, "x") }()
	r := e.pending("gitgrant")
	var d Detail
	_ = json.Unmarshal([]byte(r.Extra), &d)
	if d.Workflows {
		t.Fatal("workflow files without push access make no sense and must not be shown as asked")
	}
	_ = e.s.Decide(ctx, r.ID, false, 0, false, "")
}

func TestFullAccessWithoutWorkflowsStillAsksWhenWorkflowsAreWanted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 60, Opts{}); err != nil {
		t.Fatal(err)
	}
	// plain request: answered by the window; with workflows: the user is asked
	if g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/r"}, true, false, 10, "x"); !g.Allowed {
		t.Fatal("covered by full repo access")
	}
	ch := make(chan GrantResult, 1)
	go func() { g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/r"}, true, true, 10, "x"); ch <- g }()
	r := e.pending("gitgrant")
	_ = e.s.Decide(ctx, r.ID, false, 0, false, "")
	if g := <-ch; g.Allowed {
		t.Fatal("workflow files need the user's decision even under full repo access")
	}
	// a full repo access that already has the tick covers the request
	if _, err := e.s.OpenWindow(ctx, "shop", "o/r", 60, Opts{Workflows: true}); err != nil {
		t.Fatal(err)
	}
	if g, _ := e.s.RequestGrant(ctx, "shop", "", []string{"o/r"}, true, true, 10, "x"); !g.Allowed {
		t.Fatal("full repo access with workflows covers the request")
	}
}

func (f *fakeGH) createdPRs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

// A git grant with write access is enough to open a pull request without a card; read-only grants and no grant still ask.
func TestPRCreateNeedsNoCardWithWriteGrant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	req := ActionReq{Action: ActPRCreate, Repo: "o/r", Title: "New", Head: "feature", Base: "main"}

	e.grant("shop", "o/r", true, true, 30, true)
	r, err := e.s.Action(ctx, "shop", "work/main", req)
	if err != nil || !r.OK || !strings.Contains(r.Message, "#6") {
		t.Fatalf("%+v %v", r, err)
	}
	if rs, _ := e.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatalf("no card may be created: %+v", rs)
	}
	if got := e.f.createdPRs(); len(got) != 1 || got[0] != "feature" {
		t.Fatalf("%v", got)
	}
	if r, _ := e.s.Action(ctx, "shop", "", ActionReq{Action: ActPRCreate, Repo: "o/r", Head: "x"}); r.OK {
		t.Fatal("title is required")
	}
	// another sandbox has no grant: it is asked
	done := make(chan ActionRes, 1)
	go func() { done <- e.action("other", req) }()
	c := e.pending("ghaction")
	if err := e.s.Decide(ctx, c.ID, false, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.OK {
		t.Fatalf("%+v", res)
	}
	if len(e.f.createdPRs()) != 1 {
		t.Fatal("a sandbox without a grant must be asked")
	}
}

func TestPRCreateWithReadOnlyGrantStillAsks(t *testing.T) {
	e := newEnv(t)
	e.grant("shop", "o/r", false, true, 30, false)
	done := make(chan ActionRes, 1)
	go func() {
		done <- e.action("shop", ActionReq{Action: ActPRCreate, Repo: "o/r", Title: "New", Head: "feature", Base: "main"})
	}()
	c := e.pending("ghaction")
	if err := e.s.Decide(context.Background(), c.ID, false, 0, false, ""); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.OK || len(e.f.createdPRs()) != 0 {
		t.Fatalf("%+v", res)
	}
}
