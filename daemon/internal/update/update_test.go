package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.4", "1.2.3", true}, {"1.3.0", "1.2.9", true}, {"2.0.0", "1.9.9", true},
		{"1.2.3", "1.2.3", false}, {"1.2.2", "1.2.3", false},
		{"1.0.0", "0.0.0-dev", true}, {"0.0.1", "dev", true},
		{"1.2.3", "1.2.3-rc1", true}, {"1.2.3-rc1", "1.2.2", false}, {"garbage", "1.0.0", false},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q,%q)=%v, want %v", c.latest, c.current, got, c.want)
		}
	}
	if _, _, ok := ParseVersion("1.2"); ok {
		t.Fatal("1.2 is not a version")
	}
}

// ---- fixtures ----

type file struct {
	name string
	data string
	mode os.FileMode
}

func bundleFiles(version string, extra ...file) []file {
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist><dict><key>CFBundleIdentifier</key><string>%s</string><key>CFBundleShortVersionString</key><string>%s</string></dict></plist>`, BundleID, version)
	fs := []file{
		{"Bergfried.app/Contents/Info.plist", plist, 0o644},
		{"Bergfried.app/Contents/MacOS/Bergfried", "app", 0o755},
		{"Bergfried.app/Contents/Resources/bergfriedd", "daemon", 0o755},
		{"Bergfried.app/Contents/Resources/bergfried-mcp", "mcp", 0o755},
	}
	return append(fs, extra...)
}

func makeZip(t *testing.T, files []file) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		h.SetMode(f.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(f.data))
	}
	_ = zw.Close()
	return buf.Bytes()
}

type fx struct {
	svc      *Service
	srv      *httptest.Server
	zip      []byte
	sums     string
	tag      string
	draft    bool
	apiFail  bool
	mu       sync.Mutex
	calls    []string
	launched []string
	backedUp bool
}

func newFx(t *testing.T, current, tag string, files []file) *fx {
	f := &fx{tag: tag, zip: makeZip(t, files)}
	sum := sha256.Sum256(f.zip)
	f.sums = hex.EncodeToString(sum[:]) + "  " + AssetName(tag) + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if f.apiFail {
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q,"body":"notes","html_url":"https://example/rel","draft":%v,"prerelease":false,"assets":[
		 {"name":%q,"browser_download_url":"%s/dl/zip","size":1},{"name":"SHA256SUMS","browser_download_url":"%s/dl/sums","size":1}]}`,
			f.tag, f.draft, AssetName(f.tag), f.srv.URL, f.srv.URL)
	})
	mux.HandleFunc("/dl/zip", func(w http.ResponseWriter, r *http.Request) { w.Write(f.zip) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(f.sums)) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	home := t.TempDir()
	f.svc = New("o/r", current, home)
	f.svc.API = f.srv.URL
	f.svc.BundlePath = func() (string, error) { return filepath.Join(home, "Applications", "Bergfried.app"), nil }
	f.svc.Verify = func(string) error { return nil }
	f.svc.Backup = func(dst string) error { f.backedUp = true; return os.WriteFile(dst, []byte("db"), 0o600) }
	f.svc.Launch = func(script string, args ...string) error {
		f.launched = append([]string{script}, args...)
		if !f.backedUp {
			t.Error("the database must be backed up before the helper starts")
		}
		return nil
	}
	return f
}

func TestCheck(t *testing.T) {
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	info := f.svc.Check(context.Background())
	if !info.Available || info.Latest != "0.2.0" || info.Notes != "notes" || !info.Updatable || info.Error != "" {
		t.Fatalf("%+v", info)
	}
	if f.svc.Last().Latest != "0.2.0" {
		t.Fatal("Last() must return the cached answer")
	}
	// same version: nothing to do
	f.svc.Current = "0.2.0"
	if info := f.svc.Check(context.Background()); info.Available {
		t.Fatalf("same version must not be offered: %+v", info)
	}
	// drafts are ignored
	f.svc.Current, f.draft = "0.1.0", true
	if info := f.svc.Check(context.Background()); info.Available {
		t.Fatal("draft must not be offered")
	}
	// API failure keeps the last good answer visible but reports the error
	f.draft, f.apiFail = false, false
	f.svc.Check(context.Background())
	f.apiFail = true
	info = f.svc.Check(context.Background())
	if info.Error == "" || info.Latest != "0.2.0" || !info.Available {
		t.Fatalf("%+v", info)
	}
}

func TestCheckNotFromBundle(t *testing.T) {
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	f.svc.BundlePath = func() (string, error) { return "", errors.New("not running from an app bundle") }
	info := f.svc.Check(context.Background())
	if info.Updatable || !strings.Contains(info.Reason, "app bundle") || !info.Available {
		t.Fatalf("%+v", info)
	}
	if err := f.svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "app bundle") {
		t.Fatalf("install outside a bundle must fail: %v", err)
	}
}

func stageErr(t *testing.T, f *fx) error {
	info := f.svc.Check(context.Background())
	if !info.Available {
		t.Fatalf("setup: %+v", info)
	}
	_, err := f.svc.Stage(context.Background(), info)
	return err
}

func TestStageSuccess(t *testing.T) {
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	info := f.svc.Check(context.Background())
	st, err := f.svc.Stage(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(st.App, "Contents", "Resources", "bergfriedd")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(st.App, "Contents", "MacOS", "Bergfried"))
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatal("executable bit lost")
	}
}

func TestStageRejects(t *testing.T) {
	// wrong hash
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	f.sums = strings.Repeat("0", 64) + "  " + AssetName("v0.2.0") + "\n"
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash mismatch must be refused: %v", err)
	}
	// sums without entry
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	f.sums = strings.Repeat("a", 64) + "  other.zip\n"
	if err := stageErr(t, f); err == nil {
		t.Fatal("missing SHA256SUMS entry must be refused")
	}
	// bundle with a different version than the tag
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.9.9"))
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version mismatch: %v", err)
	}
	// wrong bundle id
	files := bundleFiles("0.2.0")
	files[0].data = strings.Replace(files[0].data, BundleID, "com.evil.app", 1)
	f = newFx(t, "0.1.0", "v0.2.0", files)
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("bundle id: %v", err)
	}
	// missing daemon / not executable
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0")[:3])
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete bundle: %v", err)
	}
	files = bundleFiles("0.2.0")
	files[2].mode = 0o644
	f = newFx(t, "0.1.0", "v0.2.0", files)
	if err := stageErr(t, f); err == nil {
		t.Fatal("non-executable daemon must be refused")
	}
	// zip slip
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0", file{"../../evil.txt", "x", 0o644}))
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("zip slip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.Home, "evil.txt")); err == nil {
		t.Fatal("zip slip wrote a file")
	}
	// symlink entries
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0", file{"Bergfried.app/link", "/etc/passwd", os.ModeSymlink | 0o777}))
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
	// signature check failure
	f = newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	f.svc.Verify = func(string) error { return errors.New("bad signature") }
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("signature: %v", err)
	}
}

func TestInstallBacksUpThenLaunches(t *testing.T) {
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	if err := f.svc.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.launched) != 7 {
		t.Fatalf("helper args: %v", f.launched)
	}
	app, newApp := f.launched[1], f.launched[2]
	if !strings.HasSuffix(app, "Applications/Bergfried.app") || !strings.HasSuffix(newApp, "unpacked/Bergfried.app") ||
		f.launched[4] != LaunchLabel || f.launched[5] != BundleID {
		t.Fatalf("helper args: %v", f.launched)
	}
	if b, _ := os.ReadFile(filepath.Join(f.svc.Home, "update", "backup-0.1.0", "bergfried.db")); string(b) != "db" {
		t.Fatal("backup missing")
	}
	fi, err := os.Stat(f.launched[0])
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("script: %v %v", fi, err)
	}
	// a second install while the first is pending is refused
	if err := f.svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("second install: %v", err)
	}
}

func TestInstallBackupFailureStopsEverything(t *testing.T) {
	f := newFx(t, "0.1.0", "v0.2.0", bundleFiles("0.2.0"))
	f.svc.Backup = func(string) error { return errors.New("disk full") }
	if err := f.svc.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("%v", err)
	}
	if f.launched != nil {
		t.Fatal("the helper must not start after a failed backup")
	}
	// and it can be retried
	f.svc.Backup = func(dst string) error { f.backedUp = true; return os.WriteFile(dst, []byte("db"), 0o600) }
	if err := f.svc.Install(context.Background()); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
}

// ---- the swap helper, run with stubbed system commands ----

func runSwap(t *testing.T, failCopy bool) (log, calls string, tree string) {
	root := t.TempDir()
	stubs := filepath.Join(root, "stubs")
	_ = os.MkdirAll(stubs, 0o755)
	callLog := filepath.Join(root, "calls.log")
	stub := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"launchctl", "pkill", "open"} {
		stub(c, fmt.Sprintf(`echo "%s $*" >> %q`, c, callLog))
	}
	stub("pgrep", "exit 1") // nothing left running
	if failCopy {
		stub("cp", "exit 1")
	}
	home := filepath.Join(root, "home")
	_ = os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "Library", "LaunchAgents", LaunchLabel+".plist"), []byte("x"), 0o644)
	app := filepath.Join(root, "Applications", "Bergfried.app")
	newApp := filepath.Join(root, "update", "unpacked", "Bergfried.app")
	prev := filepath.Join(root, "update", "Bergfried.app.previous")
	for _, d := range []string{filepath.Join(app, "Contents"), filepath.Join(newApp, "Contents")} {
		_ = os.MkdirAll(d, 0o755)
	}
	_ = os.WriteFile(filepath.Join(app, "Contents", "version"), []byte("old"), 0o644)
	_ = os.WriteFile(filepath.Join(newApp, "Contents", "version"), []byte("new"), 0o644)
	script := filepath.Join(root, "swap.sh")
	_ = os.WriteFile(script, []byte(SwapScript), 0o700)
	logf := filepath.Join(root, "update.log")
	cmd := exec.Command("/bin/bash", script, app, newApp, prev, LaunchLabel, BundleID, logf)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil && !failCopy {
		t.Fatalf("%v: %s", err, out)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	return read(logf), read(callLog), fmt.Sprintf("app=%s prev=%s", read(filepath.Join(app, "Contents", "version")), read(filepath.Join(prev, "Contents", "version")))
}

func TestSwapScriptReplacesBundleAndRestarts(t *testing.T) {
	log, calls, tree := runSwap(t, false)
	if tree != "app=new prev=old" {
		t.Fatalf("tree: %s\nlog: %s", tree, log)
	}
	i := func(s string) int { return strings.Index(calls, s) }
	if i("launchctl bootout") < 0 || i("pkill") < 0 || i("launchctl bootstrap") < 0 || i("launchctl kickstart") < 0 || i("open ") < 0 {
		t.Fatalf("calls:\n%s", calls)
	}
	if !(i("launchctl bootout") < i("launchctl bootstrap") && i("launchctl kickstart") < i("open ")) {
		t.Fatalf("order wrong:\n%s", calls)
	}
	if !strings.Contains(log, "update: done") {
		t.Fatal(log)
	}
}

func TestSwapScriptRollsBackWhenCopyFails(t *testing.T) {
	log, calls, tree := runSwap(t, true)
	if !strings.HasPrefix(tree, "app=old") {
		t.Fatalf("the old bundle must be restored: %s\nlog: %s", tree, log)
	}
	if !strings.Contains(log, "restoring the old one") || !strings.Contains(calls, "open ") {
		t.Fatalf("log: %s\ncalls: %s", log, calls)
	}
}
