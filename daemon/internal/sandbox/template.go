package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"gopkg.in/yaml.v3"
)

const MaxTemplateBytes = 64 << 10

// TemplateStore: user-editable copy of the VM template under <Home>/template/.
// The base VM's name derives from the content hash (bergfried-base-<hash>); the revision counts changes (display).
type TemplateStore struct {
	Dir  string // <Home>/template
	Seed string // mitgelieferte Vorlage (Bundle/Repo)
	L    lima.Client
}

func (t TemplateStore) Path() string         { return filepath.Join(t.Dir, "bergfried.yaml") }
func (t TemplateStore) seedHashPath() string { return filepath.Join(t.Dir, "seed.sha256") }
func (t TemplateStore) revPath() string      { return filepath.Join(t.Dir, "revision") }

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

// knownSeedHashes are the SHA-256 sums of earlier bundled templates (git history of vm/bergfried.yaml). A copy that matches one of
// them has never been edited by the user and may be replaced by a newer bundled template (installs from before seed.sha256 existed).
var knownSeedHashes = map[string]bool{
	"4a427a0d168e9f8b35359c89ec6c9f3cf85cab2b471023b95f1ce02cd534a4ae": true,
	"31db9118829810f5b8e62cc13259e382157fb39e96883af807f711c18217be3a": true,
	"aa94f061e79a36cc67f8cbdbc16f89fd0c8aacdc33fff7b88c94566e4ce94e0a": true,
	"2c1ee6d8036bc743130cce0848df97a263124109a94f6272f5b4aaa111d1d2ea": true,
	"41935cfb0f81e8ce57161411168fd48f7dec0cceb39eadcdc354bf2e5aa96c1f": true,
	"48d7e50ad3ae31e43c5fe86a7a6294fb75907ec2c9043e52d6d76acc5e2a790c": true,
	"96e5329da10c7e167456aa1284d9490726e6b1ac7d60cced77317879b9f68ef3": true,
}

func sumHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (t TemplateStore) recordSeed(seed []byte) error {
	return os.WriteFile(t.seedHashPath(), []byte(sumHex(seed)), 0o600)
}

// AdoptSeed replaces the user's copy by the bundled template if the bundled one changed and the copy was never edited
// (it equals the previously adopted bundled template or a known earlier one). It reports whether it replaced the copy.
// A copy edited by the user is left alone; see SeedNewer.
func (t TemplateStore) AdoptSeed() (bool, error) {
	seed, err := os.ReadFile(t.Seed)
	if err != nil {
		return false, err
	}
	cur, err := os.ReadFile(t.Path())
	if err != nil {
		return false, err
	}
	if sumHex(cur) == sumHex(seed) {
		return false, t.recordSeed(seed) // already identical
	}
	last, _ := os.ReadFile(t.seedHashPath())
	if sumHex(cur) != strings.TrimSpace(string(last)) && !knownSeedHashes[sumHex(cur)] {
		return false, nil // edited by the user
	}
	if _, err := CheckTemplate(string(seed)); err != nil {
		return false, fmt.Errorf("bundled template invalid: %w", err)
	}
	if err := os.WriteFile(t.Path(), seed, 0o600); err != nil {
		return false, err
	}
	if err := t.recordSeed(seed); err != nil {
		return true, err
	}
	return true, t.bump()
}

// SeedNewer is true if the bundled template differs from the user's edited copy and has not been adopted yet.
func (t TemplateStore) SeedNewer() bool {
	seed, err1 := os.ReadFile(t.Seed)
	cur, err2 := os.ReadFile(t.Path())
	if err1 != nil || err2 != nil || sumHex(seed) == sumHex(cur) {
		return false
	}
	last, _ := os.ReadFile(t.seedHashPath())
	return sumHex(seed) != strings.TrimSpace(string(last))
}

// Ensure creates the directory (0700) and adopts the bundled template the first time.
func (t TemplateStore) Ensure() error {
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(t.Path()); os.IsNotExist(err) {
		if t.Seed == "" {
			return errors.New("no template present and no bundled one given")
		}
		if err := copyFile(t.Seed, t.Path()); err != nil {
			return err
		}
		if b, err := os.ReadFile(t.Seed); err == nil {
			_ = t.recordSeed(b)
		}
	}
	if _, err := os.Stat(t.revPath()); os.IsNotExist(err) {
		return os.WriteFile(t.revPath(), []byte(strconv.Itoa(TemplateVersion)), 0o600)
	}
	return nil
}

func (t TemplateStore) Rev() int {
	b, err := os.ReadFile(t.revPath())
	if err != nil {
		return TemplateVersion
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 {
		return TemplateVersion
	}
	return n
}

// Hash: short hash of the template content; names the base VM (same content = same base).
func (t TemplateStore) Hash() string {
	b, err := os.ReadFile(t.Path())
	if err != nil {
		return "unbekannt"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:8]
}

func (t TemplateStore) Read() (string, error) {
	b, err := os.ReadFile(t.Path())
	return string(b), err
}

func (t TemplateStore) bump() error {
	return os.WriteFile(t.revPath(), []byte(strconv.Itoa(t.Rev()+1)), 0o600)
}

// Save checks, validates (limactl) and stores; increases the revision if the content changes.
func (t TemplateStore) Save(ctx context.Context, yml string) (warnings []string, err error) {
	warnings, err = CheckTemplate(yml)
	if err != nil {
		return warnings, err
	}
	cur, _ := t.Read()
	if cur == yml {
		return warnings, nil
	}
	tmp := filepath.Join(t.Dir, "candidate.yaml")
	if err := os.WriteFile(tmp, []byte(yml), 0o600); err != nil {
		return warnings, err
	}
	defer os.Remove(tmp)
	if _, err := t.L.R.Run(ctx, "validate", tmp); err != nil {
		return warnings, fmt.Errorf("limactl validate: %s", strings.TrimPrefix(err.Error(), "limactl validate "+tmp+": "))
	}
	if err := os.Rename(tmp, t.Path()); err != nil {
		return warnings, err
	}
	return warnings, t.bump()
}

// Reset restores the bundled template.
func (t TemplateStore) Reset() error {
	b, err := os.ReadFile(t.Seed)
	if err != nil {
		return err
	}
	if _, err := CheckTemplate(string(b)); err != nil {
		return fmt.Errorf("bundled template invalid: %w", err)
	}
	cur, _ := t.Read()
	if cur == string(b) {
		return t.recordSeed(b)
	}
	if err := os.WriteFile(t.Path(), b, 0o600); err != nil {
		return err
	}
	if err := t.recordSeed(b); err != nil {
		return err
	}
	return t.bump()
}

// CheckTemplate enforces the isolation rules. Errors = cannot be saved, warnings = hint.
func CheckTemplate(yml string) (warnings []string, err error) {
	if len(yml) > MaxTemplateBytes {
		return nil, fmt.Errorf("template too large (max. %d KB)", MaxTemplateBytes>>10)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yml), &m); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if m == nil {
		return nil, errors.New("template is empty")
	}
	var errs []string
	if mounts, ok := m["mounts"]; ok {
		if l, ok := mounts.([]any); !ok || len(l) > 0 {
			errs = append(errs, "`mounts` must stay empty – the daemon sets the mounts (shared/in, shared/out) per sandbox; home must never be mounted")
		}
	}
	if base, ok := m["base"]; ok {
		list, _ := base.([]any)
		if _, isStr := base.(string); isStr {
			list = []any{base}
		}
		for _, b := range list {
			s := fmt.Sprint(b)
			if strings.Contains(s, "mounts") || s == "template:default" || strings.HasPrefix(s, "template:docker") || s == "template://default" {
				errs = append(errs, fmt.Sprintf("base %q brings mounts/Docker socket forwarding (home mount)", s))
			}
		}
	}
	if ssh, ok := m["ssh"].(map[string]any); ok {
		if fa, ok := ssh["forwardAgent"].(bool); ok && fa {
			errs = append(errs, "`ssh.forwardAgent` must not be true (the private key never leaves the Mac)")
		}
	}
	guestSockets := map[string]bool{}
	if pf, ok := m["portForwards"].([]any); ok {
		for _, e := range pf {
			f, _ := e.(map[string]any)
			gs, _ := f["guestSocket"].(string)
			hs, _ := f["hostSocket"].(string)
			if strings.Contains(gs, "docker.sock") || strings.Contains(hs, "docker.sock") {
				errs = append(errs, "the host's/VM's Docker socket must not be forwarded")
			}
			if gs != "" {
				guestSockets[gs] = true
				if !strings.HasPrefix(hs, "{{.Dir}}/sock/") {
					warnings = append(warnings, fmt.Sprintf("socket forward %q is outside {{.Dir}}/sock", gs))
				}
			} else {
				warnings = append(warnings, "port forward in the template: ports are passed through to the Mac")
			}
		}
	}
	for _, need := range []string{"/run/user/{{.UID}}/bergfried.sock", "/run/user/{{.UID}}/bergfried-agent.sock"} {
		if !guestSockets[need] {
			errs = append(errs, fmt.Sprintf("mandatory forward %q is missing (file/SSH requests would not work)", need))
		}
	}
	if len(errs) > 0 {
		return warnings, errors.New(strings.Join(errs, "; "))
	}
	return warnings, nil
}
