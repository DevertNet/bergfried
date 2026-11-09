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

func (t TemplateStore) Path() string    { return filepath.Join(t.Dir, "bergfried.yaml") }
func (t TemplateStore) revPath() string { return filepath.Join(t.Dir, "revision") }

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
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
		return nil
	}
	if err := os.WriteFile(t.Path(), b, 0o600); err != nil {
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
