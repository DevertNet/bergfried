package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
