// Package access: check file/folder requests, wait for the decision, copy to shared/in.
package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

const (
	LargeBytes = 100 << 20
	LargeFiles = 500
)

// Asker shows the request to the user. The answer comes later via Service.Decide.
type Asker interface{ Ask(r store.Request) }

type decision struct {
	allow bool
	note  string
}

type Service struct {
	S       *store.Store
	Ix      *index.Index
	DataDir string
	Asker   Asker
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]chan decision
}

func New(s *store.Store, ix *index.Index, dataDir string, a Asker) *Service {
	return &Service{S: s, Ix: ix, DataDir: dataDir, Asker: a, Timeout: 10 * time.Minute, waiters: map[int64]chan decision{}}
}

func (v *Service) audit(event, sandbox, agent, detail string) {
	if err := v.S.Audit("daemon", event, sandbox, agent, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

// Dest: target folder in the sandbox's shared/in (visible in the VM under the same path).
func (v *Service) Dest(sandbox string) string {
	return filepath.Join(v.DataDir, "sandboxes", sandbox, "shared", "in", sandbox)
}

func measure(abs string, isDir bool, size int64, ix *index.Index, relBase string) (bytes, files int64) {
	if !isDir {
		return size, 1
	}
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, _ := filepath.Rel(abs, p)
		if rel != "." {
			if _, _, e := ix.Resolve(filepath.ToSlash(filepath.Join(relBase, rel))); e != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if !d.IsDir() {
			if info, e := d.Info(); e == nil && info.Mode().IsRegular() {
				bytes += info.Size()
				files++
			}
		}
		return nil
	})
	return
}

// Request handles request_file (folder=false) and request_folder (folder=true).
func (v *Service) Request(ctx context.Context, sandbox, agent string, folder bool, relPath, reason string) (*pb.AccessResult, error) {
	kind := "file"
	if folder {
		kind = "folder"
	}
	reason = sanitize(reason, 500)
	agent = sanitize(agent, 64)
	abs, rel, err := v.Ix.Resolve(relPath)
	if err != nil {
		if errors.Is(err, index.ErrBlocked) {
			id, _ := v.S.AddRequest(store.Request{Sandbox: sandbox, Agent: agent, Kind: kind, Path: sanitize(relPath, 300), Reason: reason, Status: "blocked"})
			v.audit("request.blocked", sandbox, agent, fmt.Sprintf("id=%d %s %s", id, kind, sanitize(relPath, 300)))
			return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_BLOCKED, Message: "Path is on the blocklist and will not be shared."}, nil
		}
		v.audit("request.invalid", sandbox, agent, fmt.Sprintf("%s %s: %v", kind, sanitize(relPath, 300), err))
		return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: "Path invalid or does not exist."}, nil
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: "Path does not exist."}, nil
	}
	if folder != info.IsDir() || (!folder && !info.Mode().IsRegular()) {
		return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: "Path is " + map[bool]string{true: "a folder (use request_folder)", false: "not a regular file (use request_file)"}[info.IsDir()]}, nil
	}
	b, n := measure(abs, info.IsDir(), info.Size(), v.Ix, rel)
	req := store.Request{Sandbox: sandbox, Agent: agent, Kind: kind, Path: rel, Reason: reason, Status: "pending", Bytes: b, Files: n}
	id, err := v.S.AddRequest(req)
	if err != nil {
		return nil, err
	}
	req.ID = id
	ch := make(chan decision, 1)
	v.mu.Lock()
	v.waiters[id] = ch
	v.mu.Unlock()
	defer func() { v.mu.Lock(); delete(v.waiters, id); v.mu.Unlock() }()
	v.audit("request.created", sandbox, agent, fmt.Sprintf("id=%d %s %s bytes=%d files=%d", id, kind, rel, b, n))
	if v.Asker != nil {
		v.Asker.Ask(req)
	}
	timer := time.NewTimer(v.Timeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		if !d.allow {
			msg := "The user declined."
			if d.note != "" {
				msg += " Note from the user: " + d.note
			}
			return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: msg}, nil
		}
		dst, err := v.copyOut(sandbox, rel, abs, info.IsDir())
		if err != nil {
			v.audit("request.copy_failed", sandbox, agent, fmt.Sprintf("id=%d %v", id, err))
			return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_DENIED, Message: "Kopieren fehlgeschlagen: " + err.Error()}, nil
		}
		v.audit("request.copied", sandbox, agent, fmt.Sprintf("id=%d -> %s", id, dst))
		return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_ALLOWED, VmPath: dst, Message: "Approved and copied."}, nil
	case <-timer.C:
		if ok, _ := v.S.SetRequestStatus(id, "expired"); ok {
			v.audit("request.expired", sandbox, agent, fmt.Sprintf("id=%d", id))
		}
		return &pb.AccessResult{Verdict: pb.Verdict_VERDICT_EXPIRED, Message: "No answer, request expired."}, nil
	case <-ctx.Done():
		if ok, _ := v.S.SetRequestStatus(id, "expired"); ok {
			v.audit("request.cancelled", sandbox, agent, fmt.Sprintf("id=%d", id))
		}
		return nil, ctx.Err()
	}
}

// Decide decides an open request. Large requests need confirmLarge. note is optional and
// returned to the agent when the request is denied (or allowed).
func (v *Service) Decide(id int64, allow, confirmLarge bool, note string) (string, error) {
	r, err := v.S.GetRequest(id)
	if err != nil {
		return "", err
	}
	if r.Status != "pending" {
		return "", fmt.Errorf("request %d is no longer open (%s)", id, r.Status)
	}
	if allow && Large(r) && !confirmLarge {
		return "", fmt.Errorf("large request (%d MB, %d files): second confirmation required", r.Bytes>>20, r.Files)
	}
	v.mu.Lock()
	ch, ok := v.waiters[id]
	v.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("request %d is no longer waiting (agent gave up)", id)
	}
	note = strings.TrimSpace(sanitize(note, 500))
	status := "denied"
	if allow {
		status = "allowed"
	}
	if ok, err := v.S.SetRequestStatus(id, status); err != nil || !ok {
		return "", fmt.Errorf("request %d is no longer open", id)
	}
	v.audit("request."+status, r.Sandbox, r.Agent, fmt.Sprintf("id=%d large_confirmed=%v", id, confirmLarge))
	ch <- decision{allow: allow, note: note}
	return status, nil
}

func Large(r store.Request) bool { return r.Bytes > LargeBytes || r.Files > LargeFiles }

func (v *Service) copyOut(sandbox, rel, abs string, isDir bool) (string, error) {
	base := v.Dest(sandbox)
	dst := filepath.Join(base, filepath.FromSlash(rel))
	if !strings.HasPrefix(dst, base+string(os.PathSeparator)) {
		return "", errors.New("target path outside shared/in")
	}
	if !isDir {
		return dst, copyFile(abs, dst)
	}
	err := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		r2, _ := filepath.Rel(abs, p)
		relFull := filepath.ToSlash(filepath.Join(rel, r2))
		if r2 != "." {
			// re-check every file (blocklist, symlink resolution)
			if _, _, e := v.Ix.Resolve(relFull); e != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		target := filepath.Join(dst, r2)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target)
	})
	return dst, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// sanitize: strip control characters, limit length. Agent text is display data only.
func sanitize(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' || r == 127 {
			return -1
		}
		return r
	}, s)
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
