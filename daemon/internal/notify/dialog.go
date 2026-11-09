// Package notify: approval dialog via osascript (macOS). The agent text is only passed as an
// argument, never built into AppleScript source.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/access"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const script = `on run argv
	set r to display dialog (item 1 of argv) with title "Bergfried" buttons {(item 2 of argv), (item 3 of argv)} default button (item 2 of argv) with icon caution giving up after 600
	return (button returned of r) & "|" & ((gave up of r) as text)
end run`

type Dialog struct {
	Decide func(id int64, allow, confirmLarge bool) (string, error)
	Log    func(string)
}

func ask(text, no, yes string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", script, text, no, yes).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == yes+"|false"
}

func Text(r store.Request) string {
	what := "the file"
	if r.Kind == "folder" {
		what = "the folder"
	}
	return fmt.Sprintf("Sandbox \"%s\" / agent \"%s\" wants %s copied:\n\n~/%s\n(%d file(s), %.1f MB)\n\nReason (from the agent, unchecked):\n%s",
		r.Sandbox, r.Agent, what, r.Path, r.Files, float64(r.Bytes)/1048576, r.Reason)
}

func (d *Dialog) Ask(r store.Request) {
	go func() {
		if !ask(Text(r), "Ablehnen", "Erlauben") {
			if _, err := d.Decide(r.ID, false, false); err != nil && d.Log != nil {
				d.Log(fmt.Sprintf("Dialog #%d: %v", r.ID, err))
			}
			return
		}
		confirm := false
		if access.Large(r) {
			confirm = ask(fmt.Sprintf("Very large approval: %.0f MB in %d files.\nReally copy?", float64(r.Bytes)/1048576, r.Files), "Cancel", "Yes, copy")
			if !confirm {
				_, _ = d.Decide(r.ID, false, false)
				return
			}
		}
		if _, err := d.Decide(r.ID, true, confirm); err != nil && d.Log != nil {
			d.Log(fmt.Sprintf("Dialog #%d: %v", r.ID, err))
		}
	}()
}
