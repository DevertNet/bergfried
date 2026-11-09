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
		{"1.2.3", "1.2.3-rc1", true}, {"1.2.3-rc1", "1.2.2", true}, {"garbage", "1.0.0", false},
		{"0.1.1-dev.17", "0.1.1-dev.3", true}, {"0.1.1-dev.3", "0.1.1-dev.17", false}, {"0.1.1-dev.9", "0.1.1-dev.10", false},
		{"0.1.1-dev.3", "0.1.0", true}, {"0.1.1", "0.1.1-dev.17", true}, {"0.1.1-dev.17", "0.1.1", false}, {"0.1.1-dev.17", "0.1.1-dev.17", false},
		{"0.2.0-dev.1", "0.1.9", true}, {"0.1.1-dev.1", "0.1.1-dev.1.1", false}, {"0.1.1-alpha", "0.1.1-dev.1", false},
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

type rel struct {
	tag      string
	draft    bool
	pre      bool
	noAssets bool
	files    []file // default: a valid bundle of that version
}

type fx struct {
	svc      *Service
	srv      *httptest.Server
	zips     map[string][]byte
	sums     map[string]string
	rels     []rel
	apiFail  bool
	launched []string
	backedUp bool
}

// newFx serves the given releases (newest first, like GitHub) and builds a service pointing at them.
func newFx(t *testing.T, current string, rels ...rel) *fx {
	f := &fx{rels: rels, zips: map[string][]byte{}, sums: map[string]string{}}
	for _, r := range rels {
		files := r.files
		if files == nil {
			files = bundleFiles(strings.TrimPrefix(r.tag, "v"))
		}
		z := makeZip(t, files)
		f.zips[r.tag] = z
		sum := sha256.Sum256(z)
		f.sums[r.tag] = hex.EncodeToString(sum[:]) + "  " + AssetName(r.tag) + "\n"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases", func(w http.ResponseWriter, r *http.Request) {
		if f.apiFail {
			w.WriteHeader(500)
			return
		}
		var items []string
		for _, rl := range f.rels {
			assets := "[]"
			if !rl.noAssets {
				assets = fmt.Sprintf(`[{"name":%q,"browser_download_url":"%s/dl/%s/zip","size":1},{"name":"SHA256SUMS","browser_download_url":"%s/dl/%s/sums","size":1}]`,
					AssetName(rl.tag), f.srv.URL, rl.tag, f.srv.URL, rl.tag)
			}
			items = append(items, fmt.Sprintf(`{"tag_name":%q,"body":"notes for %s","html_url":"https://example/rel","draft":%v,"prerelease":%v,"assets":%s}`, rl.tag, rl.tag, rl.draft, rl.pre, assets))
		}
		fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/"), "/")
		if len(parts) != 2 {
			w.WriteHeader(404)
			return
		}
		if parts[1] == "zip" {
			w.Write(f.zips[parts[0]])
		} else {
			w.Write([]byte(f.sums[parts[0]]))
		}
	})
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
		return nil
	}
	return f
}

func stable(tag string) rel { return rel{tag: tag} }
func dev(tag string) rel    { return rel{tag: tag, pre: true} }

func TestCheckStableChannel(t *testing.T) {
	f := newFx(t, "0.1.0", dev("v0.2.0-dev.9"), stable("v0.1.5"), stable("v0.1.0"))
	info := f.svc.Check(context.Background())
	if !info.Available || info.Latest != "0.1.5" || info.Channel != Stable || info.Notes != "notes for v0.1.5" || !info.Updatable || info.Error != "" {
		t.Fatalf("stable must ignore dev pre-releases: %+v", info)
	}
	if f.svc.Last().Latest != "0.1.5" {
		t.Fatal("Last() must return the cached answer")
	}
	f.svc.Current = "0.1.5"
	if info := f.svc.Check(context.Background()); info.Available {
		t.Fatalf("same version must not be offered: %+v", info)
	}
}

func TestCheckDevChannel(t *testing.T) {
	f := newFx(t, "0.1.0", dev("v0.1.1-dev.17"), dev("v0.1.1-dev.3"), stable("v0.1.0"))
	if err := f.svc.SetChannel(Dev); err != nil {
		t.Fatal(err)
	}
	info := f.svc.Check(context.Background())
	if !info.Available || info.Latest != "0.1.1-dev.17" || info.Channel != Dev {
		t.Fatalf("dev must pick the highest dev build: %+v", info)
	}
	// a stable release newer than the dev builds wins on the dev channel as well
	f.rels = append([]rel{stable("v0.1.1")}, f.rels...)
	if info := f.svc.Check(context.Background()); info.Latest != "0.1.1" {
		t.Fatalf("stable 0.1.1 is newer than 0.1.1-dev.17: %+v", info)
	}
	// running a newer dev build than anything published: nothing to offer, no downgrade
	f.svc.Current = "0.2.0-dev.1"
	if info := f.svc.Check(context.Background()); info.Available {
		t.Fatalf("no downgrade: %+v", info)
	}
}

func TestChannelFiltering(t *testing.T) {
	for _, c := range []struct {
		channel, tag string
		draft, want  bool
	}{
		{Stable, "v1.0.0", false, true}, {Stable, "v1.0.0-dev.2", false, false}, {Stable, "v1.0.0-rc1", false, false},
		{Dev, "v1.0.0", false, true}, {Dev, "v1.0.0-dev.2", false, true}, {Dev, "v1.0.0-rc1", false, false},
		{Dev, "v1.0.0-dev.x", false, false}, {Dev, "v1.0.0", true, false}, {Stable, "garbage", false, false},
	} {
		if got := acceptable(c.channel, c.tag, c.draft); got != c.want {
			t.Errorf("acceptable(%s,%s,draft=%v)=%v, want %v", c.channel, c.tag, c.draft, got, c.want)
		}
	}
}

func TestSetChannelPersistsAndValidates(t *testing.T) {
	f := newFx(t, "0.1.0", stable("v0.2.0"))
	if f.svc.Channel() != Stable {
		t.Fatal("default must be stable")
	}
	if err := f.svc.SetChannel("beta"); err == nil {
		t.Fatal("unknown channel must be refused")
	}
	f.svc.Check(context.Background())
	if err := f.svc.SetChannel(Dev); err != nil {
		t.Fatal(err)
	}
	if f.svc.Last().Checked != 0 {
		t.Fatal("switching the channel must drop the cached answer")
	}
	// another service on the same home sees the setting; file permissions are private
	other := New("o/r", "0.1.0", f.svc.Home)
	if other.Channel() != Dev {
		t.Fatalf("setting not persisted: %s", other.Channel())
	}
	fi, err := os.Stat(filepath.Join(f.svc.Home, "settings.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings.json: %v %v", fi, err)
	}
	// a hand-edited or broken file falls back to stable
	_ = os.WriteFile(filepath.Join(f.svc.Home, "settings.json"), []byte("{nonsense"), 0o600)
	if New("o/r", "0.1.0", f.svc.Home).Channel() != Stable {
		t.Fatal("broken settings must fall back to stable")
	}
}

func TestCheckSkipsReleaseWithoutAssets(t *testing.T) {
	f := newFx(t, "0.1.0", rel{tag: "v0.3.0", noAssets: true}, stable("v0.2.0"))
	info := f.svc.Check(context.Background())
	if info.Available || !strings.Contains(info.Error, "lacks") {
		t.Fatalf("an incomplete newest release must be reported, not silently skipped: %+v", info)
	}
}

func TestCheckAPIFailureKeepsLastAnswer(t *testing.T) {
	f := newFx(t, "0.1.0", stable("v0.2.0"))
	f.svc.Check(context.Background())
	f.apiFail = true
	info := f.svc.Check(context.Background())
	if info.Error == "" || info.Latest != "0.2.0" || !info.Available {
		t.Fatalf("%+v", info)
	}
}

func TestCheckWithoutAnyRelease(t *testing.T) {
	f := newFx(t, "0.1.0", stable("v0.2.0"))
	f.svc.API = f.srv.URL + "/nothing-here" // 404 like a repository without releases
	info := f.svc.Check(context.Background())
	if info.Available || info.Error != "" || info.Checked == 0 {
		t.Fatalf("a missing release is not an error: %+v", info)
	}
}

func TestCheckNotFromBundle(t *testing.T) {
	f := newFx(t, "0.1.0", stable("v0.2.0"))
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
	f := newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
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
	f := newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
	f.sums["v0.2.0"] = strings.Repeat("0", 64) + "  " + AssetName("v0.2.0") + "\n"
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash mismatch must be refused: %v", err)
	}
	// sums without entry
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
	f.sums["v0.2.0"] = strings.Repeat("a", 64) + "  other.zip\n"
	if err := stageErr(t, f); err == nil {
		t.Fatal("missing SHA256SUMS entry must be refused")
	}
	// bundle with a different version than the tag
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.9.9")})
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("version mismatch: %v", err)
	}
	// wrong bundle id
	files := bundleFiles("0.2.0")
	files[0].data = strings.Replace(files[0].data, BundleID, "com.evil.app", 1)
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: files})
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "identifier") {
		t.Fatalf("bundle id: %v", err)
	}
	// missing daemon / not executable
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")[:3]})
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete bundle: %v", err)
	}
	files = bundleFiles("0.2.0")
	files[2].mode = 0o644
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: files})
	if err := stageErr(t, f); err == nil {
		t.Fatal("non-executable daemon must be refused")
	}
	// zip slip
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0", file{"../../evil.txt", "x", 0o644})})
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("zip slip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.Home, "evil.txt")); err == nil {
		t.Fatal("zip slip wrote a file")
	}
	// symlink entries
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0", file{"Bergfried.app/link", "/etc/passwd", os.ModeSymlink | 0o777})})
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink: %v", err)
	}
	// signature check failure
	f = newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
	f.svc.Verify = func(string) error { return errors.New("bad signature") }
	if err := stageErr(t, f); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("signature: %v", err)
	}
}

func TestInstallBacksUpThenLaunches(t *testing.T) {
	f := newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
	f.svc.Launch = func(script string, args ...string) error {
		if !f.backedUp {
			t.Error("the database must be backed up before the helper starts")
		}
		f.launched = append([]string{script}, args...)
		return nil
	}
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
	f := newFx(t, "0.1.0", rel{tag: "v0.2.0", files: bundleFiles("0.2.0")})
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

func runSwap(t *testing.T, failCopy bool, restoreDB bool) (log, calls string, tree string) {
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
	args := []string{script, app, newApp, prev, LaunchLabel, BundleID, logf}
	dbT := filepath.Join(root, "bergfried.db")
	_ = os.WriteFile(dbT, []byte("broken"), 0o600)
	_ = os.WriteFile(dbT+"-wal", []byte("w"), 0o600)
	_ = os.WriteFile(filepath.Join(root, "backup.db"), []byte("good"), 0o600)
	if restoreDB {
		args = append(args, filepath.Join(root, "backup.db"), dbT)
	}
	cmd := exec.Command("/bin/bash", args...)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil && !failCopy {
		t.Fatalf("%v: %s", err, out)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	_, walErr := os.Stat(dbT + "-wal")
	return read(logf), read(callLog), fmt.Sprintf("app=%s prev=%s db=%s wal=%v", read(filepath.Join(app, "Contents", "version")), read(filepath.Join(prev, "Contents", "version")), read(dbT), walErr == nil)
}

func TestSwapScriptReplacesBundleAndRestarts(t *testing.T) {
	log, calls, tree := runSwap(t, false, false)
	if tree != "app=new prev=old db=broken wal=true" {
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
	log, calls, tree := runSwap(t, true, false)
	if !strings.HasPrefix(tree, "app=old") {
		t.Fatalf("the old bundle must be restored: %s\nlog: %s", tree, log)
	}
	if !strings.Contains(log, "restoring the old one") || !strings.Contains(calls, "open ") {
		t.Fatalf("log: %s\ncalls: %s", log, calls)
	}
}

func TestSwapScriptRestoresDatabaseWhenAsked(t *testing.T) {
	log, _, tree := runSwap(t, false, true)
	if tree != "app=new prev=old db=good wal=false" {
		t.Fatalf("the database must be restored and the old WAL removed: %s\n%s", tree, log)
	}
	if !strings.Contains(log, "database restored") {
		t.Fatal(log)
	}
}

// ---- rollback ----

func rollbackFx(t *testing.T) *fx {
	f := newFx(t, "0.2.0", stable("v0.3.0"))
	// state after an update 0.1.0 -> 0.2.0: previous bundle + database backup of 0.1.0
	prev := filepath.Join(f.svc.Home, "update", "Bergfried.app.previous")
	for _, fl := range bundleFiles("0.1.0") {
		p := filepath.Join(f.svc.Home, "update", strings.Replace(fl.name, "Bergfried.app", "Bergfried.app.previous", 1))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(fl.data), fl.mode); err != nil {
			t.Fatal(err)
		}
	}
	_ = prev
	bdir := filepath.Join(f.svc.Home, "update", "backup-0.1.0")
	_ = os.MkdirAll(bdir, 0o700)
	_ = os.WriteFile(filepath.Join(bdir, "bergfried.db"), []byte("old db"), 0o600)
	return f
}

func TestRollbackLaunchesHelperWithBackup(t *testing.T) {
	f := rollbackFx(t)
	if err := f.svc.Rollback(RollbackOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.launched) != 9 {
		t.Fatalf("helper args: %v", f.launched)
	}
	if !strings.HasSuffix(f.launched[2], "rollback-src") || !strings.HasSuffix(f.launched[3], "Bergfried.app.replaced") ||
		!strings.HasSuffix(f.launched[7], "backup-0.1.0/bergfried.db") || !strings.HasSuffix(f.launched[8], "bergfried.db") {
		t.Fatalf("helper args: %v", f.launched)
	}
	if _, err := os.Stat(filepath.Join(f.launched[2], "Contents", "Resources", "bergfriedd")); err != nil {
		t.Fatalf("the rollback source must be a copy of the previous bundle: %v", err)
	}
	// the previous bundle itself stays untouched (the helper works on a copy)
	if _, err := os.Stat(filepath.Join(f.svc.Home, "update", "Bergfried.app.previous", "Contents", "Info.plist")); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackWithoutDatabase(t *testing.T) {
	f := rollbackFx(t)
	if err := f.svc.Rollback(RollbackOptions{KeepDatabase: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.launched) != 7 {
		t.Fatalf("no database arguments expected: %v", f.launched)
	}
}

func TestRollbackRefusals(t *testing.T) {
	// nothing to roll back to
	f := newFx(t, "0.2.0", stable("v0.3.0"))
	if err := f.svc.Rollback(RollbackOptions{}); err == nil || !strings.Contains(err.Error(), "previous version") {
		t.Fatalf("%v", err)
	}
	// previous bundle but no database backup
	f = rollbackFx(t)
	_ = os.RemoveAll(filepath.Join(f.svc.Home, "update", "backup-0.1.0"))
	if err := f.svc.Rollback(RollbackOptions{}); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("%v", err)
	}
	if f.launched != nil {
		t.Fatal("nothing may start when the backup is missing")
	}
	// a damaged previous bundle
	f = rollbackFx(t)
	_ = os.Remove(filepath.Join(f.svc.Home, "update", "Bergfried.app.previous", "Contents", "Resources", "bergfriedd"))
	if err := f.svc.Rollback(RollbackOptions{}); err == nil || !strings.Contains(err.Error(), "not usable") {
		t.Fatalf("%v", err)
	}
	// outside an app bundle
	f = rollbackFx(t)
	f.svc.BundlePath = func() (string, error) { return "", errors.New("not running from an app bundle") }
	if err := f.svc.Rollback(RollbackOptions{}); err == nil {
		t.Fatal("must refuse outside a bundle")
	}
}
