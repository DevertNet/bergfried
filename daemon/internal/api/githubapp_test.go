package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/ghapp"
	"github.com/DevertNet/bergfried/daemon/internal/gitaccess"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

func appServer(t *testing.T) (*Server, string, ghapp.Store) {
	s, ts := srv(t)
	as := ghapp.Store{Dir: filepath.Join(t.TempDir(), "ga")}
	s.GitApp = ghapp.NewClient(as)
	s.GitSetup = ghapp.NewSetup(as)
	t.Cleanup(s.GitSetup.Close)
	s.Git = gitaccess.NewService(s.M.S, s.GitApp)
	return s, ts.URL, as
}

func TestGitHubAppStatusAndSetup(t *testing.T) {
	_, url, as := appServer(t)
	code, body := do(t, "GET", url+"/v1/githubapp", "")
	if code != 200 || !strings.Contains(body, `"configured":false`) || !strings.Contains(body, `"installations":[]`) {
		t.Fatal(code, body)
	}
	code, body = do(t, "POST", url+"/v1/githubapp/setup", "{}")
	if code != 200 || !strings.Contains(body, "http://127.0.0.1:") {
		t.Fatal(code, body)
	}
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemB := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := as.Save(ghapp.Config{AppID: 5, Slug: "bergfried-t", Name: "Bergfried-t"}, pemB); err != nil {
		t.Fatal(err)
	}
	if code, body = do(t, "POST", url+"/v1/githubapp/setup", "{}"); code != 409 {
		t.Fatalf("an existing app must not be replaced silently: %d %s", code, body)
	}
	code, body = do(t, "GET", url+"/v1/githubapp", "")
	if code != 200 || !strings.Contains(body, `"configured":true`) || !strings.Contains(body, "/apps/bergfried-t/installations/new") {
		t.Fatal(code, body)
	}
	if strings.Contains(body, "BEGIN") || strings.Contains(body, "private") {
		t.Fatalf("the key must never be in a response: %s", body)
	}
	if code, _ = do(t, "POST", url+"/v1/githubapp/forget", "{}"); code != 200 || as.Configured() {
		t.Fatal("forget must delete the key")
	}
}

func TestGitRequestsAreListedWithDetailAndGrantsEmpty(t *testing.T) {
	s, url, _ := appServer(t)
	extra, _ := json.Marshal(gitaccess.Detail{Kind: "grant", Repo: "o/r", Write: true, Minutes: 15, Max: 60})
	if _, err := s.M.S.AddRequest(store.Request{Sandbox: "shop", Kind: "gitgrant", Path: "o/r", Reason: "push", Status: "pending", Extra: string(extra)}); err != nil {
		t.Fatal(err)
	}
	code, body := do(t, "GET", url+"/v1/requests", "")
	if code != 200 || !strings.Contains(body, `"git":{"kind":"grant","repo":"o/r","write":true`) {
		t.Fatal(code, body)
	}
	if code, body = do(t, "GET", url+"/v1/git/grants", ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatal(code, body)
	}
	gid, _ := s.M.S.AddGitGrant(store.GitGrant{Sandbox: "shop", Repo: "o/r", Write: true, Expires: time.Now().Add(time.Hour)})
	if _, body = do(t, "GET", url+"/v1/git/grants", ""); !strings.Contains(body, `"repo":"o/r"`) {
		t.Fatal(body)
	}
	if code, _ = do(t, "POST", url+"/v1/git/revoke", `{"id":`+itoa(gid)+`}`); code != 200 {
		t.Fatal(code)
	}
	if gs, _ := s.M.S.ListGitGrants("", true, time.Now()); len(gs) != 0 {
		t.Fatal("revoked grant still active")
	}
	if code, _ = do(t, "POST", url+"/v1/git/decide", `{"id":999,"allow":true}`); code != 409 {
		t.Fatalf("an unknown request must be refused: %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestActiveGrantsCarryTheReason(t *testing.T) {
	s, ts := srv(t)
	if _, err := s.M.S.AddGrant(store.SSHGrant{Sandbox: "shop", Host: "h", KeyFP: "SHA256:x", User: "u", Expires: time.Now().Add(time.Hour), Reason: "deploy the fix"}); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/ssh/grants", ""); code != 200 || !strings.Contains(body, `"reason":"deploy the fix"`) {
		t.Fatal(code, body)
	}
	if _, err := s.M.S.AddGitGrant(store.GitGrant{Sandbox: "shop", Repo: "o/r", Reason: "push my branch", Expires: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, "GET", ts.URL+"/v1/git/grants", ""); code != 200 || !strings.Contains(body, `"reason":"push my branch"`) {
		t.Fatal(code, body)
	}
}
