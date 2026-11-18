package ghapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Permissions the app asks for. No Workflows and no Administration permission: the assistant must neither change
// .github/workflows nor create repositories. Narrower tokens are minted per grant (see gitaccess).
var DefaultPermissions = map[string]string{
	"contents":      "write",
	"pull_requests": "write",
	"actions":       "write",
	"metadata":      "read",
}

// Manifest builds the GitHub App manifest. The webhook is left out on purpose (Bergfried never receives events).
func Manifest(name, redirectURL string) map[string]any {
	return map[string]any{
		"name":                name,
		"url":                 "https://github.com/DevertNet/bergfried",
		"redirect_url":        redirectURL,
		"public":              false,
		"default_permissions": DefaultPermissions,
	}
}

// Setup runs the manifest flow: a short-lived loopback web server on 127.0.0.1 serves a form that sends the manifest
// to GitHub, receives the redirect with the one-time code and exchanges it for the app id and the private key.
type Setup struct {
	Store   Store
	Web     string // https://github.com (tests: local stand-in)
	API     string // https://api.github.com
	HTTP    *http.Client
	Timeout time.Duration
	Log     func(event, detail string)

	mu     sync.Mutex
	active *flow
}

type flow struct {
	state string
	srv   *http.Server
	ln    net.Listener
	done  chan struct{}
	err   error
	name  string
}

func NewSetup(s Store) *Setup {
	return &Setup{Store: s, Web: "https://github.com", API: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}, Timeout: 15 * time.Minute}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Setup) log(ev, d string) {
	if s.Log != nil {
		s.Log(ev, d)
	}
}

// Start opens the loopback server and returns the local URL to open in the browser. An earlier unfinished flow is replaced.
func (s *Setup) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		s.active.srv.Close()
		s.active = nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f := &flow{state: randHex(16), ln: ln, done: make(chan struct{}), name: "Bergfried-" + randHex(3)}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /start", func(w http.ResponseWriter, r *http.Request) { s.serveStart(w, f, redirect) })
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) { s.serveCallback(w, r, f) })
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.srv.Serve(ln) }()
	go func() {
		select {
		case <-f.done:
		case <-time.After(s.Timeout):
		}
		_ = f.srv.Close()
	}()
	s.active = f
	s.log("githubapp.setup.start", "")
	return fmt.Sprintf("http://127.0.0.1:%d/start", port), nil
}

// serveStart: auto-submitting form (GitHub expects a POST with the manifest in the field "manifest").
func (s *Setup) serveStart(w http.ResponseWriter, f *flow, redirect string) {
	mb, _ := json.Marshal(Manifest(f.name, redirect))
	action := s.Web + "/settings/apps/new?state=" + url.QueryEscape(f.state)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Bergfried</title>
<body style="font-family:-apple-system,sans-serif;max-width:32em;margin:3em auto">
<p>Forwarding to GitHub to create your Bergfried app …</p>
<form id="f" method="post" action="%s"><input type="hidden" name="manifest" value="%s"><noscript><button>Continue to GitHub</button></noscript></form>
<script>document.getElementById("f").submit()</script></body>`, html.EscapeString(action), html.EscapeString(string(mb)))
}

func page(w http.ResponseWriter, code int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Bergfried</title><body style="font-family:-apple-system,sans-serif;max-width:32em;margin:3em auto"><h2>%s</h2>%s</body>`, html.EscapeString(title), body)
}

func (s *Setup) serveCallback(w http.ResponseWriter, r *http.Request, f *flow) {
	q := r.URL.Query()
	if q.Get("state") != f.state || q.Get("code") == "" {
		page(w, 400, "Invalid request", "<p>The state does not match. Start the setup again in Bergfried.</p>")
		return
	}
	cfg, pem, err := s.convert(r.Context(), q.Get("code"))
	if err == nil {
		err = s.Store.Save(cfg, pem)
	}
	if err != nil {
		f.err = err
		s.log("githubapp.setup.failed", err.Error())
		page(w, 502, "Setup failed", "<p>"+html.EscapeString(err.Error())+"</p>")
		return
	}
	s.log("githubapp.setup.done", cfg.Slug)
	inst := fmt.Sprintf("%s/apps/%s/installations/new", s.Web, url.PathEscape(cfg.Slug))
	page(w, 200, "Your Bergfried app is ready",
		`<p>Now install it on GitHub and select <b>only the repositories</b> your assistants may use:</p><p><a href="`+html.EscapeString(inst)+`">Install the app</a></p><p>Then go back to Bergfried.</p>`)
	close(f.done)
}

// convert exchanges the one-time code (valid for one hour) for the app credentials. Needs no authentication.
func (s *Setup) convert(ctx context.Context, code string) (Config, []byte, error) {
	c := &Client{Base: s.API, HTTP: s.HTTP, Store: s.Store, Now: time.Now}
	var d struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		ClientID string `json:"client_id"`
		HTMLURL  string `json:"html_url"`
		PEM      string `json:"pem"`
	}
	if err := c.do(ctx, "POST", "/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &d); err != nil {
		return Config{}, nil, err
	}
	if d.ID == 0 || d.PEM == "" || d.Slug == "" {
		return Config{}, nil, errors.New("GitHub returned incomplete app data")
	}
	return Config{AppID: d.ID, ClientID: d.ClientID, Slug: d.Slug, Name: d.Name, HTMLURL: d.HTMLURL}, []byte(d.PEM), nil
}

// Close stops a running flow.
func (s *Setup) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		s.active.srv.Close()
		s.active = nil
	}
}
