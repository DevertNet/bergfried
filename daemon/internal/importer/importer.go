// Package importer copies a folder chosen by the user into the VM as a tar stream.
// It is never mounted: the VM gets a copy. Blocklist, symlinks, .env/keys and
// noise folders (node_modules, caches) stay out; credentials in .git/config are removed.
package importer

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/DevertNet/bergfried/daemon/internal/index"
)

const (
	MaxBytes = 5 << 30
	MaxFiles = 200000
)

type Importer struct {
	Home string
	BL   func() index.Blocklist
}

var credRe = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)

// resolve checks the folder: absolute, real path below home (not home itself), not blocked.
func (im Importer) resolve(p string) (real, rel string, err error) {
	if !filepath.IsAbs(p) || strings.ContainsRune(p, 0) {
		return "", "", errors.New("path must be absolute")
	}
	home, err := filepath.EvalSymlinks(im.Home)
	if err != nil {
		return "", "", err
	}
	real, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", "", fmt.Errorf("folder not found: %w", err)
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return "", "", errors.New("path is not a folder")
	}
	r, err := filepath.Rel(home, real)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("the folder must be below your home directory (not home itself)")
	}
	rel = filepath.ToSlash(r)
	if im.BL().BlockedRel(rel) {
		return "", "", errors.New("this folder is on the blocklist")
	}
	return real, rel, nil
}

type entry struct {
	sub  string // relative to the folder
	abs  string
	dir  bool
	mode fs.FileMode
	size int64
	mt   int64
}

func (im Importer) collect(real, rel string) (es []entry, bytes, skipped int64, err error) {
	bl := im.BL()
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		sub, _ := filepath.Rel(real, p)
		if sub == "." {
			return nil
		}
		sub = filepath.ToSlash(sub)
		if bl.BlockedRel(path.Join(rel, sub)) || d.Type()&fs.ModeSymlink != 0 {
			skipped++
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if index.SkipDirs[d.Name()] && d.Name() != ".git" {
				skipped++
				return fs.SkipDir
			}
			es = append(es, entry{sub: sub, abs: p, dir: true})
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || !info.Mode().IsRegular() {
			skipped++
			return nil
		}
		size := info.Size()
		if sub == ".git/config" {
			if b, rerr := os.ReadFile(p); rerr == nil {
				size = int64(len(credRe.ReplaceAll(b, []byte("$1"))))
			}
		}
		es = append(es, entry{sub: sub, abs: p, mode: info.Mode().Perm(), size: size, mt: info.ModTime().Unix()})
		bytes += size
		if int64(len(es)) > MaxFiles || bytes > MaxBytes {
			return fmt.Errorf("folder too large (max. %d GB / %d files)", MaxBytes>>30, MaxFiles)
		}
		return nil
	})
	return
}

// Preview: number of files/bytes that would be copied.
func (im Importer) Preview(p string) (files, bytes int64, err error) {
	real, rel, err := im.resolve(p)
	if err != nil {
		return 0, 0, err
	}
	es, b, _, err := im.collect(real, rel)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range es {
		if !e.dir {
			files++
		}
	}
	return files, b, nil
}

// Stream writes the folder as tar to w.
func (im Importer) Stream(p string, w io.Writer) (files, bytes int64, err error) {
	real, rel, err := im.resolve(p)
	if err != nil {
		return 0, 0, err
	}
	es, _, _, err := im.collect(real, rel)
	if err != nil {
		return 0, 0, err
	}
	tw := tar.NewWriter(w)
	for _, e := range es {
		if e.dir {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: e.sub + "/", Mode: 0o755}); err != nil {
				return files, bytes, err
			}
			continue
		}
		f, err := os.Open(e.abs)
		if err != nil {
			continue // vanished/unreadable: skip
		}
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			f.Close()
			continue
		}
		var data io.Reader = f
		size := st.Size()
		if e.sub == ".git/config" {
			b, _ := io.ReadAll(f)
			clean := credRe.ReplaceAll(b, []byte("$1"))
			data, size = strings.NewReader(string(clean)), int64(len(clean))
		}
		mode := int64(0o644)
		if e.mode&0o100 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.sub, Mode: mode, Size: size, ModTime: st.ModTime()}); err != nil {
			f.Close()
			return files, bytes, err
		}
		n, err := io.CopyN(tw, data, size)
		f.Close()
		if err != nil {
			return files, bytes, fmt.Errorf("%s: %w", e.sub, err)
		}
		files++
		bytes += n
	}
	return files, bytes, tw.Close()
}
