package notify

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/broker"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
)

const chooseScript = `on run argv
	set p to item 1 of argv
	set d to item 2 of argv
	set its to rest of (rest of argv)
	set r to choose from list its with title "Bergfried" with prompt p default items {d}
	if r is false then return "__cancel__"
	return item 1 of r
end run`

func choose(prompt, def string, items []string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 11*time.Minute)
	defer cancel()
	args := append([]string{"-e", chooseScript, prompt, def}, items...)
	out, err := exec.CommandContext(ctx, "osascript", args...).Output()
	v := strings.TrimSpace(string(out))
	if err != nil || v == "__cancel__" || v == "" {
		return "", false
	}
	return v, true
}

type SSHDialog struct {
	Decide func(broker.Decision) (string, error)
	Keys   func() ([]*pb.KeyInfo, error)
	Log    func(string)
}

var durations = []struct {
	Label string
	Min   int
}{{"15 Minuten", 15}, {"1 Stunde", 60}, {"4 Stunden", 240}, {"24 Stunden", 1440}}

func (d *SSHDialog) log(f string, a ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(f, a...))
	}
}

func (d *SSHDialog) AskSSH(r store.Request, info broker.SSHInfo) {
	go d.run(r, info)
}

func (d *SSHDialog) run(r store.Request, info broker.SSHInfo) {
	no := func() { _, _ = d.Decide(broker.Decision{ID: r.ID, Allow: false}) }
	warn := ""
	if info.Source != "known_hosts" {
		warn = "\n\n⚠ Host-Key ist NEU (nicht in deiner known_hosts). Fingerprint:\n" + strings.Join(info.HostKeyFPs, "\n") + "\nNur bestätigen, wenn du ihn kennst."
	}
	user := info.RequestedUser
	if user == "" {
		user = info.DefaultUser
	}
	text := fmt.Sprintf("Sandbox „%s“ / Agent „%s“ möchte per SSH auf %s zugreifen (Benutzer: %s).%s\n\nBegründung (vom Agent, ungeprüft):\n%s", r.Session, r.Agent, r.Path, user, warn, r.Reason)
	if !ask(text, "Ablehnen", "Weiter …") {
		no()
		return
	}
	labels := []string{}
	def := durations[0].Label
	for _, x := range durations {
		labels = append(labels, x.Label)
		if x.Min <= info.SuggestedMinutes {
			def = x.Label
		}
	}
	dl, ok := choose("Wie lange soll die Freigabe gelten?", def, labels)
	if !ok {
		no()
		return
	}
	minutes := 15
	for _, x := range durations {
		if x.Label == dl {
			minutes = x.Min
		}
	}
	keys, err := d.Keys()
	if err != nil || len(keys) == 0 {
		d.log("SSH-Dialog #%d: keine Keys im Upstream-Agent (%v)", r.ID, err)
		no()
		return
	}
	var klabels []string
	kdef := ""
	byLabel := map[string]string{}
	for _, k := range keys {
		l := fmt.Sprintf("%s  %s  (%s)", k.Fingerprint, k.Comment, k.Type)
		klabels = append(klabels, l)
		byLabel[l] = k.Fingerprint
		if k.Fingerprint == info.DefaultKey || kdef == "" {
			kdef = l
		}
	}
	kl, ok := choose(fmt.Sprintf("Mit welchem Key? (Benutzer: %s)", user), kdef, klabels)
	if !ok {
		no()
		return
	}
	scope, ok := choose("Geltung der Freigabe:", "Nur dieser Agent (Anzeige/Protokoll)", []string{"Nur dieser Agent (Anzeige/Protokoll)", "Alle Agents der Sandbox"})
	if !ok {
		no()
		return
	}
	mode, ok := choose("Signieren:", "Still im Zeitfenster (nur protokolliert)", []string{"Still im Zeitfenster (nur protokolliert)", "Jede Verbindung einzeln bestätigen"})
	if !ok {
		no()
		return
	}
	_, err = d.Decide(broker.Decision{ID: r.ID, Allow: true, Minutes: minutes, KeyFP: byLabel[kl], User: user,
		AllAgents: strings.HasPrefix(scope, "Alle"), PerConn: strings.HasPrefix(mode, "Jede"), TrustUnknownHost: info.Source != "known_hosts"})
	if err != nil {
		d.log("SSH-Dialog #%d: %v", r.ID, err)
	}
}

// ConfirmEach: Einzelbestätigung je Verbindung.
func ConfirmEach(g store.SSHGrant, hostKeyFP string) bool {
	return ask(fmt.Sprintf("SSH-Verbindung zu %s (Freigabe #%d, Sandbox „%s“) erlauben?", g.Host, g.ID, g.Session), "Ablehnen", "Erlauben")
}
