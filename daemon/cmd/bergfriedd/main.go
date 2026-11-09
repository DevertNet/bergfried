// bergfriedd: CLI and daemon for managing Bergfried sandboxes.
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
	"github.com/DevertNet/bergfried/daemon/internal/ghtoken"
	"github.com/DevertNet/bergfried/daemon/internal/github"
	"github.com/DevertNet/bergfried/daemon/internal/importer"
	"github.com/DevertNet/bergfried/daemon/internal/index"
	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/notify"
	"github.com/DevertNet/bergfried/daemon/internal/rpc"
	"github.com/DevertNet/bergfried/daemon/internal/sandbox"
	"github.com/DevertNet/bergfried/daemon/internal/store"
	pb "github.com/DevertNet/bergfried/proto/gen/bergfried/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const usage = `bergfriedd – Bergfried CLI

    base build [--rebuild]        build the base VM from the template
    create <name> [--repo URL | --import FOLDER]   clone a sandbox from the base VM
  start|stop <name>
    delete <name> [--force]       warns about unpushed commits
    list                          sandboxes with VM status and agents
    agent add <sandbox> <agent>   new agent (tmux window + worktree + Remote Control)
    login <sandbox>               claude auth login in the VM (once per VM)
    attach <sandbox>              open the sandbox's tmux
    shell <sandbox>               shell in the VM
    audit [-n 50]                 audit log
    serve [--no-dialog]           daemon: index, sockets per sandbox, approval dialog (runs in the foreground)
    ssh keys|grants|allow|deny|revoke|extend   SSH grants
    requests [list|allow|deny] [id] [--confirm-large] [--all]   show/decide requests

Environment: BERGFRIED_HOME (default ~/.bergfried), BERGFRIED_TEMPLATE (default vm/bergfried.yaml)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
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
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := sandbox.MigrateHome(home); err != nil {
		return err
	}
	st, err := store.Open(home)
	if err != nil {
		return err
	}
	m := &sandbox.Manager{L: lima.Client{R: lima.ExecRunner{}}, S: st, Home: home,
		Template: getenv("BERGFRIED_TEMPLATE", "vm/bergfried.yaml"), Actor: "cli"}
	// Template in the data directory (editable in the app); CLI and daemon use the same revision.
	ts := sandbox.TemplateStore{Dir: filepath.Join(home, "template"), Seed: m.Template, L: m.L}
	if err := ts.Ensure(); err == nil {
		m.Tpl = &ts
	} else if cmd == "base" || cmd == "create" || cmd == "serve" {
		return fmt.Errorf("Vorlage: %w", err)
	}
	need := func(n int) error {
		if len(args) < n {
			return errors.New("too few arguments\n\n" + usage)
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
		repo := fs.String("repo", "", "git URL, cloned to ~/work/main")
		imp := fs.String("import", "", "existing folder in home, COPIED to ~/work/main (blocklist, without .env/keys)")
		_ = fs.Parse(args[1:])
		if *imp != "" {
			bl, err := index.LoadBlocklist(filepath.Join(home, "blocklist.json"))
			if err != nil {
				return err
			}
			m.Imp = importer.Importer{Home: h, BL: func() index.Blocklist { return bl }}
		}
		return m.Create(ctx, args[0], *repo, *imp, "")
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
			return errors.New("nutze: agent add <sandbox> <agent>")
		}
		return m.AgentAdd(ctx, args[1], args[2])
	case "login", "attach", "shell":
		if err := need(1); err != nil {
			return err
		}
		s, err := st.GetSandbox(args[0])
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
		_ = st.Audit("cli", "sandbox."+cmd, args[0], "", "")
		return syscall.Exec(lc, append([]string{"limactl", "shell", s.VM, "--"}, inner...), os.Environ())
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		noDialog := fs.Bool("no-dialog", false, "do not show a macOS dialog (CLI decisions only)")
		knownHosts := fs.String("known-hosts", filepath.Join(h, ".ssh", "known_hosts"), "known_hosts for host key checks of the targets")
		upstream := fs.String("upstream-agent", os.Getenv("SSH_AUTH_SOCK"), "ssh-agent socket with your real keys")
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
			fmt.Printf("%s  %-8s %-16s %s/%s %s\n", e.TS.Format("2006-01-02 15:04:05"), e.Actor, e.Event, e.Sandbox, e.Agent, e.Detail)
		}
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unbekannter Befehl %q", cmd)
	}
}

func list(ctx context.Context, m *sandbox.Manager) error {
	ss, err := m.S.ListSandboxes()
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
	fmt.Printf("%-16s %-10s %-4s %s\n", "SANDBOX", "STATUS", "TPL", "AGENTS")
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

func serve(ctx context.Context, m *sandbox.Manager, home, userHome string, noDialog bool, upstream, knownHosts string) error {
	lock, err := os.OpenFile(filepath.Join(home, "serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("bergfriedd serve is already running")
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
		fmt.Printf("Index ready (%s)\n", time.Since(t0).Round(time.Second))
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
		fmt.Fprintln(os.Stderr, "WARNING: no upstream ssh-agent (SSH_AUTH_SOCK / --upstream-agent): SSH grants will not work.")
	}
	hc, err := broker.LoadHostConfig(filepath.Join(home, "hosts.json"))
	if err != nil {
		return err
	}
	br := &broker.Broker{St: m.S, Up: broker.SocketUpstream{Path: upstream}}
	sshSvc := broker.NewService(br, broker.ResolveHostKeys(knownHosts),
		sandbox.Hooks{M: m, Render: broker.RenderSSHConfig}, nil, hc)
	if !noDialog {
		sshSvc.Asker = &notify.SSHDialog{Decide: sshSvc.DecideSSH, Keys: sshSvc.Keys, Log: func(s string) { fmt.Fprintln(os.Stderr, s) }}
		br.ConfirmEach = notify.ConfirmEach
	}
	// GitHub: one fine-grained token per sandbox (lives in the VM, copy on the Mac for repo checks; see docs/GITHUB.md).
	tokStore := ghtoken.Store{Home: home}
	mkClient := func(tok string) *github.Client {
		c := github.New(github.StaticToken(tok))
		if v := os.Getenv("BERGFRIED_GITHUB_API"); v != "" { // development/tests only: local GitHub stand-in
			c.Base = v
		}
		return c
	}
	ghSvc := ghtoken.NewService(m.S, tokStore, func(ctx context.Context, sandbox, tok string) error {
		s, err := m.S.GetSandbox(sandbox)
		if err != nil {
			return err
		}
		return m.InstallGitToken(ctx, s.VM, tok)
	})
	ghSvc.Client = mkClient
	m.PreCheck = func(ctx context.Context, token, repoURL string) error {
		if err := ghSvc.Validate(ctx, token); err != nil {
			return err
		}
		if r, err := github.ParseRemote(repoURL); err == nil {
			return ghSvc.CheckRepo(ctx, token, r)
		}
		return nil
	}
	m.SaveToken = tokStore.Set
	m.DropToken = tokStore.Delete
	d := &rpc.Daemon{St: m.S, Svc: svc, SSH: sshSvc, GH: ghSvc, AdminSock: filepath.Join(home, "admin.sock"), L: m.L}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	m.Imp = importer.Importer{Home: userHome, BL: ix.Blocklist}
	am := *m
	am.Actor = "app"
	mon := api.NewMonitor(&am)
	go mon.Run(ctx)
	apiSrv := &api.Server{GH: ghSvc, Mon: mon, M: &am, Access: svc, SSH: sshSvc, Ix: ix, BlocklistFile: filepath.Join(home, "blocklist.json"), Upstream: upstream}
	go func() {
		if err := apiSrv.Serve(ctx, filepath.Join(home, "app.sock")); err != nil {
			fmt.Fprintln(os.Stderr, "App-Schnittstelle:", err)
		}
	}()
	_ = m.S.Audit("daemon", "serve.start", "", "", fmt.Sprintf("dialog=%v", !noDialog))
	fmt.Println("bergfriedd serve is running (Ctrl-C stops it). Admin socket:", d.AdminSock)
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
		all := fs.Bool("all", false, "include finished")
		if len(args) > 1 {
			_ = fs.Parse(args[1:])
		}
		r, err := c.ListRequests(ctx, &pb.ListRequestsRequest{OnlyPending: !*all})
		if err != nil {
			return fmt.Errorf("daemon not reachable (is `bergfriedd serve` running?): %w", err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATUS\tSANDBOX/AGENT\tKIND\tPATH\tSIZE\tREASON")
		for _, q := range r.Requests {
			fmt.Fprintf(tw, "%d\t%s\t%s/%s\t%s\t~/%s\t%d files, %.1f MB\t%s\n", q.Id, q.Status, q.Sandbox, q.Agent, q.Kind, q.Path, q.TotalFiles, float64(q.TotalBytes)/1048576, q.Reason)
		}
		return tw.Flush()
	case "allow", "deny":
		fs := flag.NewFlagSet("requests "+sub, flag.ExitOnError)
		big := fs.Bool("confirm-large", false, "second confirmation for large folders")
		if len(args) < 2 {
			return errors.New("usage: requests " + sub + " <id> [--confirm-large]")
		}
		var id int64
		if _, err := fmt.Sscan(args[1], &id); err != nil {
			return errors.New("invalid ID")
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
	unreachable := func(err error) error { return fmt.Errorf("daemon not reachable or refused: %w", err) }
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
		all := fs.Bool("all", false, "include expired/revoked")
		_ = fs.Parse(args[1:])
		r, err := c.ListGrants(ctx, &pb.ListGrantsRequest{OnlyActive: !*all})
		if err != nil {
			return unreachable(err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSANDBOX\tAGENT\tHOST\tUSER\tUNTIL\tMODE\tSTATUS")
		for _, g := range r.Grants {
			st := "active"
			if g.Revoked {
				st = "revoked"
			} else if time.Unix(g.ExpiresUnix, 0).Before(time.Now()) {
				st = "expired"
			}
			ag := g.Agent
			if ag == "" {
				ag = "(all)"
			}
			mode := "silent"
			if g.PerConnection {
				mode = "each"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", g.Id, g.Sandbox, ag, g.Host, g.User, time.Unix(g.ExpiresUnix, 0).Format("01-02 15:04"), mode, st)
		}
		return tw.Flush()
	case "allow", "deny":
		id, err := num(1)
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("ssh allow", flag.ExitOnError)
		minutes := fs.Int("minutes", 15, "Dauer")
		key := fs.String("key", "", "key fingerprint (SHA256:…); otherwise the host default")
		user := fs.String("user", "", "user on the target (default: claude)")
		allAg := fs.Bool("all-agents", false, "for all agents of the sandbox")
		per := fs.Bool("per-connection", false, "confirm every connection individually")
		trust := fs.Bool("trust-host-key", false, "confirm a new host key")
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
		minutes := fs.Int("minutes", 15, "additional minutes")
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
