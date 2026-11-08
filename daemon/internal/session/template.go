package session

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

// TemplateStore: vom Nutzer änderbare Kopie der VM-Vorlage unter <Home>/template/.
// Der Name der Basis-VM ergibt sich aus dem Inhalts-Hash (bergfried-base-<hash>); die Revision zählt Änderungen (Anzeige).
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

// Ensure legt das Verzeichnis (0700) an und übernimmt beim ersten Mal die mitgelieferte Vorlage.
func (t TemplateStore) Ensure() error {
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(t.Path()); os.IsNotExist(err) {
		if t.Seed == "" {
			return errors.New("keine Vorlage vorhanden und keine mitgelieferte angegeben")
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

// Hash: Kurz-Hash des Vorlagen-Inhalts; benennt die Basis-VM (gleicher Inhalt = gleiche Basis).
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

// Save prüft, validiert (limactl) und speichert; erhöht die Revision, wenn sich der Inhalt ändert.
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

// Reset stellt die mitgelieferte Vorlage wieder her.
func (t TemplateStore) Reset() error {
	b, err := os.ReadFile(t.Seed)
	if err != nil {
		return err
	}
	if _, err := CheckTemplate(string(b)); err != nil {
		return fmt.Errorf("mitgelieferte Vorlage ungültig: %w", err)
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

// CheckTemplate erzwingt die Isolationsregeln. Fehler = nicht speicherbar, Warnungen = Hinweis.
func CheckTemplate(yml string) (warnings []string, err error) {
	if len(yml) > MaxTemplateBytes {
		return nil, fmt.Errorf("Vorlage zu groß (max. %d KB)", MaxTemplateBytes>>10)
	}
	var m map[string]any
	if err := yaml.Unmarshal([]byte(yml), &m); err != nil {
		return nil, fmt.Errorf("YAML ungültig: %w", err)
	}
	if m == nil {
		return nil, errors.New("Vorlage ist leer")
	}
	var errs []string
	if mounts, ok := m["mounts"]; ok {
		if l, ok := mounts.([]any); !ok || len(l) > 0 {
			errs = append(errs, "`mounts` muss leer bleiben – Mounts (shared/in, shared/out) setzt der Daemon je Session; Home darf nie gemountet werden")
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
				errs = append(errs, fmt.Sprintf("Basis %q bringt Mounts/Docker-Socket-Weiterleitung mit (Home-Mount)", s))
			}
		}
	}
	if ssh, ok := m["ssh"].(map[string]any); ok {
		if fa, ok := ssh["forwardAgent"].(bool); ok && fa {
			errs = append(errs, "`ssh.forwardAgent` darf nicht true sein (der private Key verlässt den Mac nie)")
		}
	}
	guestSockets := map[string]bool{}
	if pf, ok := m["portForwards"].([]any); ok {
		for _, e := range pf {
			f, _ := e.(map[string]any)
			gs, _ := f["guestSocket"].(string)
			hs, _ := f["hostSocket"].(string)
			if strings.Contains(gs, "docker.sock") || strings.Contains(hs, "docker.sock") {
				errs = append(errs, "Docker-Socket des Hosts/der VM darf nicht weitergeleitet werden")
			}
			if gs != "" {
				guestSockets[gs] = true
				if !strings.HasPrefix(hs, "{{.Dir}}/sock/") {
					warnings = append(warnings, fmt.Sprintf("Socket-Weiterleitung %q liegt außerhalb von {{.Dir}}/sock", gs))
				}
			} else {
				warnings = append(warnings, "Port-Weiterleitung in der Vorlage: Ports werden zum Mac durchgereicht")
			}
		}
	}
	for _, need := range []string{"/run/user/{{.UID}}/bergfried.sock", "/run/user/{{.UID}}/bergfried-agent.sock"} {
		if !guestSockets[need] {
			errs = append(errs, fmt.Sprintf("Pflicht-Weiterleitung %q fehlt (Datei-/SSH-Anfragen würden nicht funktionieren)", need))
		}
	}
	if len(errs) > 0 {
		return warnings, errors.New(strings.Join(errs, "; "))
	}
	return warnings, nil
}
