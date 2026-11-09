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
	"sync/atomic"
)

const MaxResults = 1000

type Entry struct {
	Path  string // relative to home, "/"
	IsDir bool
	Size  int64
	MTime int64
}

type Index struct {
	home    string // resolved real path
	blMu    sync.RWMutex
	bl      Blocklist
	mu      sync.RWMutex
	entries []Entry
	ready   atomic.Bool
}

// New creates the index without scanning; Refresh builds it (e.g. in a goroutine).
func New(home string, bl Blocklist) (*Index, error) {
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	return &Index{home: real, bl: bl}, nil
}

func (ix *Index) Ready() bool { return ix.ready.Load() }

func (ix *Index) blocklist() Blocklist {
	ix.blMu.RLock()
	defer ix.blMu.RUnlock()
	return ix.bl
}

// Blocklist returns the current blocklist.
func (ix *Index) Blocklist() Blocklist { return ix.blocklist() }

// SetBlocklist replaces the blocklist immediately (for checks) and rebuilds the index.
func (ix *Index) SetBlocklist(bl Blocklist) {
	ix.blMu.Lock()
	ix.bl = bl
	ix.blMu.Unlock()
	go func() { _ = ix.Refresh() }()
}

var ErrBlocked = errors.New("path is blocked")
var ErrInvalid = errors.New("invalid path")
var ErrNotReady = errors.New("index is still being built")

// SkipDirs: directories the index does not descend into (noise). They remain shareable
// by path but are not part of lists/search.
var SkipDirs = map[string]bool{"node_modules": true, ".git": true, ".cache": true, ".npm": true,
	".gradle": true, ".Trash": true, ".cocoapods": true, "DerivedData": true, ".cargo": true, ".rustup": true}

// Build scans the home. Symlinks are neither listed nor followed.
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
			return nil // unreadable: skip
		}
		rel, rerr := filepath.Rel(ix.home, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ix.blocklist().BlockedRel(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && SkipDirs[d.Name()] {
			return fs.SkipDir
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
	ix.ready.Store(true)
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

// List: direct children of rel; optional glob on the name. Metadata only.
func (ix *Index) List(rel, pattern string) ([]Entry, bool, error) {
	if !ix.Ready() {
		return nil, false, ErrNotReady
	}
	c, err := cleanRel(rel)
	if err != nil {
		return nil, false, err
	}
	if ix.blocklist().BlockedRel(c) {
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
	if !ix.Ready() {
		return nil, false
	}
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

// Resolve checks a path for an approval and returns the real absolute path.
// Checked against the blocklist both lexically AND after resolving symlinks;
// the real path must be inside home.
func (ix *Index) Resolve(rel string) (abs string, relClean string, err error) {
	c, err := cleanRel(rel)
	if err != nil || c == "" {
		return "", "", ErrInvalid
	}
	if ix.blocklist().BlockedRel(c) {
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
	if ix.blocklist().BlockedRel(filepath.ToSlash(rr)) {
		return "", "", ErrBlocked
	}
	return real, c, nil
}
