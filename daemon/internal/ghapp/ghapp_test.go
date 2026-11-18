package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func newStore(t *testing.T) (Store, *rsa.PrivateKey) {
	t.Helper()
	k, p := testKey(t)
	s := Store{Dir: filepath.Join(t.TempDir(), "github-app")}
	if err := s.Save(Config{AppID: 42, ClientID: "Iv1.abc", Slug: "bergfried-x"}, p); err != nil {
		t.Fatal(err)
	}
	return s, k
}

func TestStorePermissionsAndLoad(t *testing.T) {
	s, _ := newStore(t)
	for _, n := range []string{"app.json", "private-key.pem"} {
		fi, err := os.Stat(filepath.Join(s.Dir, n))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", n, fi, err)
		}
	}
	if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o700 {
		t.Fatal("directory must be 0700")
	}
	if !s.Configured() {
		t.Fatal("must be configured")
	}
	s.Delete()
	if s.Configured() {
		t.Fatal("must be deleted")
	}
	if err := s.Save(Config{AppID: 1, Slug: "x"}, []byte("not pem")); err == nil {
		t.Fatal("a broken key must be refused")
	}
}

func TestJWTVerifiesAndIsShortLived(t *testing.T) {
	s, k := newStore(t)
	now := time.Unix(1_800_000_000, 0)
	c := NewClient(s)
	c.Now = func() time.Time { return now }
	j, err := c.JWT()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(j, ".")
	if len(parts) != 3 {
		t.Fatal(j)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal("signature:", err)
	}
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var p struct {
		Iss string
		Iat int64
		Exp int64
	}
	_ = json.Unmarshal(pb, &p)
	if p.Iss != "Iv1.abc" || p.Iat != now.Unix()-60 || p.Exp-now.Unix() > 600 || p.Exp <= now.Unix() {
		t.Fatalf("%+v", p)
	}
}

func TestMintNarrowsAndRevokeUsesTheToken(t *testing.T) {
	s, _ := newStore(t)
	var gotBody map[string]any
	var revokedWith string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app/installations/7/access_tokens":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				w.WriteHeader(401)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(201)
			io.WriteString(w, `{"token":"ghs_secret","expires_at":"2030-01-01T00:00:00Z"}`)
		case r.Method == "DELETE" && r.URL.Path == "/installation/token":
			revokedWith = r.Header.Get("Authorization")
			w.WriteHeader(204)
		case r.URL.Path == "/repos/o/missing/installation":
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not Found"}`)
		case r.URL.Path == "/repos/o/r/installation":
			io.WriteString(w, `{"id":7}`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := NewClient(s)
	c.Base = srv.URL
	ctx := context.Background()
	id, err := c.InstallationFor(ctx, "o", "r")
	if err != nil || id != 7 {
		t.Fatal(id, err)
	}
	if _, err := c.InstallationFor(ctx, "o", "missing"); err != ErrNotInstalled {
		t.Fatal(err)
	}
	if _, err := c.InstallationFor(ctx, "o/../x", "r"); err == nil {
		t.Fatal("invalid names must be refused")
	}
	tok, err := c.Mint(ctx, 7, []string{"r"}, map[string]string{"contents": "write"})
	if err != nil || tok.Token != "ghs_secret" {
		t.Fatal(tok, err)
	}
	if repos, _ := gotBody["repositories"].([]any); len(repos) != 1 || repos[0] != "r" {
		t.Fatalf("repositories must be narrowed: %v", gotBody)
	}
	if perms, _ := gotBody["permissions"].(map[string]any); perms["contents"] != "write" {
		t.Fatalf("permissions must be narrowed: %v", gotBody)
	}
	if _, err := c.Mint(ctx, 7, nil, map[string]string{"contents": "read"}); err == nil {
		t.Fatal("a token without repositories must be refused")
	}
	if err := c.Revoke(ctx, "ghs_secret"); err != nil || revokedWith != "Bearer ghs_secret" {
		t.Fatal(revokedWith, err)
	}
}

func TestManifestFlow(t *testing.T) {
	k, p := testKey(t)
	_ = k
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/app-manifests/thecode/conversions" {
			w.WriteHeader(404)
			return
		}
		b, _ := json.Marshal(map[string]any{"id": 99, "slug": "bergfried-abc", "name": "Bergfried-abc", "client_id": "Iv1.x", "html_url": "https://github.com/apps/bergfried-abc", "pem": string(p)})
		w.WriteHeader(201)
		w.Write(b)
	}))
	defer api.Close()
	st := Store{Dir: filepath.Join(t.TempDir(), "ga")}
	su := NewSetup(st)
	su.API = api.URL
	var events []string
	su.Log = func(e, d string) { events = append(events, e) }
	defer su.Close()
	start, err := su.Start()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(start, "http://127.0.0.1:") {
		t.Fatal(start)
	}
	resp, err := http.Get(start)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	hs := html.UnescapeString(string(body))
	for _, want := range []string{"https://github.com/settings/apps/new?state=", `name="manifest"`, `"redirect_url":"http://127.0.0.1:`, `"default_permissions"`, `"public":false`} {
		if !strings.Contains(hs, want) {
			t.Fatalf("form misses %q:\n%s", want, hs)
		}
	}
	if strings.Contains(hs, "workflows") || strings.Contains(hs, "administration") {
		t.Fatal("the manifest must not ask for Workflows or Administration")
	}
	state := url.QueryEscape(su.active.state)
	cb := strings.TrimSuffix(start, "/start") + "/callback"
	// wrong state: refused, nothing stored
	if r, _ := http.Get(cb + "?code=thecode&state=wrong"); r.StatusCode != 400 || st.Configured() {
		t.Fatal("a wrong state must be refused")
	}
	r, err := http.Get(cb + "?code=thecode&state=" + state)
	if err != nil || r.StatusCode != 200 {
		t.Fatal(r, err)
	}
	if !st.Configured() {
		t.Fatal("the app must be stored")
	}
	c, _, _ := st.Load()
	if c.AppID != 99 || c.Slug != "bergfried-abc" {
		t.Fatalf("%+v", c)
	}
	if len(events) < 2 || events[len(events)-1] != "githubapp.setup.done" {
		t.Fatal(events)
	}
}
