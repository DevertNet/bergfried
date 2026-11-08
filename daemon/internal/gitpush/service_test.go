package gitpush

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

type svcEnv struct {
	*env
	svc  *Service
	home string
	vm   string
	prs  *[]map[string]any
}

func svcSetup(t *testing.T) *svcEnv {
	e := setup(t, "s3cret")
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "db"))
	if err != nil {
		t.Fatal(err)
	}
	var prs []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/o/r", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"default_branch":"main"}`)) })
	mux.HandleFunc("POST /repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		prs = append(prs, b)
		w.WriteHeader(201)
		w.Write([]byte(`{"html_url":"https://github.com/o/r/pull/1","number":1}`))
	})
	api := httptest.NewServer(mux)
	t.Cleanup(api.Close)
	gh := github.New(github.StaticToken("tok"))
	gh.Base = api.URL
	svc := NewService(st, e.p, gh, home)
	svc.Timeout = 3 * time.Second
	_ = os.MkdirAll(filepath.Join(home, "sessions", "shop", "shared", "out"), 0o755)
	return &svcEnv{env: e, svc: svc, home: home, vm: e.vm(t), prs: &prs}
}

func (e *svcEnv) commit(t *testing.T, branch, file, content string, newBranch bool) {
	if newBranch {
		sh(t, e.vm, "checkout", "-q", "-b", branch)
	}
	write(t, filepath.Join(e.vm, file), content)
	sh(t, e.vm, "add", ".")
	sh(t, e.vm, "commit", "-qm", "änderung "+file)
}

// place legt das Bundle wie die VM nach shared/out/.git-push/.
func (e *svcEnv) place(t *testing.T, branch string) string {
	rel := filepath.Join(BundleDir, "b1.bundle")
	dst := filepath.Join(e.svc.OutDir("shop"), rel)
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	sh(t, e.vm, "bundle", "create", dst, "refs/heads/"+branch, "--not", "--remotes")
	return rel
}

func (e *svcEnv) pendingID(t *testing.T) int64 {
	for i := 0; i < 200; i++ {
		rs, _ := e.svc.S.ListRequests(true, 10)
		for _, r := range rs {
			if r.Kind == "gitpush" {
				return r.ID
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("keine offene Push-Anfrage")
	return 0
}

func (e *svcEnv) request(rel, branch string, wantPR bool) chan *pb.GitPushResult {
	ch := make(chan *pb.GitPushResult, 1)
	go func() {
		res, _ := e.svc.Request(context.Background(), "shop", "main", Input{RemoteURL: "https://github.com/o/r.git", Branch: branch, BundleRel: rel,
			Reason: "weil", WantPR: wantPR, PRTitle: "Titel", PRBody: "Text"})
		ch <- res
	}()
	return ch
}

func TestServiceAllowPushAndPR(t *testing.T) {
	e := svcSetup(t)
	e.commit(t, "agent/x", "a.txt", "a\n", true)
	ch := e.request(e.place(t, "agent/x"), "agent/x", true)
	id := e.pendingID(t)
	// Vor der Entscheidung ist nichts auf dem Remote
	if out := sh(t, e.bare, "branch", "--list", "agent/x"); out != "" {
		t.Fatal("vor der Freigabe darf nichts gepusht sein")
	}
	rs, _ := e.svc.S.ListRequests(true, 10)
	var d Detail
	if err := json.Unmarshal([]byte(rs[0].Extra), &d); err != nil || len(d.Commits) != 1 || d.Repo != "o/r" || !d.WantPR {
		t.Fatalf("%+v %v", d, err)
	}
	// Leere Listen müssen als [] gespeichert sein (null bricht die App)
	if strings.Contains(rs[0].Extra, "null") {
		t.Fatalf("Extra enthält null: %s", rs[0].Extra)
	}
	if _, err := e.svc.Decide(id, Decision{Allow: true, CreatePR: true}); err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.Verdict != pb.Verdict_VERDICT_ALLOWED || res.PrUrl != "https://github.com/o/r/pull/1" || res.PushedSha == "" {
		t.Fatalf("%+v", res)
	}
	if got := sh(t, e.bare, "rev-parse", "refs/heads/agent/x"); got != res.PushedSha {
		t.Fatal("Remote hat den Commit nicht")
	}
	if len(*e.prs) != 1 || (*e.prs)[0]["head"] != "agent/x" || (*e.prs)[0]["base"] != "main" || !strings.Contains((*e.prs)[0]["body"].(string), "Bergfried") {
		t.Fatalf("%v", *e.prs)
	}
	if !e.svc.S.HasPushed("shop", "o/r", "agent/x") || e.svc.S.HasPushed("shop", "o/r", "agent/y") {
		t.Fatal("Push muss vermerkt sein (nur für diesen Branch)")
	}
	if _, err := os.Stat(filepath.Join(e.svc.P.Dir, ".incoming", "x")); err == nil {
		t.Fatal()
	}
}

func TestServiceDenyPushesNothing(t *testing.T) {
	e := svcSetup(t)
	e.commit(t, "agent/x", "a.txt", "a\n", true)
	ch := e.request(e.place(t, "agent/x"), "agent/x", false)
	id := e.pendingID(t)
	if _, err := e.svc.Decide(id, Decision{Allow: false}); err != nil {
		t.Fatal(err)
	}
	if res := <-ch; res.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatalf("%+v", res)
	}
	if out := sh(t, e.bare, "branch", "--list", "agent/x"); out != "" {
		t.Fatal("abgelehnt: nichts darf gepusht sein")
	}
	if e.svc.S.HasPushed("shop", "o/r", "agent/x") {
		t.Fatal("nicht vermerken")
	}
	if _, err := e.svc.Decide(id, Decision{Allow: true}); err == nil {
		t.Fatal("zweite Entscheidung muss scheitern")
	}
}

func TestServiceWorkflowAndSecretNeedExtraConfirmation(t *testing.T) {
	e := svcSetup(t)
	e.commit(t, "agent/x", ".github/workflows/ci.yml", ciYAML, true)
	e.commit(t, "agent/x", "k.txt", "api_key = abcdefghijklmnop1234\n", false)
	ch := e.request(e.place(t, "agent/x"), "agent/x", false)
	id := e.pendingID(t)
	if _, err := e.svc.Decide(id, Decision{Allow: true}); err == nil || !strings.Contains(err.Error(), "workflows") {
		t.Fatalf("Workflow-Änderung braucht Zusatzbestätigung: %v", err)
	}
	if _, err := e.svc.Decide(id, Decision{Allow: true, ConfirmWorkflows: true}); err == nil || !strings.Contains(err.Error(), "Geheimnisse") {
		t.Fatalf("Geheimnisse brauchen Zusatzbestätigung: %v", err)
	}
	if out := sh(t, e.bare, "branch", "--list", "agent/x"); out != "" {
		t.Fatal("ohne Bestätigung nichts pushen")
	}
	if _, err := e.svc.Decide(id, Decision{Allow: true, ConfirmWorkflows: true, ConfirmSecrets: true}); err != nil {
		t.Fatal(err)
	}
	if res := <-ch; res.Verdict != pb.Verdict_VERDICT_ALLOWED {
		t.Fatalf("%+v", res)
	}
}

func TestServiceRejectsBadInput(t *testing.T) {
	e := svcSetup(t)
	e.commit(t, "agent/x", "a.txt", "a\n", true)
	rel := e.place(t, "agent/x")
	run := func(remote, branch, rel string) *pb.GitPushResult {
		res, _ := e.svc.Request(context.Background(), "shop", "main", Input{RemoteURL: remote, Branch: branch, BundleRel: rel})
		return res
	}
	good := "https://github.com/o/r.git"
	for name, res := range map[string]*pb.GitPushResult{
		"Branch main":         run(good, "main", rel),
		"fremder Host":        run("https://evil.com/o/r.git", "agent/x", rel),
		"Zugangsdaten in URL": run("https://u:p@github.com/o/r.git", "agent/x", rel),
		"Pfad außerhalb":      run(good, "agent/x", "../../x.bundle"),
		"nicht .git-push":     run(good, "agent/x", "x.bundle"),
		"absolut":             run(good, "agent/x", "/etc/passwd"),
	} {
		if res.Verdict == pb.Verdict_VERDICT_ALLOWED || res.Verdict == pb.Verdict_VERDICT_EXPIRED {
			t.Fatalf("%s muss abgelehnt werden: %+v", name, res)
		}
	}
	// Symlink als Bundle (zeigt auf eine Datei außerhalb)
	secret := filepath.Join(t.TempDir(), "geheim.txt")
	write(t, secret, "GEHEIM")
	link := filepath.Join(e.svc.OutDir("shop"), BundleDir, "link.bundle")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if res := run(good, "agent/x", filepath.Join(BundleDir, "link.bundle")); res.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(res.Message, "heraus") {
		t.Fatalf("Symlink-Bundle muss abgelehnt werden: %+v", res)
	}
	// Symlink-Verzeichnis
	dirLink := filepath.Join(e.svc.OutDir("shop"), BundleDir, "dir")
	if err := os.Symlink(filepath.Dir(secret), dirLink); err != nil {
		t.Fatal(err)
	}
	if res := run(good, "agent/x", filepath.Join(BundleDir, "dir", "geheim.txt")); res.Verdict != pb.Verdict_VERDICT_DENIED {
		t.Fatalf("Symlink-Verzeichnis: %+v", res)
	}
	if rs, _ := e.svc.S.ListRequests(true, 10); len(rs) != 0 {
		t.Fatal("abgelehnte Eingaben dürfen keine offene Anfrage erzeugen")
	}
}

func TestServiceNoTokenMessage(t *testing.T) {
	e := svcSetup(t)
	e.svc.GH.Tokens = github.StaticToken("")
	e.commit(t, "agent/x", "a.txt", "a\n", true)
	res, _ := e.svc.Request(context.Background(), "shop", "main", Input{RemoteURL: "https://github.com/o/r.git", Branch: "agent/x", BundleRel: e.place(t, "agent/x")})
	if res.Verdict != pb.Verdict_VERDICT_DENIED || !strings.Contains(res.Message, "Token") {
		t.Fatalf("%+v", res)
	}
}
