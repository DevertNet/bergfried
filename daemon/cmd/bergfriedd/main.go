// bergfriedd (M1): CLI zum Verwalten von Bergfried-Sessions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/DevertNet/bergfried/daemon/internal/access"
	"github.com/DevertNet/bergfried/daemon/internal/api"
	"github.com/DevertNet/bergfried/daemon/internal/broker"
	"github.com/DevertNet/bergfried/daemon/internal/importer"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/notify"
	"github.com/DevertNet/bergfried/daemon/internal/rpc"
	"github.com/DevertNet/bergfried/daemon/internal/session"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const usage = `bergfriedd – Bergfried-CLI (M1)

  base build [--rebuild]        Basis-VM aus der Vorlage bauen
  create <name> [--repo URL | --import ORDNER]   Session aus Basis-VM klonen
  start|stop <name>
  delete <name> [--force]       warnt bei ungepushten Commits
  list                          Sessions mit VM-Status und Agents
  agent add <session> <agent>   neuer Agent (tmux-Fenster + Worktree + Remote Control)
  login <session>               claude auth login in der VM (einmal pro VM)
  attach <session>              tmux der Session öffnen
  shell <session>               Shell in der VM
  audit [-n 50]                 Audit-Log
  serve [--no-dialog]           Daemon: Index, Sockets je Session, Freigabe-Dialog (läuft im Vordergrund)
  ssh keys|grants|allow|deny|revoke|extend   SSH-Freigaben
  requests [list|allow|deny] [id] [--confirm-large] [--all]   Anfragen ansehen/entscheiden

Umgebung: BERGFRIED_HOME (Standard ~/.bergfried), BERGFRIED_TEMPLATE (Standard vm/bergfried.yaml)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(cmd string, args []string) error {
	ctx := context.Background()
	h, _ := os.UserHomeDir()
	home := getenv("BERGFRIED_HOME", filepath.Join(h, ".bergfried"))
	st, err := store.Open(home)
	if err != nil {
		return err
	}
	m := &session.Manager{L: lima.Client{R: lima.ExecRunner{}}, S: st, Home: home,
		Template: getenv("BERGFRIED_TEMPLATE", "vm/bergfried.yaml"), Actor: "cli"}
	// Vorlage im Datenverzeichnis (änderbar in der App); CLI und Daemon nutzen dieselbe Revision.
	ts := session.TemplateStore{Dir: filepath.Join(home, "template"), Seed: m.Template, L: m.L}
	if err := ts.Ensure(); err == nil {
		m.Tpl = &ts
	} else if cmd == "base" || cmd == "create" || cmd == "serve" {
		return fmt.Errorf("Vorlage: %w", err)
	}
	need := func(n int) error {
		if len(args) < n {
			return errors.New("zu wenige Argumente\n\n" + usage)
		}
		return nil
	}
	switch cmd {
	case "base":
		if err := need(1); err != nil || args[0] != "build" {
			return errors.New("nutze: base build [--rebuild]")
		}
		fs := flag.NewFlagSet("base", flag.ExitOnError)
		re := fs.Bool("rebuild", false, "")
		_ = fs.Parse(args[1:])
		return m.BuildBase(ctx, *re)
	case "create":
		if err := need(1); err != nil {
			return err
		}
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		repo := fs.String("repo", "", "Git-URL, wird nach ~/work/main geklont")
		imp := fs.String("import", "", "bestehender Ordner im Home, wird nach ~/work/main KOPIERT (Sperrliste, ohne .env/Schlüssel)")
		_ = fs.Parse(args[1:])
		if *imp != "" {
			bl, err := index.LoadBlocklist(filepath.Join(home, "blocklist.json"))
			if err != nil {
				return err
			}
			m.Imp = importer.Importer{Home: h, BL: func() index.Blocklist { return bl }}
		}
		return m.Create(ctx, args[0], *repo, *imp)
	case "start":
		if err := need(1); err != nil {
			return err
		}
		return m.Start(ctx, args[0])
	case "stop":
		if err := need(1); err != nil {
			return err
		}
		return m.Stop(ctx, args[0])
	case "delete":
		if err := need(1); err != nil {
			return err
		}
		fs := flag.NewFlagSet("delete", flag.ExitOnError)
		f := fs.Bool("force", false, "")
		_ = fs.Parse(args[1:])
		return m.Delete(ctx, args[0], *f)
	case "list":
		return list(ctx, m)
	case "agent":
		if err := need(3); err != nil || args[0] != "add" {
			return errors.New("nutze: agent add <session> <agent>")
		}
		return m.AgentAdd(ctx, args[1], args[2])
	case "login", "attach", "shell":
		if err := need(1); err != nil {
			return err
		}
		s, err := st.GetSession(args[0])
		if err != nil {
			return err
		}
		inner := map[string][]string{
			"login":  {"bash", "-lc", "claude auth login"},
			"attach": {"bash", "-lc", "tmux attach -t bf || tmux new -s bf"},
			"shell":  {"bash", "-l"},
		}[cmd]
		lc, err := exec.LookPath("limactl")
		if err != nil {
			return err
		}
		_ = st.Audit("cli", "session."+cmd, args[0], "", "")
		return syscall.Exec(lc, append([]string{"limactl", "shell", s.VM, "--"}, inner...), os.Environ())
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		noDialog := fs.Bool("no-dialog", false, "keinen macOS-Dialog zeigen (nur CLI-Entscheidung)")
		knownHosts := fs.String("known-hosts", filepath.Join(h, ".ssh", "known_hosts"), "known_hosts für Host-Key-Prüfung der Ziele")
		upstream := fs.String("upstream-agent", os.Getenv("SSH_AUTH_SOCK"), "ssh-agent-Socket mit deinen echten Keys")
		_ = fs.Parse(args)
		return serve(ctx, m, home, h, *noDialog, *upstream, *knownHosts)
	case "requests":
		return requests(ctx, home, args)
	case "ssh":
		return sshCmd(ctx, home, args)
	case "audit":
		fs := flag.NewFlagSet("audit", flag.ExitOnError)
		n := fs.Int("n", 50, "")
		_ = fs.Parse(args)
		es, err := st.AuditList(*n)
		if err != nil {
			return err
		}
		for _, e := range es {
			fmt.Printf("%s  %-8s %-16s %s/%s %s\n", e.TS.Format("2006-01-02 15:04:05"), e.Actor, e.Event, e.Session, e.Agent, e.Detail)
		}
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unbekannter Befehl %q", cmd)
	}
}

func list(ctx context.Context, m *session.Manager) error {
	ss, err := m.S.ListSessions()
	if err != nil {
		return err
	}
	vms, err := m.L.List(ctx)
	if err != nil {
		return err
	}
	status := map[string]string{}
	for _, v := range vms {
		status[v.Name] = v.Status
	}
	fmt.Printf("%-16s %-10s %-4s %s\n", "SESSION", "STATUS", "TPL", "AGENTS")
	for _, s := range ss {
		st := status[s.VM]
		if st == "" {
			st = "fehlt"
		}
		ags, _ := m.S.ListAgents(s.Name)
		names := ""
		for _, a := range ags {
			names += a.Name + " "
		}
		fmt.Printf("%-16s %-10s v%-3d %s\n", s.Name, st, s.TemplateVersion, names)
	}
	return nil
}

func serve(ctx context.Context, m *session.Manager, home, userHome string, noDialog bool, upstream, knownHosts string) error {
	lock, err := os.OpenFile(filepath.Join(home, "serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("bergfriedd serve läuft bereits")
	}
	bl, err := index.LoadBlocklist(filepath.Join(home, "blocklist.json"))
	if err != nil {
		return err
	}
	ix, err := index.New(userHome, bl)
	if err != nil {
		return err
	}
	go func() {
		t0 := time.Now()
		if err := ix.Refresh(); err != nil {
			fmt.Fprintln(os.Stderr, "Index-Aufbau:", err)
			return
		}
		fmt.Printf("Index bereit (%s)\n", time.Since(t0).Round(time.Second))
		for range time.Tick(15 * time.Minute) {
			if err := ix.Refresh(); err != nil {
				fmt.Fprintln(os.Stderr, "Index-Refresh:", err)
			}
		}
	}()
	svc := access.New(m.S, ix, home, nil)
	if !noDialog {
		svc.Asker = &notify.Dialog{Decide: svc.Decide, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
	}
	if upstream == "" {
		fmt.Fprintln(os.Stderr, "WARNUNG: kein Upstream-ssh-agent (SSH_AUTH_SOCK / --upstream-agent): SSH-Freigaben gehen nicht.")
	}
	hc, err := broker.LoadHostConfig(filepath.Join(home, "hosts.json"))
	if err != nil {
		return err
	}
	br := &broker.Broker{St: m.S, Up: broker.SocketUpstream{Path: upstream}}
	sshSvc := broker.NewService(br, broker.ResolveHostKeys(knownHosts),
		session.Hooks{M: m, Render: broker.RenderSSHConfig}, nil, hc)
	if !noDialog {
		sshSvc.Asker = &notify.SSHDialog{Decide: sshSvc.DecideSSH, Keys: sshSvc.Keys, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
		br.ConfirmEach = notify.ConfirmEach
	}
	d := &rpc.Daemon{St: m.S, Svc: svc, SSH: sshSvc, AdminSock: filepath.Join(home, "admin.sock"), L: m.L}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	m.Imp = importer.Importer{Home: userHome, BL: ix.Blocklist}
	am := *m
	am.Actor = "app"
	apiSrv := &api.Server{M: &am, Access: svc, SSH: sshSvc, Ix: ix, BlocklistFile: filepath.Join(home, "blocklist.json"), Upstream: upstream}
	go func() {
		if err := apiSrv.Serve(ctx, filepath.Join(home, "app.sock")); err != nil {
			fmt.Fprintln(os.Stderr, "App-Schnittstelle:", err)
		}
	}()
	_ = m.S.Audit("daemon", "serve.start", "", "", fmt.Sprintf("dialog=%v", !noDialog))
	fmt.Println("bergfriedd serve läuft (Strg-C beendet). Admin-Socket:", d.AdminSock)
	err = d.Run(ctx)
	_ = m.S.Audit("daemon", "serve.stop", "", "", "")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func requests(ctx context.Context, home string, args []string) error {
	conn, err := grpc.NewClient("unix://"+filepath.Join(home, "admin.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	c := pb.NewAdminClient(conn)
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		fs := flag.NewFlagSet("requests list", flag.ExitOnError)
		all := fs.Bool("all", false, "auch erledigte")
		if len(args) > 1 {
			_ = fs.Parse(args[1:])
		}
		r, err := c.ListRequests(ctx, &pb.ListRequestsRequest{OnlyPending: !*all})
		if err != nil {
			return fmt.Errorf("Daemon nicht erreichbar (läuft `bergfriedd serve`?): %w", err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATUS\tSESSION/AGENT\tART\tPFAD\tGRÖSSE\tBEGRÜNDUNG")
		for _, q := range r.Requests {
			fmt.Fprintf(tw, "%d\t%s\t%s/%s\t%s\t~/%s\t%d Dat., %.1f MB\t%s\n", q.Id, q.Status, q.Session, q.Agent, q.Kind, q.Path, q.TotalFiles, float64(q.TotalBytes)/1048576, q.Reason)
		}
		return tw.Flush()
	case "allow", "deny":
		fs := flag.NewFlagSet("requests "+sub, flag.ExitOnError)
		big := fs.Bool("confirm-large", false, "zweite Bestätigung für große Ordner")
		if len(args) < 2 {
			return errors.New("nutze: requests " + sub + " <id> [--confirm-large]")
		}
		var id int64
		if _, err := fmt.Sscan(args[1], &id); err != nil {
			return errors.New("ungültige ID")
		}
		_ = fs.Parse(args[2:])
		r, err := c.Decide(ctx, &pb.DecideRequest{Id: id, Allow: sub == "allow", ConfirmLarge: *big})
		if err != nil {
			return err
		}
		fmt.Println(r.Message)
		return nil
	}
	return errors.New("nutze: requests [list|allow|deny]")
}

func sshCmd(ctx context.Context, home string, args []string) error {
	conn, err := grpc.NewClient("unix://"+filepath.Join(home, "admin.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	c := pb.NewAdminClient(conn)
	if len(args) == 0 {
		return errors.New("nutze: ssh keys | grants [--all] | allow <id> [--minutes N] [--key SHA256:…] [--user U] [--all-agents] [--per-connection] [--trust-host-key] | deny <id> | revoke <grant> | extend <grant> --minutes N")
	}
	num := func(i int) (int64, error) {
		var n int64
		if len(args) <= i {
			return 0, errors.New("ID fehlt")
		}
		_, err := fmt.Sscan(args[i], &n)
		return n, err
	}
	unreachable := func(err error) error { return fmt.Errorf("Daemon nicht erreichbar oder abgelehnt: %w", err) }
	switch args[0] {
	case "keys":
		r, err := c.ListKeys(ctx, &pb.ListKeysRequest{})
		if err != nil {
			return unreachable(err)
		}
		if r.Error != "" {
			return errors.New(r.Error)
		}
		for _, k := range r.Keys {
			fmt.Printf("%s  %s  (%s)\n", k.Fingerprint, k.Comment, k.Type)
		}
	case "grants":
		fs := flag.NewFlagSet("ssh grants", flag.ExitOnError)
		all := fs.Bool("all", false, "auch abgelaufene/entzogene")
		_ = fs.Parse(args[1:])
		r, err := c.ListGrants(ctx, &pb.ListGrantsRequest{OnlyActive: !*all})
		if err != nil {
			return unreachable(err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSESSION\tAGENT\tHOST\tUSER\tBIS\tMODUS\tSTATUS")
		for _, g := range r.Grants {
			st := "aktiv"
			if g.Revoked {
				st = "entzogen"
			} else if time.Unix(g.ExpiresUnix, 0).Before(time.Now()) {
				st = "abgelaufen"
			}
			ag := g.Agent
			if ag == "" {
				ag = "(alle)"
			}
			mode := "still"
			if g.PerConnection {
				mode = "einzeln"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", g.Id, g.Session, ag, g.Host, g.User, time.Unix(g.ExpiresUnix, 0).Format("01-02 15:04"), mode, st)
		}
		return tw.Flush()
	case "allow", "deny":
		id, err := num(1)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("ssh allow", flag.ExitOnError)
		minutes := fs.Int("minutes", 15, "Dauer")
		key := fs.String("key", "", "Key-Fingerprint (SHA256:…); sonst Standard des Hosts")
		user := fs.String("user", "", "Benutzer auf dem Ziel (Standard: claude)")
		allAg := fs.Bool("all-agents", false, "für alle Agents der Session")
		per := fs.Bool("per-connection", false, "jede Verbindung einzeln bestätigen")
		trust := fs.Bool("trust-host-key", false, "neuen Host-Key bestätigen")
		_ = fs.Parse(args[2:])
		r, err := c.DecideSSH(ctx, &pb.DecideSSHRequest{Id: id, Allow: args[0] == "allow", Minutes: int32(*minutes), KeyFingerprint: *key,
			User: *user, AllAgents: *allAg, PerConnection: *per, TrustUnknownHost: *trust})
		if err != nil {
			return err
		}
		fmt.Println(r.Message)
	case "revoke":
		id, err := num(1)
		if err != nil {
			return err
		}
		r, err := c.RevokeGrant(ctx, &pb.GrantRef{Id: id})
		if err != nil {
			return err
		}
		fmt.Println(r.Message)
	case "extend":
		id, err := num(1)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("ssh extend", flag.ExitOnError)
		minutes := fs.Int("minutes", 15, "zusätzliche Minuten")
		_ = fs.Parse(args[2:])
		r, err := c.ExtendGrant(ctx, &pb.ExtendGrantRequest{Id: id, Minutes: int32(*minutes)})
		if err != nil {
			return err
		}
		fmt.Println(r.Message)
	default:
		return errors.New("unbekannter Unterbefehl")
	}
	return nil
}
