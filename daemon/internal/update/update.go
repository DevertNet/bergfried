// Package update installs new Bergfried versions from GitHub releases.
//
// A release has one asset, Bergfried-<version>-macos-arm64.zip (the app bundle, which contains the daemon,
// bergfried-mcp and the VM template), and a SHA256SUMS file. Releases are NOT signed (see docs/UPDATES.md):
// the checks here protect against corrupted downloads and wrong bundles, not against a forged release.
package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	BundleID      = "de.einfach123.bergfried"
	LaunchLabel   = "de.einfach123.bergfried"
	maxZipBytes   = 500 << 20
	maxUnpacked   = 1 << 30
	maxSumsBytes  = 64 << 10
	maxAPIBytes   = 2 << 20
	checkInterval = 6 * time.Hour
)

// Info is what the app shows.
type Info struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	URL       string `json:"url"`
	Checked   int64  `json:"checked_unix"`
	Error     string `json:"error,omitempty"`
	Updatable bool   `json:"updatable"` // false when the daemon does not run from an app bundle
	Reason    string `json:"reason,omitempty"`

	assetURL, sumsURL string
}

type Service struct {
	API     string // default https://api.github.com
	Repo    string // owner/name
	Current string
	Home    string // ~/.bergfried
	HTTP    *http.Client

	// BundlePath returns the path of the running Bergfried.app (error if not running from a bundle).
	BundlePath func() (string, error)
	// Verify checks the code signature of the staged bundle (default: codesign --verify on macOS).
	Verify func(app string) error
	// Backup copies the database to dst.
	Backup func(dst string) error
	// Launch starts the detached helper script.
	Launch func(script string, args ...string) error
	Now    func() time.Time

	mu   sync.Mutex
	last Info
	busy bool
}

func New(repo, current, home string) *Service {
	return &Service{API: "https://api.github.com", Repo: repo, Current: current, Home: home,
		HTTP: &http.Client{Timeout: 10 * time.Minute}, BundlePath: RunningBundle, Verify: CodesignVerify, Launch: LaunchDetached}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---- versions ----

var verRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// ParseVersion parses v1.2.3 / 1.2.3 / 1.2.3-rc1; ok=false for anything else (for example "dev").
func ParseVersion(v string) (n [3]int, pre string, ok bool) {
	m := verRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return n, "", false
	}
	for i := 0; i < 3; i++ {
		n[i], _ = strconv.Atoi(m[i+1])
	}
	return n, m[4], true
}

// Newer reports whether latest is a newer release than current. Unparseable or "-dev" current versions count as older than any release;
// a pre-release latest never counts as newer.
func Newer(latest, current string) bool {
	l, lpre, lok := ParseVersion(latest)
	if !lok || lpre != "" {
		return false
	}
	c, cpre, cok := ParseVersion(current)
	if !cok {
		return true
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre != "" // 1.2.3 is newer than 1.2.3-rc1
}

// ---- check ----

type release struct {
	Tag    string `json:"tag_name"`
	Body   string `json:"body"`
	URL    string `json:"html_url"`
	Draft  bool   `json:"draft"`
	Pre    bool   `json:"prerelease"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// AssetName is the expected asset for a version.
func AssetName(version string) string {
	return "Bergfried-" + strings.TrimPrefix(version, "v") + "-macos-arm64.zip"
}

func (s *Service) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "bergfried-updater")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub answered %d for %s", resp.StatusCode, url)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("answer from %s is larger than %d bytes", url, limit)
	}
	return b, nil
}

func (s *Service) updatable() (bool, string) {
	if s.BundlePath == nil {
		return false, "updates are not available"
	}
	if _, err := s.BundlePath(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// Check asks GitHub for the latest release and caches the result.
func (s *Service) Check(ctx context.Context) Info {
	info := Info{Current: s.Current, Checked: s.now().Unix()}
	info.Updatable, info.Reason = s.updatable()
	b, err := s.get(ctx, s.API+"/repos/"+s.Repo+"/releases/latest", maxAPIBytes)
	if err != nil {
		info.Error = err.Error()
		s.mu.Lock()
		prev := s.last
		s.mu.Unlock()
		if prev.Latest != "" { // keep the last good answer visible
			prev.Error, prev.Checked, prev.Updatable, prev.Reason = info.Error, info.Checked, info.Updatable, info.Reason
			info = prev
		}
		s.mu.Lock()
		s.last = info
		s.mu.Unlock()
		return info
	}
	var rel release
	if err := json.Unmarshal(b, &rel); err != nil || rel.Tag == "" {
		info.Error = "release answer not readable"
		s.mu.Lock()
		s.last = info
		s.mu.Unlock()
		return info
	}
	info.Latest, info.Notes, info.URL = strings.TrimPrefix(rel.Tag, "v"), rel.Body, rel.URL
	if !rel.Draft && !rel.Pre && Newer(rel.Tag, s.Current) {
		want := AssetName(rel.Tag)
		for _, a := range rel.Assets {
			switch a.Name {
			case want:
				info.assetURL = a.URL
			case "SHA256SUMS":
				info.sumsURL = a.URL
			}
		}
		if info.assetURL != "" && info.sumsURL != "" {
			info.Available = true
		} else {
			info.Error = "release " + rel.Tag + " lacks " + want + " or SHA256SUMS"
		}
	}
	s.mu.Lock()
	s.last = info
	s.mu.Unlock()
	return info
}

// Last returns the cached result of the latest check (zero value before the first check).
func (s *Service) Last() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Run checks at start and then every few hours until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		s.Check(cctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- download, verify, stage ----

func parseSums(b []byte) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && len(f[0]) == 64 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out
}

func (s *Service) download(ctx context.Context, url, dst string, limit int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "bergfried-updater")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download answered %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	if n > limit {
		return "", fmt.Errorf("download is larger than %d bytes", limit)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Unzip extracts zip into dir. It refuses paths outside dir, symlinks and oversized archives.
func Unzip(zipPath, dir string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total uint64
	root, _ := filepath.Abs(dir)
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains a symlink (%s)", f.Name)
		}
		total += f.UncompressedSize64
		if total > maxUnpacked {
			return errors.New("archive is too large when unpacked")
		}
		target := filepath.Join(root, f.Name)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry outside the target folder (%s)", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode().Perm()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)+1))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var (
	idRe   = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]*)</string>`)
	verRe2 = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]*)</string>`)
)

// CheckBundle verifies that dir/Bergfried.app is the expected app in the expected version.
func CheckBundle(app, version string) error {
	pl, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return fmt.Errorf("Info.plist missing: %w", err)
	}
	if m := idRe.FindSubmatch(pl); m == nil || string(m[1]) != BundleID {
		return fmt.Errorf("unexpected bundle identifier")
	}
	if m := verRe2.FindSubmatch(pl); m == nil || strings.TrimPrefix(string(m[1]), "v") != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("bundle version does not match the release (%s)", version)
	}
	for _, rel := range []string{"Contents/MacOS/Bergfried", "Contents/Resources/bergfriedd", "Contents/Resources/bergfried-mcp"} {
		fi, err := os.Lstat(filepath.Join(app, rel))
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o100 == 0 {
			return fmt.Errorf("bundle is incomplete: %s", rel)
		}
	}
	return nil
}

// Staged is a verified new bundle waiting to be swapped in.
type Staged struct {
	Version string
	App     string
	Dir     string
}

// Stage downloads and verifies the latest release and unpacks it below Home/update/<version>/.
func (s *Service) Stage(ctx context.Context, info Info) (*Staged, error) {
	if !info.Available || info.assetURL == "" {
		return nil, errors.New("no update available")
	}
	if !regexp.MustCompile(`^[0-9A-Za-z.-]{1,40}$`).MatchString(info.Latest) {
		return nil, errors.New("unusual version string")
	}
	dir := filepath.Join(s.Home, "update", info.Latest)
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sumsRaw, err := s.get(ctx, info.sumsURL, maxSumsBytes)
	if err != nil {
		return nil, fmt.Errorf("SHA256SUMS: %w", err)
	}
	want, ok := parseSums(sumsRaw)[AssetName(info.Latest)]
	if !ok {
		return nil, errors.New("SHA256SUMS has no entry for the app archive")
	}
	zipPath := filepath.Join(dir, AssetName(info.Latest))
	got, err := s.download(ctx, info.assetURL, zipPath, maxZipBytes)
	if err != nil {
		return nil, err
	}
	if got != want {
		_ = os.Remove(zipPath)
		return nil, errors.New("SHA-256 of the download does not match SHA256SUMS – refusing to install")
	}
	unpacked := filepath.Join(dir, "unpacked")
	if err := Unzip(zipPath, unpacked); err != nil {
		return nil, err
	}
	app := filepath.Join(unpacked, "Bergfried.app")
	if err := CheckBundle(app, info.Latest); err != nil {
		return nil, err
	}
	if s.Verify != nil {
		if err := s.Verify(app); err != nil {
			return nil, fmt.Errorf("code signature check failed: %w", err)
		}
	}
	return &Staged{Version: info.Latest, App: app, Dir: dir}, nil
}

// CodesignVerify runs `codesign --verify --deep --strict` (macOS only; elsewhere it is skipped).
func CodesignVerify(app string) error {
	p, err := exec.LookPath("codesign")
	if err != nil {
		return nil
	}
	if out, err := exec.Command(p, "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RunningBundle finds the Bergfried.app the daemon runs from (…/Bergfried.app/Contents/Resources/bergfriedd).
func RunningBundle() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	res := filepath.Dir(exe)
	if filepath.Base(res) != "Resources" || filepath.Base(filepath.Dir(res)) != "Contents" || !strings.HasSuffix(filepath.Dir(filepath.Dir(res)), ".app") {
		return "", errors.New("not running from an app bundle (start the daemon from the app: Settings → Install and start daemon)")
	}
	return filepath.Dir(filepath.Dir(res)), nil
}

// ---- swap ----

// SwapScript is the detached helper. It is a plain bash script so it survives the daemon and app exit; all paths come as arguments.
const SwapScript = `#!/bin/bash
# Bergfried updater helper: replaces the app bundle and restarts daemon and app.
# Arguments: APP NEW PREVIOUS LABEL BUNDLE_ID LOG
set -u
APP="$1"; NEW="$2"; PREV="$3"; LABEL="$4"; BUNDLE_ID="$5"; LOG="$6"
exec >>"$LOG" 2>&1
echo "$(date '+%F %T') update: start"
UIDN=$(id -u)
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

# stop daemon (launchd would restart it) and the app
launchctl bootout "gui/$UIDN/$LABEL" 2>/dev/null
pkill -f "$APP/Contents/Resources/bergfriedd" 2>/dev/null
pkill -f "$APP/Contents/MacOS/Bergfried" 2>/dev/null
for i in $(seq 1 30); do
  if ! pgrep -f "$APP/Contents/(Resources/bergfriedd|MacOS/Bergfried)" >/dev/null 2>&1; then break; fi
  sleep 0.5
done

# swap, with rollback
rm -rf "$PREV"
mkdir -p "$(dirname "$PREV")"
if ! mv "$APP" "$PREV"; then echo "update: cannot move the old bundle away"; exit 1; fi
if ! cp -R "$NEW" "$APP"; then
  echo "update: copying the new bundle failed, restoring the old one"
  rm -rf "$APP"; mv "$PREV" "$APP"
fi

# restart
if [ -f "$PLIST" ]; then
  launchctl bootstrap "gui/$UIDN" "$PLIST" 2>/dev/null
  launchctl kickstart -k "gui/$UIDN/$LABEL" 2>/dev/null
fi
open "$APP"
echo "$(date '+%F %T') update: done"
`

// LaunchDetached starts script with bash in its own session (survives the daemon).
func LaunchDetached(script string, args ...string) error {
	cmd := exec.Command("/bin/bash", append([]string{script}, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Install stages the latest release, backs up the database and launches the swap helper. The daemon (and the app) are stopped by the helper.
func (s *Service) Install(ctx context.Context) error {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return errors.New("an update is already running")
	}
	s.busy = true
	s.mu.Unlock()
	release := func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }

	app, err := s.BundlePath()
	if err != nil {
		release()
		return err
	}
	info := s.Check(ctx)
	if info.Error != "" && !info.Available {
		release()
		return errors.New(info.Error)
	}
	st, err := s.Stage(ctx, info)
	if err != nil {
		release()
		return err
	}
	if s.Backup != nil {
		bdir := filepath.Join(s.Home, "update", "backup-"+s.Current)
		if err := os.MkdirAll(bdir, 0o700); err != nil {
			release()
			return err
		}
		if err := s.Backup(filepath.Join(bdir, "bergfried.db")); err != nil {
			release()
			return fmt.Errorf("database backup failed (nothing changed): %w", err)
		}
	}
	script := filepath.Join(s.Home, "update", "swap.sh")
	if err := os.WriteFile(script, []byte(SwapScript), 0o700); err != nil {
		release()
		return err
	}
	prev := filepath.Join(s.Home, "update", "Bergfried.app.previous")
	log := filepath.Join(s.Home, "update", "update.log")
	if err := s.Launch(script, app, st.App, prev, LaunchLabel, BundleID, log); err != nil {
		release()
		return fmt.Errorf("could not start the update helper: %w", err)
	}
	return nil // busy stays set: the helper stops this process
}
