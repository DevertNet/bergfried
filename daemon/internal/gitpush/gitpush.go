// Package gitpush: Der Mac pusht für die VM. Die VM liefert ein `git bundle` mit ihren neuen Commits (über shared/out); der Mac
// ergänzt sie in einem Spiegel-Klon, prüft Regeln (nur agent/…, nur Fast-Forward, nie die Standard-Branches), erstellt eine
// Analyse für die Bestätigung (Commits, Diffstat, Workflows, mögliche Geheimnisse) und pusht nach Freigabe mit dem Token des Macs.
// Zugangsdaten gelangen nie in die VM.
package gitpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"gopkg.in/yaml.v3"
)

// Runner führt git aus (austauschbar für Tests).
type Runner interface {
	Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", firstArgs(args), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func firstArgs(a []string) string {
	for i, x := range a {
		if !strings.HasPrefix(x, "-") && x != "" && !strings.Contains(x, "=") && i > 0 && a[i-1] != "-c" && a[i-1] != "-C" {
			return x
		}
	}
	return strings.Join(a, " ")
}

// Creds liefert Benutzername und Token für git über HTTPS.
type Creds func(ctx context.Context) (user, token string, err error)

type Pusher struct {
	Dir       string // <Home>/repos
	Git       Runner
	Creds     Creds
	RemoteURL func(github.Repo) string // Standard: https://github.com/o/r.git (für Tests überschreibbar)
}

func New(dir string, c Creds) *Pusher {
	return &Pusher{Dir: dir, Git: ExecRunner{}, Creds: c, RemoteURL: func(r github.Repo) string { return r.HTTPSURL() }}
}

type Commit struct{ SHA, Author, Subject string }

type Analysis struct {
	Repo           string
	Branch         string
	DefaultBranch  string
	BaseSHA        string
	HeadSHA        string
	NewBranch      bool
	Commits        []Commit
	DiffStat       string
	FilesChanged   int
	Additions      int
	Deletions      int
	WorkflowFiles  []string // geänderte Dateien unter .github/workflows/
	Triggers       []string // Workflows, die durch diesen Push/PR starten würden
	SecretFindings []string // "datei:zeile – Muster" (ohne den Wert)
	IncomingRef    string
}

var (
	branchRe    = regexp.MustCompile(`^agent/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	protectedRe = regexp.MustCompile(`(?i)^(main|master|trunk|develop|dev|release.*|production|prod|staging|gh-pages)$`)
)

// CheckBranch: nur agent/<…>-Branches, nie Standard-/Release-Branches.
func CheckBranch(b string) error {
	if len(b) > 100 || !branchRe.MatchString(b) || strings.Contains(b, "..") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, "/") || strings.Contains(b, "//") || strings.Contains(b, "/.") {
		return fmt.Errorf("Branch %q ist nicht erlaubt (erlaubt: agent/<name>, nur a-z A-Z 0-9 . _ - und /)", truncate(b, 60))
	}
	if protectedRe.MatchString(b) {
		return fmt.Errorf("Branch %q ist geschützt", b)
	}
	return nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (p *Pusher) mirror(r github.Repo) string {
	return filepath.Join(p.Dir, strings.ToLower(r.Owner)+"__"+strings.ToLower(r.Name)+".git")
}

// gitArgs: gemeinsame Sicherheitsoptionen (keine Credential-Helper – sonst würde das Token im Schlüsselbund landen –, keine Hooks,
// nur https/Datei-Transport).
func base(extra ...string) []string {
	return append([]string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always", "-c", "protocol.http.allow=always", "-c", "protocol.file.allow=always", "-c", "core.fsmonitor=false"}, extra...)
}

// authEnv richtet GIT_ASKPASS ein; Token und Benutzer stehen nur in der Umgebung des git-Prozesses.
func (p *Pusher) authEnv(ctx context.Context) ([]string, error) {
	user, tok, err := p.Creds(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(p.Dir, ".bin"), 0o700); err != nil {
		return nil, err
	}
	script := filepath.Join(p.Dir, ".bin", "askpass.sh")
	body := "#!/bin/sh\ncase \"$1\" in\n*sername*) printf '%s' \"$BERGFRIED_GIT_USER\" ;;\n*) printf '%s' \"$BERGFRIED_GIT_TOKEN\" ;;\nesac\n"
	if b, _ := os.ReadFile(script); string(b) != body {
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
			return nil, err
		}
	}
	return []string{"GIT_ASKPASS=" + script, "GIT_TERMINAL_PROMPT=0", "BERGFRIED_GIT_USER=" + user, "BERGFRIED_GIT_TOKEN=" + tok,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}, nil
}

func (p *Pusher) git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := p.Git.Run(ctx, dir, env, args...)
	return strings.TrimSpace(string(out)), err
}

func (p *Pusher) ensureMirror(ctx context.Context, r github.Repo) (string, error) {
	m := p.mirror(r)
	if _, err := os.Stat(filepath.Join(m, "HEAD")); err != nil {
		if err := os.MkdirAll(p.Dir, 0o700); err != nil {
			return "", err
		}
		if _, err := p.git(ctx, "", nil, "init", "--bare", "-q", m); err != nil {
			return "", err
		}
	}
	// Remote-URL immer auf den erwarteten Wert setzen (nie aus VM-Daten).
	if _, err := p.git(ctx, m, nil, "remote", "set-url", "origin", p.RemoteURL(r)); err != nil {
		if _, err2 := p.git(ctx, m, nil, "remote", "add", "origin", p.RemoteURL(r)); err2 != nil {
			return "", err
		}
	}
	return m, nil
}

// Prepare ergänzt das Bundle im Spiegel-Klon und analysiert es. incomingRef bleibt bis Cleanup/Push bestehen.
func (p *Pusher) Prepare(ctx context.Context, r github.Repo, defaultBranch, branch, bundle, id string, wantPR bool) (*Analysis, error) {
	if err := CheckBranch(branch); err != nil {
		return nil, err
	}
	m, err := p.ensureMirror(ctx, r)
	if err != nil {
		return nil, err
	}
	env, err := p.authEnv(ctx)
	if err != nil {
		return nil, err
	}
	// Remote-Stand holen: Standard-Branch und – falls vorhanden – den Ziel-Branch.
	if _, err := p.git(ctx, m, env, base("fetch", "--no-tags", "origin", "+refs/heads/"+defaultBranch+":refs/remotes/origin/"+defaultBranch)...); err != nil {
		return nil, fmt.Errorf("Remote nicht erreichbar (Token/Rechte?): %w", err)
	}
	_, _ = p.git(ctx, m, env, base("fetch", "--no-tags", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch)...) // fehlt der Branch, ist er neu
	if _, err := p.git(ctx, m, nil, base("bundle", "verify", bundle)...); err != nil {
		return nil, fmt.Errorf("Bundle passt nicht zum Remote (fehlende Basis-Commits oder ungültig): %w", err)
	}
	incoming := "refs/bergfried/incoming/" + id
	if _, err := p.git(ctx, m, nil, base("-c", "transfer.fsckObjects=true", "fetch", "--no-tags", bundle, "+refs/heads/"+branch+":"+incoming)...); err != nil {
		return nil, fmt.Errorf("Bundle enthält den Branch %q nicht oder ist beschädigt: %w", branch, err)
	}
	head, err := p.git(ctx, m, nil, "rev-parse", "--verify", incoming)
	if err != nil {
		return nil, err
	}
	a := &Analysis{Repo: r.String(), Branch: branch, DefaultBranch: defaultBranch, HeadSHA: head, IncomingRef: incoming}
	remoteHead, rerr := p.git(ctx, m, nil, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch)
	if rerr == nil && remoteHead != "" {
		if _, err := p.git(ctx, m, nil, "merge-base", "--is-ancestor", remoteHead, head); err != nil {
			_ = p.Cleanup(ctx, r, incoming)
			return nil, fmt.Errorf("kein Fast-Forward: %s auf dem Remote enthält Commits, die der Agent nicht hat (Force-Push ist nicht erlaubt)", branch)
		}
		a.BaseSHA = remoteHead
	} else {
		a.NewBranch = true
		mb, err := p.git(ctx, m, nil, "merge-base", "refs/remotes/origin/"+defaultBranch, head)
		if err != nil || mb == "" {
			_ = p.Cleanup(ctx, r, incoming)
			return nil, fmt.Errorf("keine gemeinsame Historie mit %s – Push abgelehnt", defaultBranch)
		}
		a.BaseSHA = mb
	}
	log, err := p.git(ctx, m, nil, "log", "-n", "200", "--format=%H%x1f%an%x1f%s", a.BaseSHA+".."+head)
	if err != nil {
		return nil, err
	}
	for _, l := range strings.Split(log, "\n") {
		if f := strings.Split(l, "\x1f"); len(f) == 3 {
			a.Commits = append(a.Commits, Commit{f[0], f[1], truncate(f[2], 200)})
		}
	}
	if len(a.Commits) == 0 {
		_ = p.Cleanup(ctx, r, incoming)
		return nil, errors.New("nichts zu pushen (keine neuen Commits)")
	}
	if st, err := p.git(ctx, m, nil, "diff", "--stat=100", a.BaseSHA, head); err == nil {
		a.DiffStat = truncate(st, 4000)
	}
	if ns, err := p.git(ctx, m, nil, "diff", "--numstat", a.BaseSHA, head); err == nil {
		for _, l := range strings.Split(ns, "\n") {
			f := strings.Fields(l)
			if len(f) >= 3 {
				a.FilesChanged++
				ad, _ := strconv.Atoi(f[0])
				de, _ := strconv.Atoi(f[1])
				a.Additions += ad
				a.Deletions += de
				if strings.HasPrefix(f[2], ".github/workflows/") {
					a.WorkflowFiles = append(a.WorkflowFiles, f[2])
				}
			}
		}
	}
	a.Triggers = p.triggers(ctx, m, head, branch, wantPR)
	a.SecretFindings = p.scanSecrets(ctx, m, a.BaseSHA, head)
	return a, nil
}

// Cleanup entfernt die Hilfsreferenz.
func (p *Pusher) Cleanup(ctx context.Context, r github.Repo, ref string) error {
	if !strings.HasPrefix(ref, "refs/bergfried/incoming/") {
		return errors.New("unzulässige Referenz")
	}
	_, err := p.git(ctx, p.mirror(r), nil, "update-ref", "-d", ref)
	return err
}

// Push pusht den Stand der Hilfsreferenz nach refs/heads/<branch> – ohne Force.
func (p *Pusher) Push(ctx context.Context, r github.Repo, a *Analysis) error {
	if err := CheckBranch(a.Branch); err != nil {
		return err
	}
	m := p.mirror(r)
	env, err := p.authEnv(ctx)
	if err != nil {
		return err
	}
	_, err = p.git(ctx, m, env, base("push", "--porcelain", "origin", a.HeadSHA+":refs/heads/"+a.Branch)...)
	if err != nil {
		return fmt.Errorf("Push fehlgeschlagen: %w", err)
	}
	_ = p.Cleanup(ctx, r, a.IncomingRef)
	return nil
}

// ---- Workflow-Auslöser ----

func globToRe(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		case g[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(g[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if globToRe(p).MatchString(s) {
			return true
		}
	}
	return false
}

func strList(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

// triggers: welche Workflows der Version des Agents (HEAD) würden durch den Push (und ggf. den PR) starten.
func (p *Pusher) triggers(ctx context.Context, m, head, branch string, wantPR bool) []string {
	list, err := p.git(ctx, m, nil, "ls-tree", "-r", "--name-only", head, ".github/workflows")
	if err != nil || list == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(list, "\n") {
		if !strings.HasSuffix(f, ".yml") && !strings.HasSuffix(f, ".yaml") {
			continue
		}
		content, err := p.git(ctx, m, nil, "show", head+":"+f)
		if err != nil {
			continue
		}
		out = append(out, triggerNames(path.Base(f), content, branch, wantPR)...)
	}
	return out
}

func triggersFromYAML(content, branch string) []string {
	return triggerNames("x.yml", content, branch, false)
}

// triggerNames wertet den `on:`-Abschnitt eines Workflows für diesen Branch aus.
func triggerNames(name, content, branch string, wantPR bool) []string {
	var out []string
	var doc map[string]any
	if yaml.Unmarshal([]byte(content), &doc) != nil {
		return nil
	}
	on := doc["on"] // yaml.v3 liest "on" als Zeichenkette
	add := func(ev string) { out = append(out, name+" ("+ev+")") }
	switch o := on.(type) {
	case string:
		if o == "push" {
			add("push")
		}
		if wantPR && o == "pull_request" {
			add("pull_request")
		}
		if o == "pull_request_target" {
			add("pull_request_target ⚠")
		}
	case []any:
		for _, e := range o {
			ev := fmt.Sprint(e)
			if ev == "push" || (wantPR && ev == "pull_request") {
				add(ev)
			} else if ev == "pull_request_target" {
				add("pull_request_target ⚠")
			}
		}
	case map[string]any:
		if pv, ok := o["push"]; ok {
			run := true
			if cfg, ok := pv.(map[string]any); ok {
				if br := strList(cfg["branches"]); len(br) > 0 && !matchAny(br, branch) {
					run = false
				}
				if ig := strList(cfg["branches-ignore"]); len(ig) > 0 && matchAny(ig, branch) {
					run = false
				}
				if _, onlyTags := cfg["tags"]; onlyTags && cfg["branches"] == nil && cfg["branches-ignore"] == nil {
					run = false
				}
			}
			if run {
				add("push")
			}
		}
		if wantPR {
			if _, ok := o["pull_request"]; ok {
				add("pull_request")
			}
		}
		if _, ok := o["pull_request_target"]; ok {
			add("pull_request_target ⚠")
		}
	}
	return out
}

// ---- mögliche Geheimnisse im Diff ----

var secretRes = []struct {
	name string
	re   *regexp.Regexp
}{
	{"privater Schlüssel", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"GitHub-Token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)},
	{"AWS-Schlüssel", regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"API-Schlüssel", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"Slack-Token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{"Zugangsdaten in URL", regexp.MustCompile(`(?i)https?://[^/\s:@]+:[^/\s@]{4,}@`)},
	{"Passwort/Token-Zuweisung", regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key|access[_-]?key|token)\s*[:=]\s*["']?[A-Za-z0-9/+_\-]{12,}`)},
}

func (p *Pusher) scanSecrets(ctx context.Context, m, from, to string) []string {
	diff, err := p.git(ctx, m, nil, "diff", "-U0", "--no-color", from, to)
	if err != nil {
		return nil
	}
	var out []string
	file, line := "", 0
	hunk := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			file = strings.TrimPrefix(l, "+++ b/")
		case strings.HasPrefix(l, "@@"):
			if mm := hunk.FindStringSubmatch(l); mm != nil {
				line, _ = strconv.Atoi(mm[1])
				line--
			}
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			line++
			for _, s := range secretRes {
				if s.re.MatchString(l) {
					out = append(out, fmt.Sprintf("%s:%d – %s", file, line, s.name))
					break
				}
			}
			if len(out) >= 20 {
				return out
			}
		}
	}
	return out
}
