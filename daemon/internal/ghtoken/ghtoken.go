// Package ghtoken: das GitHub-Token einer Sandbox (Fine-grained Personal Access Token).
// Es liegt in der VM (~/.git-credentials, 0600) und für CI-Abfragen des Daemons in ~/.bergfried/sandboxes/<name>/github-token (0600).
// Es dient für alle Git-Aktionen per HTTPS und für GitHub Actions. Der Agent in der VM kann es lesen – das ist Absicht und
// ein bewusster Kompromiss (siehe docs/DESIGN-GIT.md): begrenzt wird es nur über Repo-Auswahl, Rechte und Ablaufdatum des Tokens.
package ghtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

// ExpiresDays: Gültigkeit des vorgeschlagenen Tokens (GitHub erlaubt 1–366 Tage).
const ExpiresDays = 365

// Store: Mac-seitige Kopie je Sandbox.
type Store struct{ Home string }

func (s Store) path(sandbox string) string {
	return filepath.Join(s.Home, "sandboxes", sandbox, "github-token")
}

func (s Store) Get(sandbox string) (string, error) {
	b, err := os.ReadFile(s.path(sandbox))
	if err != nil {
		return "", github.ErrNoToken
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", github.ErrNoToken
	}
	return t, nil
}

func (s Store) Has(sandbox string) bool { _, err := s.Get(sandbox); return err == nil }

func (s Store) Set(sandbox, token string) error {
	if err := ValidToken(token); err != nil {
		return err
	}
	p := s.path(sandbox)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(token)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s Store) Delete(sandbox string) { _ = os.Remove(s.path(sandbox)) }

// ValidToken: nur druckbare Zeichen ohne Leerraum – das Token wird später in eine Datei geschrieben, nie in eine Befehlszeile.
func ValidToken(t string) error {
	t = strings.TrimSpace(t)
	if len(t) < 20 || len(t) > 255 {
		return errors.New("Token hat eine unplausible Länge")
	}
	for _, r := range t {
		if r <= ' ' || r > '~' || r == '@' || r == '/' || r == ':' || r == '%' || r == '\'' || r == '"' || r == '\\' {
			return errors.New("Token enthält unzulässige Zeichen")
		}
	}
	return nil
}

// CredentialsLine: Zeile für ~/.git-credentials (Format des credential.helper „store“).
func CredentialsLine(token string) string {
	return "https://x-access-token:" + strings.TrimSpace(token) + "@github.com\n"
}

// Link: GitHub-Formular „Neues Fine-grained Token“, soweit per URL vorbelegbar. Die Repo-Auswahl lässt sich nicht vorbelegen.
// Workflows-Recht fehlt bewusst: Der Agent soll .github/workflows nicht ändern können.
func Link(sandbox string) string {
	name := "bergfried-" + sandbox
	if len(name) > 40 {
		name = name[:40]
	}
	q := url.Values{}
	q.Set("name", name)
	q.Set("description", "Bergfried-Sandbox "+sandbox+": Git (clone/push/pull) und GitHub Actions für den Agent in der VM")
	q.Set("expires_in", fmt.Sprint(ExpiresDays))
	q.Set("contents", "write")
	q.Set("pull_requests", "write")
	q.Set("actions", "write")
	q.Set("metadata", "read")
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
}

// Hint: Text zur Repo-Auswahl (nicht vorbelegbar).
const Hint = "Unter „Repository access“ bitte „Only select repositories“ wählen und nur die Repos dieser Sandbox auswählen."

// Detail: Anzeige in der App (requests.extra).
type Detail struct {
	Repo     string `json:"repo"`
	HasToken bool   `json:"has_token"`
	Link     string `json:"link"`
	Hint     string `json:"hint"`
}

// Service: Anfrage „Token/Zugriff“ vom Agent, Entscheidung in der App.
type Service struct {
	S      *store.Store
	Tokens Store
	// Install schreibt das Token in die VM der Sandbox.
	Install func(ctx context.Context, sandbox, token string) error
	// Client liefert einen GitHub-Client für das Token (Tests: lokaler Ersatz).
	Client  func(token string) *github.Client
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]chan result
}

type result struct{ ok bool }

func NewService(st *store.Store, tokens Store, install func(context.Context, string, string) error) *Service {
	return &Service{S: st, Tokens: tokens, Install: install, Timeout: 10 * time.Minute, waiters: map[int64]chan result{},
		Client: func(t string) *github.Client { return github.New(github.StaticToken(t)) }}
}

func (s *Service) audit(event, sandbox, agent, detail string) {
	if err := s.S.Audit("daemon", event, sandbox, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNUNG: Audit-Log nicht geschrieben:", err)
	}
}

// CheckRepo: erreicht das Token der Sandbox dieses Repo?
func (s *Service) CheckRepo(ctx context.Context, token string, repo github.Repo) error {
	_, err := s.Client(token).DefaultBranch(ctx, repo)
	var ae *github.APIError
	if errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 403) {
		return fmt.Errorf("das Token hat keinen Zugriff auf %s (Repo nicht in der Auswahl oder Rechte fehlen)", repo)
	}
	return err
}

// Validate prüft, ob GitHub das Token akzeptiert.
func (s *Service) Validate(ctx context.Context, token string) error {
	if err := ValidToken(token); err != nil {
		return err
	}
	if err := s.Client(token).Ping(ctx); err != nil {
		var ae *github.APIError
		if errors.As(err, &ae) && ae.Status == 401 {
			return errors.New("GitHub lehnt das Token ab (ungültig, abgelaufen oder widerrufen)")
		}
		return err
	}
	return nil
}

// Deposit prüft das Token, legt es auf dem Mac ab und installiert es in der VM. repo (optional, owner/name) muss erreichbar sein.
func (s *Service) Deposit(ctx context.Context, sandbox, token, repo string) error {
	token = strings.TrimSpace(token)
	if err := s.Validate(ctx, token); err != nil {
		return err
	}
	if repo != "" {
		r, err := github.ParseRemote("https://github.com/" + repo)
		if err != nil {
			return err
		}
		if err := s.CheckRepo(ctx, token, r); err != nil {
			return err
		}
	}
	if err := s.Tokens.Set(sandbox, token); err != nil {
		return err
	}
	if s.Install != nil {
		if err := s.Install(ctx, sandbox, token); err != nil {
			return fmt.Errorf("Token konnte nicht in die VM geschrieben werden: %w", err)
		}
	}
	s.audit("ghtoken.set", sandbox, "", repo)
	return nil
}

// Request: Der Agent braucht ein Token (repo leer) oder Zugriff auf repo. Blockiert bis zur Entscheidung.
// Liefert (erledigt, Meldung). Ein Repo, das das Token schon erreicht, wird ohne Rückfrage bestätigt.
func (s *Service) Request(ctx context.Context, sandbox, agent, repo, reason string) (bool, string, error) {
	var gr github.Repo
	if repo != "" {
		r, err := github.ParseRemote("https://github.com/" + strings.TrimPrefix(repo, "https://github.com/"))
		if err != nil {
			return false, err.Error(), nil
		}
		gr, repo = r, r.String()
	}
	has := s.Tokens.Has(sandbox)
	if has && repo != "" {
		tok, _ := s.Tokens.Get(sandbox)
		if err := s.CheckRepo(ctx, tok, gr); err == nil {
			return true, "Das Token der Sandbox hat bereits Zugriff auf " + repo + ".", nil
		}
	}
	if has && repo == "" {
		return true, "Für diese Sandbox ist bereits ein Token hinterlegt. Nenne das Repo (owner/name), auf das der Zugriff fehlt.", nil
	}
	d := Detail{Repo: repo, HasToken: has, Link: Link(sandbox), Hint: Hint}
	extra, _ := json.Marshal(d)
	path := repo
	if path == "" {
		path = "GitHub-Token"
	}
	id, err := s.S.AddRequest(store.Request{Sandbox: sandbox, Agent: agent, Kind: "ghtoken", Path: path, Reason: reason, Status: "pending", Extra: string(extra)})
	if err != nil {
		return false, "", err
	}
	ch := make(chan result, 1)
	s.mu.Lock()
	s.waiters[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	s.audit("ghtoken.requested", sandbox, agent, fmt.Sprintf("id=%d %s", id, path))
	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.ok {
			return true, "Zugriff eingerichtet. Wiederhole den git-Befehl.", nil
		}
		return false, "Der Nutzer hat die Einrichtung abgelehnt.", nil
	case <-timer.C:
		if ok, _ := s.S.SetRequestStatus(id, "expired"); ok {
			s.audit("ghtoken.expired", sandbox, agent, fmt.Sprintf("id=%d", id))
		}
		return false, "Keine Antwort, Anfrage abgelaufen.", nil
	case <-ctx.Done():
		_, _ = s.S.SetRequestStatus(id, "expired")
		return false, "", ctx.Err()
	}
}

// Decide: Entscheidung der App. Mit Token: prüfen, ablegen, installieren. Ohne Token (Zugriff nur erweitert): vorhandenes Token
// gegen das Repo prüfen. Schlägt das fehl, bleibt die Anfrage offen und der Fehler geht an die App (erneut versuchen).
func (s *Service) Decide(ctx context.Context, id int64, allow bool, token string) error {
	r, err := s.S.GetRequest(id)
	if err != nil {
		return err
	}
	if r.Kind != "ghtoken" || r.Status != "pending" {
		return fmt.Errorf("Anfrage %d ist keine offene Token-Anfrage", id)
	}
	s.mu.Lock()
	ch, ok := s.waiters[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("Anfrage %d wartet nicht mehr (Agent hat abgebrochen)", id)
	}
	status := "denied"
	if allow {
		var d Detail
		_ = json.Unmarshal([]byte(r.Extra), &d)
		token = strings.TrimSpace(token)
		if token != "" {
			if err := s.Deposit(ctx, r.Sandbox, token, d.Repo); err != nil {
				return err
			}
		} else {
			tok, err := s.Tokens.Get(r.Sandbox)
			if err != nil {
				return errors.New("Bitte das Token einfügen")
			}
			gr, perr := github.ParseRemote("https://github.com/" + d.Repo)
			if d.Repo == "" || perr != nil {
				return errors.New("Bitte das Token einfügen")
			}
			if err := s.CheckRepo(ctx, tok, gr); err != nil {
				return err
			}
		}
		status = "allowed"
	}
	if ok, _ := s.S.SetRequestStatus(id, status); !ok {
		return errors.New("Anfrage nicht mehr offen")
	}
	s.audit("ghtoken."+status, r.Sandbox, r.Agent, fmt.Sprintf("id=%d", id))
	ch <- result{ok: allow}
	return nil
}
