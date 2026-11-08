package index

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const MaxResults = 1000

type Entry struct {
	Path  string // relativ zum Home, "/"
	IsDir bool
	Size  int64
	MTime int64
}

type Index struct {
	home    string // aufgelöster echter Pfad
	bl      Blocklist
	mu      sync.RWMutex
	entries []Entry
}

var ErrBlocked = errors.New("Pfad ist gesperrt")
var ErrInvalid = errors.New("ungültiger Pfad")

// Build erfasst das Home. Symlinks werden weder gelistet noch verfolgt.
func Build(home string, bl Blocklist) (*Index, error) {
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	ix := &Index{home: real, bl: bl}
	return ix, ix.Refresh()
}

func (ix *Index) Refresh() error {
	var out []Entry
	err := filepath.WalkDir(ix.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil // unlesbar: überspringen
		}
		rel, rerr := filepath.Rel(ix.home, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ix.bl.BlockedRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, Entry{Path: rel, IsDir: d.IsDir(), Size: info.Size(), MTime: info.ModTime().Unix()})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	ix.mu.Lock()
	ix.entries = out
	ix.mu.Unlock()
	return nil
}

func cleanRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", ErrInvalid
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return "", ErrInvalid
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", ErrInvalid
		}
	}
	c := path.Clean(rel)
	if c == "." {
		return "", nil
	}
	return c, nil
}

// List: direkte Kinder von rel; Glob optional auf den Namen. Nur Metadaten.
func (ix *Index) List(rel, pattern string) ([]Entry, bool, error) {
	c, err := cleanRel(rel)
	if err != nil {
		return nil, false, err
	}
	if ix.bl.BlockedRel(c) {
		return nil, false, ErrBlocked
	}
	var out []Entry
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, e := range ix.entries {
		parent := path.Dir(e.Path)
		if parent == "." {
			parent = ""
		}
		if parent != c {
			continue
		}
		if pattern != "" {
			if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(path.Base(e.Path))); !ok {
				continue
			}
		}
		if len(out) >= MaxResults {
			return out, true, nil
		}
		out = append(out, e)
	}
	return out, false, nil
}

func (ix *Index) Search(query string) ([]Entry, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, false
	}
	var out []Entry
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, e := range ix.entries {
		if strings.Contains(strings.ToLower(e.Path), q) {
			if len(out) >= MaxResults {
				return out, true
			}
			out = append(out, e)
		}
	}
	return out, false
}

// Resolve prüft einen Pfad für eine Freigabe und liefert den echten absoluten Pfad.
// Lexikalisch UND nach Auflösung von Symlinks wird gegen die Sperrliste geprüft;
// der echte Pfad muss im Home liegen.
func (ix *Index) Resolve(rel string) (abs string, relClean string, err error) {
	c, err := cleanRel(rel)
	if err != nil || c == "" {
		return "", "", ErrInvalid
	}
	if ix.bl.BlockedRel(c) {
		return "", "", ErrBlocked
	}
	real, err := filepath.EvalSymlinks(filepath.Join(ix.home, filepath.FromSlash(c)))
	if err != nil {
		return "", "", err
	}
	rr, err := filepath.Rel(ix.home, real)
	if err != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(os.PathSeparator)) {
		return "", "", ErrBlocked
	}
	if ix.bl.BlockedRel(filepath.ToSlash(rr)) {
		return "", "", ErrBlocked
	}
	return real, c, nil
}
