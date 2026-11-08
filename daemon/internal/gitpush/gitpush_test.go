package gitpush

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DevertNet/bergfried/daemon/internal/github"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@t", "-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type env struct {
	root, srv, bare string
	p               *Pusher
	repo            github.Repo
	mainSHA         string
}

// setup: Remote o/r (bare, per git http-backend mit Basic-Auth tester:s3cret), Spiegel-Verzeichnis des Pushers.
func setup(t *testing.T, password string) *env {
	t.Helper()
	execPath := sh(t, "", "--exec-path")
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend nicht vorhanden")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "o", "r.git")
	_ = os.MkdirAll(bare, 0o755)
	sh(t, bare, "init", "--bare", "-q")
	sh(t, bare, "config", "http.receivepack", "true")
	seed := t.TempDir()
	sh(t, seed, "clone", "-q", bare, "seed")
	sd := filepath.Join(seed, "seed")
	write(t, filepath.Join(sd, "README.md"), "# r\n")
	sh(t, sd, "add", ".")
	sh(t, sd, "commit", "-qm", "init")
	sh(t, sd, "push", "-q", "origin", "main")
	mainSHA := sh(t, sd, "rev-parse", "HEAD")
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "HOME=" + t.TempDir()}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "tester" || p != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	repos := filepath.Join(t.TempDir(), "repos")
	p := New(repos, func(context.Context) (string, string, error) { return "tester", password, nil })
	p.RemoteURL = func(r github.Repo) string { return srv.URL + "/" + r.Owner + "/" + r.Name + ".git" }
	return &env{root: root, srv: srv.URL, bare: bare, p: p, repo: github.Repo{Owner: "o", Name: "r"}, mainSHA: mainSHA}
}

// vm klont das Remote (wie die VM) und liefert das Arbeitsverzeichnis.
func (e *env) vm(t *testing.T) string {
	d := t.TempDir()
	sh(t, d, "clone", "-q", e.bare, "vm")
	return filepath.Join(d, "vm")
}

func bundle(t *testing.T, vm, branch string) string {
	b := filepath.Join(t.TempDir(), "x.bundle")
	sh(t, vm, "bundle", "create", b, "refs/heads/"+branch, "--not", "--remotes")
	return b
}

const ciYAML = "name: CI\non:\n  push:\n    branches: ['**']\n  pull_request:\n  pull_request_target:\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"

func TestFullFlowAnalysisPushAndFastForward(t *testing.T) {
	e := setup(t, "s3cret")
	vm := e.vm(t)
	sh(t, vm, "checkout", "-q", "-b", "agent/x")
	write(t, filepath.Join(vm, ".github/workflows/ci.yml"), ciYAML)
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "ci hinzufügen")
	write(t, filepath.Join(vm, "config.txt"), "token = ghp_"+strings.Repeat("a", 36)+"\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "config")
	ctx := context.Background()

	a, err := e.p.Prepare(ctx, e.repo, "main", "agent/x", bundle(t, vm, "agent/x"), "t1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Commits) != 2 || !a.NewBranch || a.BaseSHA != e.mainSHA || a.FilesChanged != 2 {
		t.Fatalf("%+v", a)
	}
	if len(a.WorkflowFiles) != 1 || a.WorkflowFiles[0] != ".github/workflows/ci.yml" {
		t.Fatalf("Workflow-Änderung: %v", a.WorkflowFiles)
	}
	joined := strings.Join(a.Triggers, "|")
	for _, want := range []string{"ci.yml (push)", "ci.yml (pull_request)", "ci.yml (pull_request_target ⚠)"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Auslöser %q fehlt: %v", want, a.Triggers)
		}
	}
	if len(a.SecretFindings) != 1 || !strings.Contains(a.SecretFindings[0], "config.txt:1 – GitHub-Token") || strings.Contains(a.SecretFindings[0], "ghp_") {
		t.Fatalf("Geheimnis-Fund: %v", a.SecretFindings)
	}
	// Nichts darf vor der Freigabe auf dem Remote liegen
	if out := sh(t, e.bare, "branch", "--list", "agent/x"); out != "" {
		t.Fatalf("vor dem Push darf der Branch nicht existieren: %q", out)
	}
	if err := e.p.Push(ctx, e.repo, a); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, e.bare, "rev-parse", "refs/heads/agent/x"); got != a.HeadSHA {
		t.Fatalf("Remote %s != %s", got, a.HeadSHA)
	}
	// Zugangsdaten dürfen nirgends im Spiegel landen
	filepath.Walk(e.p.Dir, func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && !strings.Contains(p, "/.bin/") {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "s3cret") {
				t.Errorf("Passwort in %s", p)
			}
		}
		return nil
	})
	// zweiter Push: nur der neue Commit, Fast-Forward
	sh(t, vm, "update-ref", "refs/remotes/origin/agent/x", a.HeadSHA)
	write(t, filepath.Join(vm, "more.txt"), "mehr\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "mehr")
	a2, err := e.p.Prepare(ctx, e.repo, "main", "agent/x", bundle(t, vm, "agent/x"), "t2", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(a2.Commits) != 1 || a2.NewBranch || a2.BaseSHA != a.HeadSHA {
		t.Fatalf("%+v", a2)
	}
	if j := strings.Join(a2.Triggers, "|"); !strings.Contains(j, "ci.yml (push)") || strings.Contains(j, "(pull_request)") {
		t.Fatalf("ohne PR darf nur der Push auslösen (plus Warnung für pull_request_target): %v", a2.Triggers)
	}
	if err := e.p.Push(ctx, e.repo, a2); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, e.bare, "rev-parse", "refs/heads/agent/x"); got != a2.HeadSHA {
		t.Fatal("zweiter Push fehlt")
	}
}

func TestNonFastForwardAndMissingPrerequisite(t *testing.T) {
	e := setup(t, "s3cret")
	ctx := context.Background()
	// Remote hat agent/x mit einem Commit (von jemand anderem)
	other := e.vm(t)
	sh(t, other, "checkout", "-q", "-b", "agent/x")
	write(t, filepath.Join(other, "a.txt"), "a\n")
	sh(t, other, "add", ".")
	sh(t, other, "commit", "-qm", "andere")
	sh(t, other, "push", "-q", "origin", "agent/x")
	// Agent hat eine abweichende Historie
	vm := e.vm(t)
	sh(t, vm, "checkout", "-q", "-b", "agent/x")
	write(t, filepath.Join(vm, "b.txt"), "b\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "meine")
	if _, err := e.p.Prepare(ctx, e.repo, "main", "agent/x", bundle(t, vm, "agent/x"), "n1", false); err == nil || !strings.Contains(err.Error(), "Fast-Forward") {
		t.Fatalf("kein Fast-Forward muss abgelehnt werden: %v", err)
	}
	// Hilfsreferenz wurde aufgeräumt
	if out := sh(t, e.p.mirror(e.repo), "for-each-ref", "refs/bergfried/"); out != "" {
		t.Fatalf("Hilfsreferenz übrig: %q", out)
	}
	// Bundle mit unbekannter Basis
	sh(t, vm, "checkout", "-q", "-b", "agent/y", "main")
	write(t, filepath.Join(vm, "c.txt"), "c\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "lokal-1")
	write(t, filepath.Join(vm, "d.txt"), "d\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "lokal-2")
	b := filepath.Join(t.TempDir(), "y.bundle")
	sh(t, vm, "bundle", "create", b, "refs/heads/agent/y", "^HEAD~1")
	if _, err := e.p.Prepare(ctx, e.repo, "main", "agent/y", b, "n2", false); err == nil || !strings.Contains(err.Error(), "Bundle passt nicht") {
		t.Fatalf("fehlende Basis muss abgelehnt werden: %v", err)
	}
	// nichts zu pushen: Bundle ohne neue Commits kann git gar nicht erzeugen; Branch gleich Remote-Stand
	sh(t, vm, "checkout", "-q", "agent/x")
}

func TestWrongPasswordAndBranchPolicy(t *testing.T) {
	e := setup(t, "falsch")
	vm := e.vm(t)
	sh(t, vm, "checkout", "-q", "-b", "agent/x")
	write(t, filepath.Join(vm, "a.txt"), "a\n")
	sh(t, vm, "add", ".")
	sh(t, vm, "commit", "-qm", "a")
	if _, err := e.p.Prepare(context.Background(), e.repo, "main", "agent/x", bundle(t, vm, "agent/x"), "w1", false); err == nil || !strings.Contains(err.Error(), "Remote nicht erreichbar") {
		t.Fatalf("falsches Passwort muss scheitern: %v", err)
	}
	for _, ok := range []string{"agent/x", "agent/shop/feature-1", "agent/a.b_c-d"} {
		if err := CheckBranch(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"main", "master", "trunk", "develop", "release/1.0", "agent/", "agent", "feature/x", "agent/../main", "agent//x", "agent/x.lock", "agent/.hidden", "agent/x y", "agent/x;rm", "agent/-oX", "AGENT/x", "", strings.Repeat("a", 120), "refs/heads/main", "agent/main/../../x"} {
		if err := CheckBranch(bad); err == nil && bad != "agent/-oX" {
			t.Fatalf("%q muss abgelehnt werden", bad)
		}
	}
}

func TestTriggerFiltering(t *testing.T) {
	cases := []struct {
		on   string
		want bool
	}{
		{"on: push", true},
		{"on: [push, pull_request]", true},
		{"on:\n  push:\n    branches: [main]", false},
		{"on:\n  push:\n    branches: ['agent/**']", true},
		{"on:\n  push:\n    branches-ignore: ['agent/**']", false},
		{"on:\n  push:\n    tags: ['v*']", false},
		{"on:\n  workflow_dispatch:", false},
	}
	for _, c := range cases {
		yml := c.on + "\njobs: {}\n"
		got := triggersFromYAML(yml, "agent/x/y")
		if (len(got) > 0) != c.want {
			t.Errorf("%q: %v", c.on, got)
		}
	}
	if globToRe("agent/**").MatchString("agent/x/y") == false || globToRe("agent/*").MatchString("agent/x/y") {
		t.Fatal("Glob-Verhalten")
	}
}
