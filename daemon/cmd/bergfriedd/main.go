// bergfriedd (M1): CLI zum Verwalten von Bergfried-Sessions.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/DevertNet/bergfried/daemon/internal/lima"
	"github.com/DevertNet/bergfried/daemon/internal/session"
	"github.com/DevertNet/bergfried/daemon/internal/store"
)

const usage = `bergfriedd – Bergfried-CLI (M1)

  base build [--rebuild]        Basis-VM aus der Vorlage bauen
  create <name> [--repo URL]    Session aus Basis-VM klonen
  start|stop <name>
  delete <name> [--force]       warnt bei ungepushten Commits
  list                          Sessions mit VM-Status und Agents
  agent add <session> <agent>   neuer Agent (tmux-Fenster + Worktree + Remote Control)
  login <session>               claude auth login in der VM (einmal pro VM)
  attach <session>              tmux der Session öffnen
  shell <session>               Shell in der VM
  audit [-n 50]                 Audit-Log

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
		_ = fs.Parse(args[1:])
		return m.Create(ctx, args[0], *repo)
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
