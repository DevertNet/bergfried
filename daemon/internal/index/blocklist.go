// Package index: Dateinamen-Index über das Home. Gesperrte Pfade tauchen nie auf
// und werden nie freigegeben. Vergleiche sind case-insensitiv (macOS-Dateisystem).
package index

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Blocklist: Pfade relativ zum Home (Präfix, auch Verzeichnisse) und Namensmuster (Glob auf Basename).
type Blocklist struct {
	Paths []string `json:"paths"`
	Names []string `json:"names"`
}

func DefaultBlocklist() Blocklist {
	return Blocklist{
		Paths: []string{".ssh", "Library", ".config",
			".gnupg", ".aws", ".kube", ".docker", ".netrc", ".git-credentials",
			".claude", ".claude.json", ".bergfried", ".lima"},
		Names: []string{".env", ".env.*", "*.keychain", "*.keychain-db", "*.kdbx", "id_rsa", "id_ed25519", "id_ecdsa"},
	}
}

func LoadBlocklist(file string) (Blocklist, error) {
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return DefaultBlocklist(), nil
	}
	if err != nil {
		return Blocklist{}, err
	}
	var bl Blocklist
	if err := json.Unmarshal(b, &bl); err != nil {
		return Blocklist{}, err
	}
	return bl, nil
}

func (bl Blocklist) Save(file string) error {
	b, err := json.MarshalIndent(bl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, b, 0o600)
}

// BlockedRel prüft einen Pfad relativ zum Home (mit "/").
func (bl Blocklist) BlockedRel(rel string) bool {
	rel = strings.ToLower(path.Clean(filepath.ToSlash(rel)))
	if rel == "." {
		return false
	}
	for _, p := range bl.Paths {
		p = strings.ToLower(path.Clean(filepath.ToSlash(p)))
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	for _, part := range strings.Split(rel, "/") {
		for _, g := range bl.Names {
			if ok, _ := path.Match(strings.ToLower(g), part); ok {
				return true
			}
		}
	}
	return false
}
