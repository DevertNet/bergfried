package sandbox

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// openCodeRegistry is a variable so tests can point it at a local server.
var openCodeRegistry = "https://registry.npmjs.org"

// openCodeHTTP is separate from http.DefaultClient to bound the 100+ MB download.
var openCodeHTTP = &http.Client{Timeout: 15 * time.Minute}

type npmDist struct {
	Dist struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

func openCodeArch() (string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64", nil
	case "amd64":
		return "x64", nil
	}
	return "", fmt.Errorf("OpenCode: unsupported daemon architecture %s", runtime.GOARCH)
}

// downloadOpenCode fetches the pinned package on the Mac (not in the VM), verifies its
// SHA-512 integrity from the npm metadata and returns the path of the .tgz.
func downloadOpenCode(ctx context.Context, version string) (string, error) {
	arch, err := openCodeArch()
	if err != nil {
		return "", err
	}
	pkg := "cli-linux-" + arch
	metaURL := fmt.Sprintf("%s/@opencode%%2f%s/%s", openCodeRegistry, pkg, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := openCodeHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenCode: npm metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenCode: npm metadata for %s %s: HTTP %d", pkg, version, resp.StatusCode)
	}
	var meta npmDist
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return "", fmt.Errorf("OpenCode: npm metadata: %w", err)
	}
	if meta.Dist.Tarball == "" {
		return "", fmt.Errorf("OpenCode: npm metadata has no tarball URL")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, meta.Dist.Tarball, nil)
	if err != nil {
		return "", err
	}
	tresp, err := openCodeHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("OpenCode: download: %w", err)
	}
	defer tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenCode: download %s: HTTP %d", meta.Dist.Tarball, tresp.StatusCode)
	}
	f, err := os.CreateTemp("", "opencode-*.tgz")
	if err != nil {
		return "", err
	}
	name := f.Name()
	h := sha512.New()
	if _, err := io.Copy(io.MultiWriter(f, h), tresp.Body); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("OpenCode: download: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if want := meta.Dist.Integrity; strings.HasPrefix(want, "sha512-") {
		got := "sha512-" + base64.StdEncoding.EncodeToString(h.Sum(nil))
		if got != want {
			os.Remove(name)
			return "", fmt.Errorf("OpenCode: checksum mismatch for %s %s", pkg, version)
		}
	}
	return name, nil
}

// openCodeCheckScript reports whether a usable v2 binary is already installed.
func openCodeCheckScript() string {
	return `OC="$HOME/.opencode/bin/opencode"
if [ -x "$OC" ] && "$OC" --version 2>/dev/null | grep -qE '(^| )v?2[.]'; then echo yes; fi`
}

// openCodeInstallScript unpacks the uploaded tarball and installs the binary. It runs in the
// VM and needs only tar/install (no network).
func openCodeInstallScript() string {
	return `set -e
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar -xzf /tmp/opencode.tgz -C "$tmp" || { echo "OpenCode: could not extract the download" >&2; exit 1; }
mkdir -p "$HOME/.opencode/bin"
install -m755 "$tmp/package/bin/opencode" "$HOME/.opencode/bin/opencode.new" || { echo "OpenCode: binary not found in the package" >&2; exit 1; }
mv -f "$HOME/.opencode/bin/opencode.new" "$HOME/.opencode/bin/opencode"
rm -f /tmp/opencode.tgz
for f in "$HOME/.profile" "$HOME/.bashrc"; do
  if [ -f "$f" ] && ! grep -qF '.opencode/bin' "$f"; then echo 'export PATH="$HOME/.opencode/bin:$PATH"' >> "$f"; fi
done
[ -f "$HOME/.profile" ] || echo 'export PATH="$HOME/.opencode/bin:$PATH"' > "$HOME/.profile"`
}
