package gitpush

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

const (
	MaxBundleBytes = 200 << 20
	BundleDir      = ".git-push"
)

// Detail: was in der Anfrage steht und in der App angezeigt wird (liegt als JSON in requests.extra).
type Detail struct {
	Repo          string   `json:"repo"`
	Branch        string   `json:"branch"`
	DefaultBranch string   `json:"default_branch"`
	NewBranch     bool     `json:"new_branch"`
	Base          string   `json:"base"`
	Head          string   `json:"head"`
	Commits       []Commit `json:"commits"`
	DiffStat      string   `json:"diffstat"`
	Files         int      `json:"files"`
	Additions     int      `json:"additions"`
	Deletions     int      `json:"deletions"`
	WorkflowFiles []string `json:"workflow_files"`
	Triggers      []string `json:"triggers"`
	Secrets       []string `json:"secrets"`
	WantPR        bool     `json:"want_pr"`
	PRTitle       string   `json:"pr_title"`
	PRBody        string   `json:"pr_body"`
}

// Normalize ersetzt leere Listen durch [] (sonst null – das würde das Dekodieren in der App brechen).
func (d *Detail) Normalize() {
	if d.Commits == nil {
		d.Commits = []Commit{}
	}
	for _, p := range []*[]string{&d.WorkflowFiles, &d.Triggers, &d.Secrets} {
		if *p == nil {
			*p = []string{}
		}
	}
}

type Decision struct {
	Allow            bool
	CreatePR         bool
	ConfirmWorkflows bool
	ConfirmSecrets   bool
}

type Input struct {
	RemoteURL, Branch, BundleRel, Reason string
	WantPR                               bool
	PRTitle, PRBody                      string
}

type pending struct {
	ch chan Decision
	d  Detail
}

type Service struct {
	S       *store.Store
	P       *Pusher
	GH      *github.Client
	Home    string // Datenverzeichnis (~/.bergfried)
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]*pending
}

func NewService(st *store.Store, p *Pusher, gh *github.Client, home string) *Service {
	return &Service{S: st, P: p, GH: gh, Home: home, Timeout: 10 * time.Minute, waiters: map[int64]*pending{}}
}

func (s *Service) audit(event, session, agent, detail string) {
	if err := s.S.Audit("daemon", event, session, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
	}
}

// OutDir: shared/out der Session auf dem Mac (in der VM unter demselben Pfad sichtbar).
func (s *Service) OutDir(session string) string {
	return filepath.Join(s.Home, "sessions", session, "shared", "out")
}

func deny(msg string) *pb.GitPushResult {
	return &pb.GitPushResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: msg}
}

// intake übernimmt das von der VM gelegte Bundle in ein privates Verzeichnis. Die VM kontrolliert shared/out und könnte
// Symlinks hinterlegen: der Pfad muss unterhalb von shared/out/.git-push liegen (nach Auflösung), eine normale Datei sein
// und wird ohne Symlink-Folgen geöffnet und kopiert.
func (s *Service) intake(session, rel, id string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || !strings.HasPrefix(rel, BundleDir+string(os.PathSeparator)) {
		return "", errors.New("Bundle-Pfad ungültig (muss unter .git-push/ in shared/out liegen)")
	}
	out := s.OutDir(session)
	realOut, err := filepath.EvalSymlinks(out)
	if err != nil {
		return "", errors.New("shared/out der Session nicht gefunden")
	}
	full := filepath.Join(realOut, rel)
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", errors.New("Bundle nicht gefunden")
	}
	allowed := filepath.Join(realOut, BundleDir) + string(os.PathSeparator)
	if !strings.HasPrefix(real, allowed) {
		return "", errors.New("Bundle-Pfad zeigt aus shared/out/.git-push heraus")
	}
	fi, err := os.Lstat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("Bundle ist keine normale Datei")
	}
	if fi.Size() > MaxBundleBytes {
		return "", fmt.Errorf("Bundle zu groß (max. %d MB)", MaxBundleBytes>>20)
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dir := filepath.Join(s.P.Dir, ".incoming")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, id+".bundle")
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(w, io.LimitReader(f, MaxBundleBytes+1)); err != nil {
		w.Close()
		return "", err
	}
	return dst, w.Close()
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) Request(ctx context.Context, session, agent string, in Input) (*pb.GitPushResult, error) {
	repo, err := github.ParseRemote(in.RemoteURL)
	if err != nil {
		return deny(err.Error()), nil
	}
	if err := CheckBranch(in.Branch); err != nil {
		s.audit("gitpush.rejected", session, agent, fmt.Sprintf("%s %s: %v", repo, truncate(in.Branch, 60), err))
		return deny(err.Error()), nil
	}
	id := randID()
	bundle, err := s.intake(session, in.BundleRel, id)
	if err != nil {
		s.audit("gitpush.rejected", session, agent, "Bundle: "+err.Error())
		return deny(err.Error()), nil
	}
	defer os.Remove(bundle)
	def, err := s.GH.DefaultBranch(ctx, repo)
	if err != nil {
		return deny(ghMessage(repo, err)), nil
	}
	a, err := s.P.Prepare(ctx, repo, def, in.Branch, bundle, id, in.WantPR)
	if err != nil {
		s.audit("gitpush.rejected", session, agent, fmt.Sprintf("%s %s: %v", repo, in.Branch, err))
		return deny(err.Error()), nil
	}
	d := Detail{Repo: a.Repo, Branch: a.Branch, DefaultBranch: a.DefaultBranch, NewBranch: a.NewBranch, Base: a.BaseSHA, Head: a.HeadSHA,
		Commits: a.Commits, DiffStat: a.DiffStat, Files: a.FilesChanged, Additions: a.Additions, Deletions: a.Deletions,
		WorkflowFiles: a.WorkflowFiles, Triggers: a.Triggers, Secrets: a.SecretFindings, WantPR: in.WantPR,
		PRTitle: truncate(in.PRTitle, 200), PRBody: truncate(in.PRBody, 4000)}
	d.Normalize()
	extra, _ := json.Marshal(d)
	reason := truncate(strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, in.Reason), 500)
	reqID, err := s.S.AddRequest(store.Request{Session: session, Agent: agent, Kind: "gitpush", Path: a.Repo + ":" + a.Branch, Reason: reason,
		Status: "pending", Bytes: int64(len(a.Commits)), Files: int64(a.FilesChanged), Extra: string(extra)})
	if err != nil {
		_ = s.P.Cleanup(ctx, repo, a.IncomingRef)
		return nil, err
	}
	p := &pending{ch: make(chan Decision, 1), d: d}
	s.mu.Lock()
	s.waiters[reqID] = p
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, reqID); s.mu.Unlock() }()
	s.audit("gitpush.requested", session, agent, fmt.Sprintf("id=%d %s %s commits=%d files=%d workflows=%d secrets=%d", reqID, a.Repo, a.Branch, len(a.Commits), a.FilesChanged, len(a.WorkflowFiles), len(a.SecretFindings)))
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case dec := <-p.ch:
		if !dec.Allow {
			_ = s.P.Cleanup(ctx, repo, a.IncomingRef)
			return &pb.GitPushResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: "Der Nutzer hat den Push abgelehnt."}, nil
		}
		return s.execute(ctx, session, agent, repo, a, in, dec)
	case <-timer.C:
		if ok, _ := s.S.SetRequestStatus(reqID, "expired"); ok {
			s.audit("gitpush.expired", session, agent, fmt.Sprintf("id=%d", reqID))
		}
		_ = s.P.Cleanup(context.Background(), repo, a.IncomingRef)
		return &pb.GitPushResult{Verdict: pb.Verdict_VERDICT_EXPIRED, Message: "Keine Antwort, Anfrage abgelaufen."}, nil
	case <-ctx.Done():
		if ok, _ := s.S.SetRequestStatus(reqID, "expired"); ok {
			s.audit("gitpush.cancelled", session, agent, fmt.Sprintf("id=%d", reqID))
		}
		_ = s.P.Cleanup(context.Background(), repo, a.IncomingRef)
		return nil, ctx.Err()
	}
}

func ghMessage(repo github.Repo, err error) string {
	var ae *github.APIError
	switch {
	case errors.Is(err, github.ErrNoToken):
		return err.Error()
	case errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403):
		return fmt.Sprintf("Kein Zugriff auf %s (Repo nicht gefunden oder das Token hat dafür keine Rechte).", repo)
	}
	return "GitHub: " + err.Error()
}

func (s *Service) execute(ctx context.Context, session, agent string, repo github.Repo, a *Analysis, in Input, dec Decision) (*pb.GitPushResult, error) {
	if err := s.P.Push(ctx, repo, a); err != nil {
		s.audit("gitpush.failed", session, agent, fmt.Sprintf("%s %s: %v", a.Repo, a.Branch, err))
		return deny(err.Error()), nil
	}
	res := &pb.GitPushResult{Verdict: pb.Verdict_VERDICT_ALLOWED, PushedSha: a.HeadSHA, Message: fmt.Sprintf("%d Commit(s) nach %s auf %s gepusht.", len(a.Commits), a.Branch, a.Repo)}
	if dec.CreatePR {
		title := strings.TrimSpace(in.PRTitle)
		if title == "" {
			title = a.Commits[0].Subject
		}
		body := truncate(in.PRBody, 4000) + fmt.Sprintf("\n\n---\nErstellt über Bergfried (Session %s, Agent %s); Push am Mac freigegeben.", session, agent)
		pr, err := s.GH.CreatePR(ctx, repo, a.Branch, a.DefaultBranch, title, body, false)
		switch {
		case err == nil:
			res.PrUrl = pr.URL
			res.Message += " Pull Request: " + pr.URL
		default:
			res.Message += " Pull Request konnte nicht angelegt werden: " + err.Error()
		}
	}
	if err := s.S.AddPush(store.GitPush{Session: session, Agent: agent, Repo: a.Repo, Branch: a.Branch, SHA: a.HeadSHA, PRURL: res.PrUrl}); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Push nicht vermerkt:", err)
	}
	s.audit("gitpush.pushed", session, agent, fmt.Sprintf("%s %s %s pr=%s", a.Repo, a.Branch, a.HeadSHA[:12], res.PrUrl))
	return res, nil
}

// Decide entscheidet eine offene Push-Anfrage. Workflow-Änderungen und mögliche Geheimnisse brauchen eine eigene Bestätigung.
func (s *Service) Decide(id int64, d Decision) (string, error) {
	r, err := s.S.GetRequest(id)
	if err != nil {
		return "", err
	}
	if r.Kind != "gitpush" || r.Status != "pending" {
		return "", fmt.Errorf("Anfrage %d ist keine offene Push-Anfrage", id)
	}
	s.mu.Lock()
	p, ok := s.waiters[id]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("Anfrage %d wartet nicht mehr (Agent hat abgebrochen)", id)
	}
	status := "denied"
	if d.Allow {
		if len(p.d.WorkflowFiles) > 0 && !d.ConfirmWorkflows {
			return "", errors.New("Der Push ändert .github/workflows/** – das startet Code mit Zugriff auf Repo-Secrets: zusätzliche Bestätigung nötig")
		}
		if len(p.d.Secrets) > 0 && !d.ConfirmSecrets {
			return "", errors.New("Im Diff stehen mögliche Geheimnisse: zusätzliche Bestätigung nötig")
		}
		status = "allowed"
	}
	if ok, _ := s.S.SetRequestStatus(id, status); !ok {
		return "", errors.New("Anfrage nicht mehr offen")
	}
	s.audit("gitpush."+status, r.Session, r.Agent, fmt.Sprintf("id=%d pr=%v workflows_confirmed=%v secrets_confirmed=%v", id, d.CreatePR, d.ConfirmWorkflows, d.ConfirmSecrets))
	p.ch <- d
	return status, nil
}
