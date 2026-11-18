// Package ghapp: the user's own GitHub App. The private key never leaves the Mac (~/.bergfried/github-app, 0600).
// The daemon signs a short-lived JWT with it and exchanges that for installation tokens that are narrowed to
// repositories and permissions (see docs/GITHUB.md). Nothing in this package talks to the VM.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config is the public part of the app (the private key is stored next to it).
type Config struct {
	AppID    int64  `json:"app_id"`
	ClientID string `json:"client_id,omitempty"`
	Slug     string `json:"slug"`
	Name     string `json:"name,omitempty"`
	HTMLURL  string `json:"html_url,omitempty"`
}

// Store keeps app.json and private-key.pem (both 0600) in Dir.
type Store struct{ Dir string }

func (s Store) cfgPath() string { return filepath.Join(s.Dir, "app.json") }
func (s Store) keyPath() string { return filepath.Join(s.Dir, "private-key.pem") }

var ErrNotConfigured = errors.New("no GitHub App is set up yet (Bergfried → Settings → GitHub)")

// Load returns the configuration and the private key.
func (s Store) Load() (Config, *rsa.PrivateKey, error) {
	var c Config
	b, err := os.ReadFile(s.cfgPath())
	if err != nil {
		return c, nil, ErrNotConfigured
	}
	if err := json.Unmarshal(b, &c); err != nil || c.AppID == 0 {
		return c, nil, ErrNotConfigured
	}
	pb, err := os.ReadFile(s.keyPath())
	if err != nil {
		return c, nil, ErrNotConfigured
	}
	k, err := ParseKey(pb)
	if err != nil {
		return c, nil, err
	}
	return c, k, nil
}

// Configured reports whether an app and its key are stored.
func (s Store) Configured() bool { _, _, err := s.Load(); return err == nil }

// ParseKey reads a PEM private key (GitHub delivers PKCS#1 "RSA PRIVATE KEY"; PKCS#8 is accepted too).
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil {
		return nil, errors.New("the private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errors.New("the private key could not be read")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an RSA key")
	}
	return rk, nil
}

// Save writes key and configuration (directory 0700, files 0600).
func (s Store) Save(c Config, pemBytes []byte) error {
	if c.AppID == 0 || c.Slug == "" {
		return errors.New("incomplete app data")
	}
	if _, err := ParseKey(pemBytes); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Dir, 0o700); err != nil {
		return err
	}
	write := func(p string, b []byte) error {
		tmp := p + ".new"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, p)
	}
	if err := write(s.keyPath(), pemBytes); err != nil {
		return err
	}
	cb, _ := json.MarshalIndent(c, "", "  ")
	return write(s.cfgPath(), cb)
}

// Delete forgets the app on this Mac (the app itself stays on GitHub; delete it there).
func (s Store) Delete() {
	_ = os.Remove(s.cfgPath())
	_ = os.Remove(s.keyPath())
}

const apiVersion = "2022-11-28"

// Client talks to the GitHub API as the app (JWT) or as an installation (token).
type Client struct {
	Base  string // https://api.github.com
	HTTP  *http.Client
	Store Store
	Now   func() time.Time

	mu   sync.Mutex
	inst map[string]int64 // owner (lower case) -> installation id
}

func NewClient(s Store) *Client {
	return &Client{Base: "https://api.github.com", HTTP: &http.Client{Timeout: 60 * time.Second}, Store: s, Now: time.Now, inst: map[string]int64{}}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// JWT signs the app JWT (RS256). iat is 60 s in the past (clock drift), exp is 9 minutes ahead (GitHub allows 10).
func (c *Client) JWT() (string, error) {
	cfg, key, err := c.Store.Load()
	if err != nil {
		return "", err
	}
	iss := cfg.ClientID
	if iss == "" {
		iss = fmt.Sprint(cfg.AppID)
	}
	now := c.Now()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	p, _ := json.Marshal(map[string]any{"iss": iss, "iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	in := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return in + "." + b64(sig), nil
}

// APIError is an error response from GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub %d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "bergfried")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		if len(e.Message) > 300 {
			e.Message = e.Message[:300] + "…"
		}
		return &APIError{resp.StatusCode, e.Message}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("response not readable: %w", err)
		}
	}
	return nil
}

// Installation is one place the app is installed (a user or an organisation).
type Installation struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
}

// Installations lists where the app is installed (GET /app/installations, as the app).
func (c *Client) Installations(ctx context.Context) ([]Installation, error) {
	jwt, err := c.JWT()
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := c.do(ctx, "GET", "/app/installations?per_page=100", jwt, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Installation, 0, len(raw))
	for _, r := range raw {
		out = append(out, Installation{r.ID, r.Account.Login})
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// InstallationFor finds the installation that serves owner/repo (GET /repos/{o}/{r}/installation, as the app).
// A 404 means the app is not installed on that repository.
func (c *Client) InstallationFor(ctx context.Context, owner, repo string) (int64, error) {
	if !nameRe.MatchString(owner) || !nameRe.MatchString(repo) {
		return 0, errors.New("invalid repository name")
	}
	jwt, err := c.JWT()
	if err != nil {
		return 0, err
	}
	var d struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, "GET", "/repos/"+owner+"/"+repo+"/installation", jwt, nil, &d); err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == 404 {
			return 0, ErrNotInstalled
		}
		return 0, err
	}
	c.mu.Lock()
	c.inst[strings.ToLower(owner)] = d.ID
	c.mu.Unlock()
	return d.ID, nil
}

var ErrNotInstalled = errors.New("the GitHub App is not installed on this repository (install it on GitHub and select the repository)")

// Token is an installation access token. It expires after one hour and can be revoked.
type Token struct {
	Token     string
	ExpiresAt time.Time
}

// Mint creates an installation token narrowed to repos (names without owner) and permissions (name -> read|write).
func (c *Client) Mint(ctx context.Context, installation int64, repos []string, perms map[string]string) (Token, error) {
	if len(repos) == 0 || len(perms) == 0 {
		return Token{}, errors.New("a token needs at least one repository and one permission")
	}
	jwt, err := c.JWT()
	if err != nil {
		return Token{}, err
	}
	var d struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": repos, "permissions": perms}
	if err := c.do(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation), jwt, body, &d); err != nil {
		return Token{}, err
	}
	if d.Token == "" {
		return Token{}, errors.New("GitHub returned no token")
	}
	return Token{d.Token, d.ExpiresAt}, nil
}

// Revoke invalidates an installation token (DELETE /installation/token, authenticated with that token). A 401 means it is already invalid.
func (c *Client) Revoke(ctx context.Context, token string) error {
	err := c.do(ctx, "DELETE", "/installation/token", token, nil, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 401 {
		return nil
	}
	return err
}
