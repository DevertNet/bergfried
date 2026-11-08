// Package github: schmaler REST-Client für das, was der Daemon mit dem Token einer Session erledigt
// (Zugriff prüfen, Actions-Läufe/-Jobs/-Logs lesen, Läufe wiederholen). Die Agents bekommen nur Ergebnisse (geschwärzt, gekürzt).
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

var ErrNoToken = errors.New("für diese Session ist kein GitHub-Token hinterlegt – rufe request_github_token auf")

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

// Ping prüft, ob GitHub das Token akzeptiert (GET /rate_limit; 401 = ungültig/abgelaufen). Sagt nichts über Repo-Rechte.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "/rate_limit", nil, nil)
	return err
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
