// Package skills validates, previews and applies changes to the shared skills
// folder (<home>/skills). The folder is shared read-only with every sandbox;
// changes are proposed by an assistant and applied by the daemon after the user
// allowed them. Nothing in this package follows symlinks.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxFiles      = 30
	MaxFileBytes  = 128 << 10
	MaxTotalBytes = 512 << 10
)

// reserved is the skill name that the VM uses for its own link folder.
const reserved = "synced"

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// checkText rejects text that cannot be shown safely in an approval card or
// diff: invalid UTF-8, control characters and invisible or line-breaking runes
// (Cf, Zl, Zp). layout allows tab and newline, which are fine in file content
// but never in a path.
func checkText(s string, layout bool) error {
	if !utf8.ValidString(s) {
		return errors.New("invalid UTF-8")
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(layout && (r == '\n' || r == '\t')) {
			return fmt.Errorf("control character U+%04X", r)
		}
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return fmt.Errorf("invisible or line-breaking character U+%04X", r)
		}
	}
	return nil
}

// Change is one write or delete below the skills folder. Path is
// slash-separated and relative to the folder: "<skill>/<file…>".
type Change struct {
	Path    string
	Delete  bool
	Content []byte
}

// FilePreview describes what a change would do. Op is "create", "modify",
// "delete" or "unchanged"; Diff is a unified-style diff (empty for unchanged).
type FilePreview struct {
	Path string
	Op   string
	Diff string
}

// Dir is the shared skills folder of the Bergfried data dir.
func Dir(home string) string { return filepath.Join(home, "skills") }

// Ensure creates the skills folder if needed and returns it.
func Ensure(home string) (string, error) {
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ValidatePath checks a slash-separated path relative to the skills folder.
// A path is a file inside a skill folder: depth 2..6, the first component is
// the skill name.
func ValidatePath(p string) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("invalid skill path %q: %s", p, fmt.Sprintf(format, args...))
	}
	if p == "" {
		return invalid("path is empty")
	}
	if strings.HasPrefix(p, "/") {
		return invalid("path must be relative")
	}
	if strings.Contains(p, `\`) {
		return invalid("backslashes are not allowed")
	}
	if err := checkText(p, false); err != nil {
		return invalid("%s", err)
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return invalid("a file must be inside a skill folder (depth 2..6)")
	}
	if len(parts) > 6 {
		return invalid("path is too deep (max. 6 components)")
	}
	for _, part := range parts {
		switch {
		case part == "":
			return invalid("empty path component")
		case part == "." || part == "..":
			return invalid("path traversal is not allowed")
		case strings.HasPrefix(part, "."):
			return invalid("hidden files and folders are not allowed")
		case len(part) > 100:
			return invalid("path component longer than 100 bytes")
		}
	}
	if !skillNameRe.MatchString(parts[0]) {
		return invalid("skill name must match %s", skillNameRe.String())
	}
	if parts[0] == reserved {
		return invalid("skill name %q is reserved", reserved)
	}
	return nil
}

// ValidateChanges checks the list itself, every path and all content limits.
func ValidateChanges(cs []Change) error {
	if len(cs) == 0 {
		return errors.New("no changes")
	}
	if len(cs) > MaxFiles {
		return fmt.Errorf("too many changes (%d, max. %d)", len(cs), MaxFiles)
	}
	seen := make(map[string]bool, len(cs))
	seenPaths := make([]string, 0, len(cs))
	total := 0
	for _, c := range cs {
		if err := ValidatePath(c.Path); err != nil {
			return err
		}
		if seen[c.Path] {
			return fmt.Errorf("duplicate path %q", c.Path)
		}
		for _, prev := range seenPaths {
			if strings.EqualFold(prev, c.Path) {
				return fmt.Errorf("%s: duplicate path (conflicts with %q, paths are compared case-insensitively)", c.Path, prev)
			}
			if strings.HasPrefix(prev, c.Path+"/") || strings.HasPrefix(c.Path, prev+"/") {
				return fmt.Errorf("%s: conflicts with %q (a path cannot be both a file and a folder)", c.Path, prev)
			}
		}
		seen[c.Path] = true
		seenPaths = append(seenPaths, c.Path)
		if c.Delete {
			if len(c.Content) != 0 {
				return fmt.Errorf("%s: a delete must not carry content", c.Path)
			}
			continue
		}
		if len(c.Content) > MaxFileBytes {
			return fmt.Errorf("%s: file too large (%d bytes, max. %d)", c.Path, len(c.Content), MaxFileBytes)
		}
		total += len(c.Content)
		if total > MaxTotalBytes {
			return fmt.Errorf("changes too large (%d bytes, max. %d)", total, MaxTotalBytes)
		}
		if !utf8.Valid(c.Content) {
			return fmt.Errorf("%s: content is not valid UTF-8", c.Path)
		}
		if bytes.IndexByte(c.Content, 0) >= 0 {
			return fmt.Errorf("%s: content contains NUL bytes (text only)", c.Path)
		}
		if err := checkText(string(c.Content), true); err != nil {
			return fmt.Errorf("%s: content contains %s", c.Path, err)
		}
	}
	return nil
}

// safePath validates rel and joins it to dir, never leaving dir.
func safePath(dir, rel string) (string, error) {
	if err := ValidatePath(rel); err != nil {
		return "", err
	}
	root := filepath.Clean(dir)
	full := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	if r == "." || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("path %q escapes the skills folder", rel)
	}
	return full, nil
}

// checkLinks refuses if any component of rel below dir is a symlink. Missing
// components are fine (the file does not exist yet).
func checkLinks(dir, rel string) error {
	p := filepath.Clean(dir)
	for _, part := range strings.Split(rel, "/") {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: component %q is a symlink", rel, part)
		}
	}
	return nil
}

// readCurrent returns the current content of rel and whether it exists. It
// refuses symlinks on the way and at the target.
func readCurrent(dir, rel string) ([]byte, bool, error) {
	full, err := safePath(dir, rel)
	if err != nil {
		return nil, false, err
	}
	if err := checkLinks(dir, rel); err != nil {
		return nil, false, err
	}
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s: not a regular file", rel)
	}
	b, err := os.ReadFile(full)
	return b, err == nil, err
}

// sha256 of the empty tree (also used for a missing skills folder).
func emptyHash() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

type hashEntry struct {
	rel     string
	kind    byte   // 'f' regular file, 'l' symlink, 'o' other
	payload string // exec bit, link target or type
	content string // hex sha256 for regular files
}

// Hash returns a hex sha256 over the whole tree below dir. Regular files are
// hashed by sorted relative path together with the executable bit and their
// content; directories are implicit. Symlinks and other non-regular entries are
// hashed with their kind and link target. A missing dir has the hash of an
// empty tree. Symlinks are never followed.
func Hash(dir string) (string, error) {
	root := filepath.Clean(dir)
	entries := []hashEntry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if p == root {
			return nil
		}
		r, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(r)
		switch {
		case d.IsDir():
			return nil // implicit in the paths of its entries
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entries = append(entries, hashEntry{rel: rel, kind: 'l', payload: target})
		case d.Type().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, cerr := io.Copy(h, f)
			f.Close()
			if cerr != nil {
				return cerr
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			exec := "-"
			if info.Mode().Perm()&0o111 != 0 {
				exec = "x"
			}
			entries = append(entries, hashEntry{rel: rel, kind: 'f', payload: exec, content: hex.EncodeToString(h.Sum(nil))})
		default:
			entries = append(entries, hashEntry{rel: rel, kind: 'o', payload: d.Type().String()})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return emptyHash(), nil
		}
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		field(h, string(e.kind))
		field(h, e.rel)
		field(h, e.payload)
		field(h, e.content)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// field writes a length-prefixed field, so entries cannot run into each other.
func field(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	io.WriteString(h, s)
}

// FileHash returns the hex sha256 of the regular file rel below dir. It returns
// "" if the file does not exist and an error if it is not a regular file or a
// symlink is involved.
func FileHash(dir, rel string) (string, error) {
	full, err := safePath(dir, rel)
	if err != nil {
		return "", err
	}
	if err := checkLinks(dir, rel); err != nil {
		return "", err
	}
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: not a regular file", rel)
	}
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Preview validates the changes and describes them relative to the current
// state of dir. Deleting a missing file is an error.
func Preview(dir string, cs []Change) ([]FilePreview, error) {
	if err := ValidateChanges(cs); err != nil {
		return nil, err
	}
	out := make([]FilePreview, 0, len(cs))
	for _, c := range cs {
		cur, exists, err := readCurrent(dir, c.Path)
		if err != nil {
			return nil, err
		}
		if c.Delete {
			if !exists {
				return nil, fmt.Errorf("%s: cannot delete, file does not exist", c.Path)
			}
			out = append(out, FilePreview{Path: c.Path, Op: "delete", Diff: unifiedDiff(string(cur), "")})
			continue
		}
		if !exists {
			out = append(out, FilePreview{Path: c.Path, Op: "create", Diff: unifiedDiff("", string(c.Content))})
			continue
		}
		if bytes.Equal(cur, c.Content) {
			out = append(out, FilePreview{Path: c.Path, Op: "unchanged"})
			continue
		}
		d := unifiedDiff(string(cur), string(c.Content))
		if d == "" {
			// Only the final newline differs, which the line diff cannot show.
			d = "(only the newline at the end of the file differs)\n"
		}
		out = append(out, FilePreview{Path: c.Path, Op: "modify", Diff: d})
	}
	return out, nil
}

// Apply validates the changes and writes them below dir. base maps path ->
// FileHash at proposal time: a missing key means the file must not exist, a
// present key means the hash must still match. All checks run before the first
// change, so a conflict leaves the folder untouched. Symlinks are refused.
func Apply(dir string, cs []Change, base map[string]string) error {
	if err := ValidateChanges(cs); err != nil {
		return err
	}
	root := filepath.Clean(dir)
	full := make([]string, len(cs))
	for i, c := range cs {
		p, err := safePath(root, c.Path)
		if err != nil {
			return err
		}
		full[i] = p
	}

	for _, c := range cs {
		if err := checkLinks(root, c.Path); err != nil {
			return err
		}
		if err := checkTarget(root, c.Path); err != nil {
			return err
		}
		got, err := FileHash(root, c.Path)
		if err != nil {
			return err
		}
		if c.Delete && got == "" {
			return fmt.Errorf("%s: cannot delete, file does not exist", c.Path)
		}
		want, keyed := base[c.Path]
		if keyed {
			if got != want {
				return fmt.Errorf("%s: changed since the proposal (expected %s, found %q)", c.Path, shortHash(want), shortHash(got))
			}
		} else if got != "" {
			return fmt.Errorf("%s: file appeared since the proposal", c.Path)
		}
	}

	for i, c := range cs {
		if c.Delete {
			if err := os.Remove(full[i]); err != nil {
				return err
			}
			prune(root, full[i])
			continue
		}
		if err := writeFile(full[i], c.Content); err != nil {
			return err
		}
	}
	return nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// checkTarget refuses a target that exists but is not a regular file.
func checkTarget(dir, rel string) error {
	full, err := safePath(dir, rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s: is a symlink", rel)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: is not a regular file", rel)
	}
	return nil
}

// writeFile writes data via a temp file in the target directory and renames it
// into place. New files get 0644, an overwritten file keeps its mode.
func writeFile(full string, data []byte) error {
	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Lstat(full); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s: is not a regular file", full)
		}
		mode = fi.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(parent, ".tmp-*")
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), full); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// prune removes empty parent directories of full up to (excluding) root.
func prune(root, full string) {
	prefix := root + string(os.PathSeparator)
	for p := filepath.Dir(full); p != root && strings.HasPrefix(p, prefix); p = filepath.Dir(p) {
		if err := os.Remove(p); err != nil {
			return
		}
	}
}
