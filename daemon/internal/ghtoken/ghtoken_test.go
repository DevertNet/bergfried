package ghtoken

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const tok = "github_pat_11ABCDEFG0abcdefghijklmnop_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func TestLink(t *testing.T) {
	u, err := url.Parse(Link("shop"))
	if err != nil || u.Host != "github.com" || u.Path != "/settings/personal-access-tokens/new" {
		t.Fatal(u, err)
	}
	q := u.Query()
	if q.Get("name") != "bergfried-shop" || q.Get("expires_in") != "365" || q.Get("contents") != "write" ||
		q.Get("actions") != "write" || q.Get("metadata") != "read" || q.Get("pull_requests") != "write" {
		t.Fatalf("%v", q)
	}
	if q.Get("workflows") != "" {
		t.Fatal("the Workflows permission must not be pre-filled")
	}
	if n := Link(strings.Repeat("a", 30)); !strings.Contains(n, "name=bergfried-"+strings.Repeat("a", 30)[:30]) {
		t.Fatal(n)
	}
	q2, _ := url.ParseQuery(strings.SplitN(Link(strings.Repeat("b", 30)), "?", 2)[1])
	if len(q2.Get("name")) > 40 {
		t.Fatal("Name > 40 Zeichen")
	}
}

func TestValidTokenAndCredentials(t *testing.T) {
	for _, bad := range []string{"", "kurz", tok + " x", "ab\ncd" + tok, tok + "@evil", tok + "/x", tok + "'"} {
		if ValidToken(bad) == nil {
			t.Errorf("%q should have been rejected", bad)
		}
	}
	if err := ValidToken(" " + tok + "\n"); err != nil {
		t.Fatal(err)
	}
	if CredentialsLine(tok) != "https://x-access-token:"+tok+"@github.com\n" {
		t.Fatal(CredentialsLine(tok))
	}
}

func TestStorePerms(t *testing.T) {
	s := Store{Home: t.TempDir()}
	if s.Has("a") {
		t.Fatal("token without Set")
	}
	if _, err := s.Get("a"); err != github.ErrNoToken {
		t.Fatal(err)
	}
	if err := s.Set("a", "too short"); err == nil {
		t.Fatal("invalid token stored")
	}
	if err := s.Set("a", tok); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(s.Home, "sandboxes", "a", "github-token"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("Rechte %v", fi.Mode().Perm())
	}
	if got, _ := s.Get("a"); got != tok {
		t.Fatal(got)
	}
	s.Delete("a")
	if s.Has("a") {
		t.Fatal("not deleted")
	}
}

type fx struct {
	svc       *Service
	st        *store.Store
	installed []string
	access    map[string]bool // Repo -> erreichbar
}

func setup(t *testing.T) *fx {
	st, err := store.Open(filepath.Join(t.TempDir(), "d"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{st: st, access: map[string]bool{"o/r": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /rate_limit", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /repos/{o}/{r}", func(w http.ResponseWriter, r *http.Request) {
		if f.access[r.PathValue("o")+"/"+r.PathValue("r")] && r.Header.Get("Authorization") == "Bearer "+tok {
			w.Write([]byte(`{"default_branch":"main"}`))
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.svc = NewService(st, Store{Home: t.TempDir()}, func(_ context.Context, sandbox, token string) error {
		f.installed = append(f.installed, sandbox+":"+token)
		return nil
	})
	f.svc.Client = func(token string) *github.Client {
		c := github.New(github.StaticToken(token))
		c.Base = srv.URL
		return c
	}
	f.svc.Timeout = 5 * time.Second
	return f
}

func waitPending(t *testing.T, st *store.Store) store.Request {
	for i := 0; i < 200; i++ {
		rs, _ := st.ListRequests(true, 10)
		if len(rs) > 0 {
			return rs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no open request")
	return store.Request{}
}

type reply struct {
	ok  bool
	msg string
	err error
}

func ask(f *fx, repo string) chan reply {
	ch := make(chan reply, 1)
	go func() {
		ok, msg, err := f.svc.Request(context.Background(), "shop", "main", repo, "clone")
		ch <- reply{ok, msg, err}
	}()
	return ch
}

func TestRequestFirstTokenFlow(t *testing.T) {
	f := setup(t)
	ch := ask(f, "o/r")
	req := waitPending(t, f.st)
	if req.Kind != "ghtoken" || req.Path != "o/r" || !strings.Contains(req.Extra, "personal-access-tokens/new") {
		t.Fatalf("%+v", req)
	}
	// Without a token you cannot simply say "done".
	if err := f.svc.Decide(context.Background(), req.ID, true, ""); err == nil {
		t.Fatal("it should have failed without a token")
	}
	// Invalid token (GitHub rejects it).
	if err := f.svc.Decide(context.Background(), req.ID, true, strings.Replace(tok, "11ABC", "22ZZZ", 1)); err == nil || !strings.Contains(err.Error(), "rejects") {
		t.Fatalf("%v", err)
	}
	if len(f.installed) != 0 || f.svc.Tokens.Has("shop") {
		t.Fatal("an invalid token was stored")
	}
	if err := f.svc.Decide(context.Background(), req.ID, true, tok); err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if !r.ok || r.err != nil {
		t.Fatalf("%+v", r)
	}
	if len(f.installed) != 1 || f.installed[0] != "shop:"+tok || !f.svc.Tokens.Has("shop") {
		t.Fatalf("installiert: %v", f.installed)
	}
}

func TestTokenWithoutRepoAccessKeepsRequestOpen(t *testing.T) {
	f := setup(t)
	ch := ask(f, "o/new")
	req := waitPending(t, f.st)
	err := f.svc.Decide(context.Background(), req.ID, true, tok)
	if err == nil || !strings.Contains(err.Error(), "no access to o/new") {
		t.Fatalf("%v", err)
	}
	if f.svc.Tokens.Has("shop") || len(f.installed) != 0 {
		t.Fatal("a token without repo access must not be stored")
	}
	if rs, _ := f.st.ListRequests(true, 10); len(rs) != 1 {
		t.Fatal("request must stay open")
	}
	f.access["o/new"] = true
	if err := f.svc.Decide(context.Background(), req.ID, true, tok); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; !r.ok {
		t.Fatalf("%+v", r)
	}
}

func TestExtendAccessWithoutNewToken(t *testing.T) {
	f := setup(t)
	if err := f.svc.Deposit(context.Background(), "shop", tok, "o/r"); err != nil {
		t.Fatal(err)
	}
	// Repo already reachable: confirmed immediately, no request.
	ok, msg, err := f.svc.Request(context.Background(), "shop", "main", "o/r", "")
	if err != nil || !ok || !strings.Contains(msg, "already has access") {
		t.Fatal(ok, msg, err)
	}
	if rs, _ := f.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("unnecessary request")
	}
	// New repo: request; the user extends the selection on GitHub and confirms without a new token.
	ch := ask(f, "https://github.com/o/zwei.git")
	req := waitPending(t, f.st)
	if req.Path != "o/zwei" {
		t.Fatal(req.Path)
	}
	if err := f.svc.Decide(context.Background(), req.ID, true, ""); err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("%v", err)
	}
	f.access["o/zwei"] = true
	if err := f.svc.Decide(context.Background(), req.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; !r.ok {
		t.Fatalf("%+v", r)
	}
	if len(f.installed) != 1 {
		t.Fatalf("the token must not be reinstalled on a mere extension: %v", f.installed)
	}
}

func TestDenyAndBadRepo(t *testing.T) {
	f := setup(t)
	ch := ask(f, "o/x")
	req := waitPending(t, f.st)
	if err := f.svc.Decide(context.Background(), req.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	if r := <-ch; r.ok || !strings.Contains(r.msg, "declined") {
		t.Fatalf("%+v", r)
	}
	ok, msg, _ := f.svc.Request(context.Background(), "shop", "main", "evil.com/../x", "")
	if ok || msg == "" {
		t.Fatal(ok, msg)
	}
}

func TestExpire(t *testing.T) {
	f := setup(t)
	f.svc.Timeout = 50 * time.Millisecond
	ok, msg, _ := f.svc.Request(context.Background(), "shop", "main", "o/x", "")
	if ok || !strings.Contains(msg, "expired") {
		t.Fatal(ok, msg)
	}
	if rs, _ := f.st.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("request did not expire")
	}
}
