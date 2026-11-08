// Package github: schmaler REST-Client für das, was Bergfried auf dem Mac für Agents erledigt
// (Pull Request anlegen, Actions-Läufe/-Jobs/-Logs lesen, Läufe wiederholen). Das Token liegt im Schlüsselbund und
// verlässt den Mac nie; die VM bekommt nur Ergebnisse (geschwärzt, gekürzt).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const apiVersion = "2022-11-28"

// TokenSource liefert das GitHub-Token (Schlüsselbund, Umgebung oder Test).
type TokenSource interface{ Token() (string, error) }

type StaticToken string

func (s StaticToken) Token() (string, error) {
	if s == "" {
		return "", ErrNoToken
	}
	return string(s), nil
}

var ErrNoToken = errors.New("kein GitHub-Token hinterlegt (App → Einstellungen → GitHub)")

const (
	KeychainService = "de.einfach123.bergfried.github"
	KeychainAccount = "token"
)

// Keychain liest/schreibt das Token im macOS-Schlüsselbund über `security`. BERGFRIED_GITHUB_TOKEN (nur Entwicklung) hat Vorrang.
type Keychain struct{}

func (Keychain) Token() (string, error) {
	if v := strings.TrimSpace(os.Getenv("BERGFRIED_GITHUB_TOKEN")); v != "" {
		return v, nil
	}
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", KeychainService, "-a", KeychainAccount, "-w").Output()
	if err != nil {
		return "", ErrNoToken
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", ErrNoToken
	}
	return t, nil
}

// Set speichert das Token (ersetzt ein vorhandenes). Hinweis: `security` bekommt das Token als Argument (für kurze Zeit in der
// Prozessliste desselben Nutzers sichtbar) – ein Weg ohne Argument gibt es nur interaktiv.
func (Keychain) Set(token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("Token ungültig")
	}
	out, err := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", KeychainService, "-a", KeychainAccount, "-w", token).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Schlüsselbund: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (Keychain) Delete() error {
	_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", KeychainService, "-a", KeychainAccount).Run()
	return nil
}

type Repo struct{ Owner, Name string }

func (r Repo) String() string   { return r.Owner + "/" + r.Name }
func (r Repo) HTTPSURL() string { return "https://github.com/" + r.Owner + "/" + r.Name + ".git" }

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// ParseRemote versteht https://github.com/o/r(.git), git@github.com:o/r(.git) und ssh://git@github.com/o/r(.git).
// Andere Hosts werden abgelehnt.
func ParseRemote(u string) (Repo, error) {
	u = strings.TrimSpace(u)
	var path string
	switch {
	case strings.HasPrefix(u, "https://github.com/"):
		path = strings.TrimPrefix(u, "https://github.com/")
	case strings.HasPrefix(u, "git@github.com:"):
		path = strings.TrimPrefix(u, "git@github.com:")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		path = strings.TrimPrefix(u, "ssh://git@github.com/")
	default:
		// https://user:token@github.com/… wird bewusst nicht akzeptiert (keine Zugangsdaten in URLs)
		return Repo{}, fmt.Errorf("nur github.com wird unterstützt (Remote: %q)", truncate(u, 80))
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !nameRe.MatchString(parts[0]) || !nameRe.MatchString(parts[1]) || parts[0] == "." || parts[1] == ".." || parts[0] == ".." || parts[1] == "." {
		return Repo{}, fmt.Errorf("ungültiger Repo-Pfad %q", truncate(path, 80))
	}
	return Repo{parts[0], parts[1]}, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

type Client struct {
	Base   string // Standard https://api.github.com
	HTTP   *http.Client
	Tokens TokenSource
}

func New(t TokenSource) *Client {
	return &Client{Base: "https://api.github.com", HTTP: &http.Client{Timeout: 60 * time.Second}, Tokens: t}
}

// APIError: Fehlerantwort von GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	tok, err := c.Tokens.Token()
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "bergfried")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return resp.StatusCode, &APIError{resp.StatusCode, truncate(e.Message, 300)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("Antwort nicht lesbar: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var u struct{ Login string }
	if _, err := c.do(ctx, "GET", "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (c *Client) DefaultBranch(ctx context.Context, r Repo) (string, error) {
	var d struct {
		DefaultBranch string `json:"default_branch"`
	}
	if _, err := c.do(ctx, "GET", "/repos/"+r.String(), nil, &d); err != nil {
		return "", err
	}
	return d.DefaultBranch, nil
}

type PR struct {
	URL    string
	Number int
}

// CreatePR legt einen Pull Request an (POST /repos/{o}/{r}/pulls).
func (c *Client) CreatePR(ctx context.Context, r Repo, head, base, title, body string, draft bool) (PR, error) {
	var out struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	_, err := c.do(ctx, "POST", "/repos/"+r.String()+"/pulls", map[string]any{"title": title, "head": head, "base": base, "body": body, "draft": draft}, &out)
	return PR{out.HTMLURL, out.Number}, err
}

type Run struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Title      string    `json:"display_title"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Branch     string    `json:"head_branch"`
	SHA        string    `json:"head_sha"`
	URL        string    `json:"html_url"`
	Event      string    `json:"event"`
	RunNumber  int       `json:"run_number"`
	Attempt    int       `json:"run_attempt"`
	Path       string    `json:"path"`
	Created    time.Time `json:"created_at"`
	Updated    time.Time `json:"updated_at"`
}

func (r Run) Done() bool { return r.Status == "completed" }

// ListRuns: Workflow-Läufe eines Branches, neueste zuerst (GET …/actions/runs?branch=).
func (c *Client) ListRuns(ctx context.Context, r Repo, branch string) ([]Run, error) {
	var d struct {
		Runs []Run `json:"workflow_runs"`
	}
	q := url.Values{"branch": {branch}, "per_page": {"20"}}
	if _, err := c.do(ctx, "GET", "/repos/"+r.String()+"/actions/runs?"+q.Encode(), nil, &d); err != nil {
		return nil, err
	}
	return d.Runs, nil
}

func (c *Client) GetRun(ctx context.Context, r Repo, id int64) (Run, error) {
	var run Run
	_, err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d", r, id), nil, &run)
	return run, err
}

type Step struct {
	Name       string `json:"name"`
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

type Job struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Branch     string `json:"head_branch"`
	SHA        string `json:"head_sha"`
	URL        string `json:"html_url"`
	Steps      []Step `json:"steps"`
}

func (c *Client) ListJobs(ctx context.Context, r Repo, runID int64) ([]Job, error) {
	var d struct {
		Jobs []Job `json:"jobs"`
	}
	if _, err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=50", r, runID), nil, &d); err != nil {
		return nil, err
	}
	return d.Jobs, nil
}

func (c *Client) GetJob(ctx context.Context, r Repo, id int64) (Job, error) {
	var j Job
	_, err := c.do(ctx, "GET", fmt.Sprintf("/repos/%s/actions/jobs/%d", r, id), nil, &j)
	return j, err
}

// JobLogs lädt das Log eines Jobs (302 auf eine kurzlebige URL; der Authorization-Header wird beim Wechsel der Domain von Go entfernt).
func (c *Client) JobLogs(ctx context.Context, r Repo, jobID int64, maxBytes int64) (string, error) {
	tok, err := c.Tokens.Token()
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", c.Base, r, jobID), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "bergfried")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", &APIError{resp.StatusCode, truncate(strings.TrimSpace(string(b)), 200)}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	return string(b), err
}

// RerunFailed wiederholt die fehlgeschlagenen Jobs eines Laufs (POST …/rerun-failed-jobs, 201).
func (c *Client) RerunFailed(ctx context.Context, r Repo, runID int64) error {
	_, err := c.do(ctx, "POST", fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", r, runID), map[string]any{}, nil)
	return err
}
