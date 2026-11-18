package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The REST calls the daemon makes on the assistant's behalf (after the user allowed them, see gitaccess).
// Results are reduced to a few fields: the assistant sees what it needs, never raw responses or tokens.

// PR is the reduced view of a pull request.
type PR struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable,omitempty"`
	Head      string `json:"head"`
	Base      string `json:"base"`
	URL       string `json:"url"`
}

type prJSON struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Merged    bool   `json:"merged"`
	Mergeable *bool  `json:"mergeable"`
	HTMLURL   string `json:"html_url"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p prJSON) pr() PR {
	return PR{p.Number, p.Title, p.State, p.Draft, p.Merged, p.Mergeable, p.Head.Ref, p.Base.Ref, p.HTMLURL}
}

func prPath(r Repo, n int) string { return fmt.Sprintf("/repos/%s/pulls/%d", r, n) }

// CreatePR opens a pull request.
func (c *Client) CreatePR(ctx context.Context, r Repo, title, head, base, body string, draft bool) (PR, error) {
	var d prJSON
	_, err := c.do(ctx, "POST", "/repos/"+r.String()+"/pulls", map[string]any{"title": title, "head": head, "base": base, "body": body, "draft": draft}, &d)
	return d.pr(), err
}

// GetPR reads a pull request.
func (c *Client) GetPR(ctx context.Context, r Repo, n int) (PR, error) {
	var d prJSON
	_, err := c.do(ctx, "GET", prPath(r, n), nil, &d)
	return d.pr(), err
}

// MergePR merges a pull request. method: merge | squash | rebase.
func (c *Client) MergePR(ctx context.Context, r Repo, n int, method string) (string, error) {
	switch method {
	case "merge", "squash", "rebase":
	case "":
		method = "merge"
	default:
		return "", errors.New("merge method must be merge, squash or rebase")
	}
	var d struct {
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if _, err := c.do(ctx, "PUT", prPath(r, n)+"/merge", map[string]any{"merge_method": method}, &d); err != nil {
		return "", err
	}
	if !d.Merged {
		return "", errors.New("GitHub did not merge: " + truncate(d.Message, 200))
	}
	return d.Message, nil
}

// Run is the reduced view of a workflow run.
type Run struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
	URL        string `json:"url"`
}

// ListRuns lists the latest workflow runs, optionally of one branch.
func (c *Client) ListRuns(ctx context.Context, r Repo, branch string, limit int) ([]Run, error) {
	if limit < 1 || limit > 30 {
		limit = 10
	}
	q := url.Values{"per_page": {fmt.Sprint(limit)}}
	if branch != "" {
		q.Set("branch", branch)
	}
	var d struct {
		Runs []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Branch     string `json:"head_branch"`
			SHA        string `json:"head_sha"`
			HTMLURL    string `json:"html_url"`
		} `json:"workflow_runs"`
	}
	if _, err := c.do(ctx, "GET", "/repos/"+r.String()+"/actions/runs?"+q.Encode(), nil, &d); err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(d.Runs))
	for _, x := range d.Runs {
		out = append(out, Run{x.ID, x.Name, x.Status, x.Conclusion, x.Branch, strings.TrimSpace(x.SHA), x.HTMLURL})
	}
	return out, nil
}

// RerunFailed re-runs the failed jobs of a workflow run.
func (c *Client) RerunFailed(ctx context.Context, r Repo, runID int64) error {
	_, err := c.do(ctx, "POST", fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", r, runID), nil, nil)
	return err
}
