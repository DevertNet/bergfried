package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const (
	// kindSkill is the store kind of a proposal for the shared skills folder.
	kindSkill = "skill"

	// maxOpenProposals: more open proposals per sandbox are refused.
	maxOpenProposals = 5

	maxReasonRunes = 500
	maxPathRunes   = 300
)

// Result is the answer the assistant gets for a proposal.
type Result struct {
	Allowed bool
	Message string
}

// FileDetail is one preview entry as the app shows it. It carries the diff,
// never the file content.
type FileDetail struct {
	Path string `json:"path"`
	Op   string `json:"op"`
	Diff string `json:"diff"`
}

// proposal is the JSON in Request.Extra of a kind "skill" row.
type proposal struct {
	Changes []proposalChange  `json:"changes"`
	Base    map[string]string `json:"base"` // path -> FileHash at proposal time (missing key: file must not exist)
	Preview []FileDetail      `json:"preview"`
}

type proposalChange struct {
	Path    string `json:"path"`
	Delete  bool   `json:"delete,omitempty"`
	Content string `json:"content,omitempty"`
}

// changes rebuilds the Change list stored in the proposal.
func (p proposal) changes() []Change {
	cs := make([]Change, len(p.Changes))
	for i, c := range p.Changes {
		cs[i] = Change{Path: c.Path, Delete: c.Delete, Content: []byte(c.Content)}
	}
	return cs
}

// DetailOf parses Request.Extra of a kind "skill" row into what the app shows.
func DetailOf(extra string) ([]FileDetail, error) {
	var p proposal
	if err := json.Unmarshal([]byte(extra), &p); err != nil {
		return nil, err
	}
	return p.Preview, nil
}

// Service: an assistant proposes changes to the shared skills folder, the user
// decides in the app, only then the daemon writes them.
type Service struct {
	S       *store.Store
	Dir     string
	Timeout time.Duration

	mu      sync.Mutex
	waiters map[int64]chan Result
	applyMu sync.Mutex // serialises Apply: two decisions must not interleave

	// beforeApply is a test hook, called after a decision claimed its waiter and
	// before applying. nil in production.
	beforeApply func()
}

func NewService(st *store.Store, dir string) *Service {
	return &Service{S: st, Dir: dir, Timeout: 10 * time.Minute, waiters: map[int64]chan Result{}}
}

// claimWaiter takes the waiter of a request, so exactly one Decide answers it.
func (s *Service) claimWaiter(id int64) (chan Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.waiters[id]
	if ok {
		delete(s.waiters, id)
	}
	return ch, ok
}

// releaseWaiter removes our own waiter; false means Decide already claimed it.
func (s *Service) releaseWaiter(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.waiters[id]; !ok {
		return false
	}
	delete(s.waiters, id)
	return true
}

func (s *Service) audit(event, sandbox, worktree, detail string) {
	if err := s.S.Audit("daemon", event, sandbox, worktree, detail); err != nil {
		fmt.Fprintln(os.Stderr, "WARNING: audit log not written:", err)
	}
}

// cleanText drops control characters (except newlines) and invisible or
// line-breaking runes (Cf, Zl, Zp) and limits the text to max runes.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsControl(r) {
			return -1
		}
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// openSkillRequests counts the pending skill proposals of sandbox.
func (s *Service) openSkillRequests(sandbox string) (int, error) {
	rs, err := s.S.ListRequests(true, 1000)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		if r.Kind == kindSkill && r.Sandbox == sandbox {
			n++
		}
	}
	return n, nil
}

// skillNames joins the distinct skill names (first path component) of cs.
func skillNames(cs []Change) string {
	seen := make(map[string]bool, len(cs))
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		name := c.Path
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	out := strings.Join(names, ", ")
	if r := []rune(out); len(r) > maxPathRunes {
		out = string(r[:maxPathRunes])
	}
	return out
}

// Request: the assistant proposes changes to the skills folder and blocks until
// the user decided, the request expired or ctx is done. Problems the assistant
// can fix come back as Result{Allowed:false, Message:…} with a nil error.
func (s *Service) Request(ctx context.Context, sandbox, folder, reason string, cs []Change) (Result, error) {
	reason = cleanText(reason, maxReasonRunes)
	pv, err := Preview(s.Dir, cs)
	if err != nil {
		return Result{Message: "Invalid proposal: " + err.Error()}, nil
	}
	unchanged := true
	for _, p := range pv {
		if p.Op != "unchanged" {
			unchanged = false
			break
		}
	}
	if unchanged {
		return Result{Message: "Nothing to change: the files already have this content."}, nil
	}
	open, err := s.openSkillRequests(sandbox)
	if err != nil {
		return Result{}, err
	}
	if open >= maxOpenProposals {
		return Result{Message: "Too many open proposals for this sandbox; wait for the user to decide."}, nil
	}

	base := map[string]string{}
	var total int64
	stored := make([]proposalChange, len(cs))
	for i, c := range cs {
		h, err := FileHash(s.Dir, c.Path)
		if err != nil {
			return Result{Message: "Invalid proposal: " + err.Error()}, nil
		}
		if h != "" {
			base[c.Path] = h
		}
		total += int64(len(c.Content))
		stored[i] = proposalChange{Path: c.Path, Delete: c.Delete, Content: string(c.Content)}
	}
	details := make([]FileDetail, len(pv))
	for i, p := range pv {
		details[i] = FileDetail{Path: p.Path, Op: p.Op, Diff: p.Diff}
	}
	extra, err := json.Marshal(proposal{Changes: stored, Base: base, Preview: details})
	if err != nil {
		return Result{}, err
	}
	path := skillNames(cs)

	s.mu.Lock()
	id, err := s.S.AddRequest(store.Request{Sandbox: sandbox, Worktree: folder, Kind: kindSkill, Path: path,
		Reason: reason, Status: "pending", Bytes: total, Files: int64(len(cs)), Extra: string(extra)})
	if err != nil {
		s.mu.Unlock()
		return Result{}, err
	}
	ch := make(chan Result, 1)
	s.waiters[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, id); s.mu.Unlock() }()
	s.audit("skills.requested", sandbox, folder, fmt.Sprintf("id=%d files=%d skills=%s", id, len(cs), path))

	timer := time.NewTimer(s.Timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, nil
	case <-timer.C:
		if !s.releaseWaiter(id) {
			// Decide claimed the request; it sends exactly one result.
			return <-ch, nil
		}
		if ok, _ := s.S.SetRequestStatus(id, "expired"); ok {
			s.audit("skills.expired", sandbox, folder, fmt.Sprintf("id=%d", id))
		}
		return Result{Message: "No answer, request expired."}, nil
	case <-ctx.Done():
		if !s.releaseWaiter(id) {
			return Result{}, ctx.Err()
		}
		_, _ = s.S.SetRequestStatus(id, "expired")
		return Result{}, ctx.Err()
	}
}

// Decide: decision from the app. Returns "allowed" or "denied" (or "failed"
// together with the error when applying failed).
func (s *Service) Decide(ctx context.Context, id int64, allow bool, note string) (string, error) {
	r, err := s.S.GetRequest(id)
	if err != nil {
		return "", err
	}
	if r.Kind != kindSkill || r.Status != "pending" {
		return "", fmt.Errorf("request %d is not an open skills proposal", id)
	}
	// Claim the waiter atomically: exactly one Decide answers a request, so a
	// parallel decision or a timeout cannot apply the same change twice.
	ch, ok := s.claimWaiter(id)
	if !ok {
		return "", fmt.Errorf("request %d is no longer waiting (the requester gave up)", id)
	}
	note = strings.TrimSpace(cleanText(note, maxReasonRunes))
	if !allow {
		msg := "The user declined."
		if note != "" {
			msg += " Note from the user: " + note
		}
		ok, serr := s.S.SetRequestStatus(id, "denied")
		if !ok {
			ch <- Result{Allowed: false, Message: msg}
			if serr != nil {
				return "", serr
			}
			return "", fmt.Errorf("request %d is no longer open", id)
		}
		s.audit("skills.denied", r.Sandbox, r.Worktree, fmt.Sprintf("id=%d", id))
		ch <- Result{Allowed: false, Message: msg}
		return "denied", nil
	}

	if s.beforeApply != nil {
		s.beforeApply()
	}
	var p proposal
	aerr := json.Unmarshal([]byte(r.Extra), &p)
	if aerr != nil {
		aerr = fmt.Errorf("the stored proposal is unreadable: %w", aerr)
	} else {
		s.applyMu.Lock()
		aerr = Apply(s.Dir, p.changes(), p.Base)
		s.applyMu.Unlock()
	}
	if aerr != nil {
		_, _ = s.S.SetRequestStatus(id, "failed")
		s.audit("skills.apply_failed", r.Sandbox, r.Worktree, fmt.Sprintf("id=%d error=%v", id, aerr))
		ch <- Result{Allowed: false, Message: "The user allowed it, but applying failed: " + aerr.Error() + ". Propose again."}
		return "failed", aerr
	}
	msg := "Approved and applied. The skills are shared with all sandboxes; the link in each VM is refreshed within a few seconds, no restart needed."
	if ok, _ := s.S.SetRequestStatus(id, "allowed"); !ok {
		ch <- Result{Allowed: true, Message: msg}
		return "", fmt.Errorf("request %d is no longer open", id)
	}
	s.audit("skills.allowed", r.Sandbox, r.Worktree, fmt.Sprintf("id=%d files=%d", id, r.Files))
	ch <- Result{Allowed: true, Message: msg}
	return "allowed", nil
}
