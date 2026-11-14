// Package lima wraps limactl. All calls go through Runner so tests work without Lima.
package lima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type outputKey struct{}

// WithOutput attaches a callback that receives the output lines of long-running limactl calls (create, clone, start) while they run.
func WithOutput(ctx context.Context, f func(line string)) context.Context {
	return context.WithValue(ctx, outputKey{}, f)
}

// Output hands one line to the callback of the context (no-op without one).
func Output(ctx context.Context, line string) {
	if f, ok := ctx.Value(outputKey{}).(func(string)); ok && f != nil {
		f(line)
	}
}

var limaMsgRe = regexp.MustCompile(`level=(\w+) msg="((?:[^"\\]|\\.)*)"`)

// CleanLine turns limactl's logfmt (`time=… level=info msg="…"`) into the plain message; other lines are returned as they are.
func CleanLine(l string) string {
	m := limaMsgRe.FindStringSubmatch(l)
	if m == nil {
		return strings.TrimSpace(l)
	}
	msg := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(m[2])
	if m[1] != "info" && m[1] != "debug" {
		msg = m[1] + ": " + msg
	}
	return msg
}

// LineRunner is an optional capability of a Runner: stream output lines while the command runs.
type LineRunner interface {
	RunLines(ctx context.Context, onLine func(string), args ...string) ([]byte, error)
}

func (r ExecRunner) RunLines(ctx context.Context, onLine func(string), args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "limactl"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var errText bytes.Buffer
	var mu sync.Mutex
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		mu.Lock()
		errText.WriteString(line + "\n")
		mu.Unlock()
		if onLine != nil {
			if c := CleanLine(line); c != "" {
				onLine(c)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return out.Bytes(), fmt.Errorf("limactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errText.String()+out.String()))
	}
	return out.Bytes(), nil
}

// RunLines runs a limactl command and sends its output lines to the callback attached with WithOutput (falls back to Run).
func (c Client) RunLines(ctx context.Context, args ...string) ([]byte, error) {
	if lr, ok := c.R.(LineRunner); ok {
		f, _ := ctx.Value(outputKey{}).(func(string))
		return lr.RunLines(ctx, f, args...)
	}
	return c.R.Run(ctx, args...)
}

type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type ExecRunner struct{ Bin string }

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "limactl"
	}
	// stdout and stderr separated: warnings on stderr must not disturb the JSON output.
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("limactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()+string(out)))
	}
	return out, nil
}

// StdinRunner: optional capability of the runner to stream input data to limactl.
type StdinRunner interface {
	RunStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error)
}

func (r ExecRunner) RunStdin(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "limactl"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("limactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()+string(out)))
	}
	return out, nil
}

type Client struct{ R Runner }

// ShellStdin runs a command in the VM and feeds stdin.
func (c Client) ShellStdin(ctx context.Context, name string, stdin io.Reader, cmd ...string) ([]byte, error) {
	sr, ok := c.R.(StdinRunner)
	if !ok {
		return nil, fmt.Errorf("runner does not support stdin")
	}
	return sr.RunStdin(ctx, stdin, append([]string{"shell", "--tty=false", name, "--"}, cmd...)...)
}

type Instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"memory"`
	Disk   int64  `json:"disk"`
}

// CreateArgs builds the limactl call: exactly three mounts (shared/in ro, shared/out rw, skills ro), never home.
func CreateArgs(name, template, inDir, outDir, skills string) []string {
	return []string{"create", "--name=" + name, "--tty=false",
		"--mount", inDir, "--mount", outDir + ":w", "--mount", skills, template}
}

func (c Client) Create(ctx context.Context, name, template, inDir, outDir, skills string) error {
	_, err := c.R.Run(ctx, CreateArgs(name, template, inDir, outDir, skills)...)
	return err
}

// MountArgs is the --mount-only part of `limactl edit`/`limactl clone`: shared/in ro, shared/out rw, skills ro.
func MountArgs(in, out, skills string) []string {
	return []string{"--mount-only", in, "--mount-only", out + ":w", "--mount-only", skills}
}

// EditMounts replaces all mounts of a stopped instance (Lima refuses a running one).
func (c Client) EditMounts(ctx context.Context, name, in, out, skills string) error {
	args := append([]string{"edit", "--tty=false", name}, MountArgs(in, out, skills)...)
	_, err := c.R.Run(ctx, args...)
	return err
}

// HasMount reports whether the instance config (<instance dir>/lima.yaml) contains a mount with this location and writable flag.
func HasMount(name, location string, writable bool) (bool, error) {
	d, err := InstanceDir(name)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(filepath.Join(d, "lima.yaml"))
	if err != nil {
		return false, err
	}
	var cfg struct {
		Mounts []struct {
			Location string `yaml:"location"`
			Writable bool   `yaml:"writable"`
		} `yaml:"mounts"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return false, err
	}
	for _, m := range cfg.Mounts {
		if m.Location == location && m.Writable == writable {
			return true, nil
		}
	}
	return false, nil
}
func (c Client) Start(ctx context.Context, name string) error {
	_, err := c.RunLines(ctx, "start", "--tty=false", name)
	return err
}

// StartWithTimeout starts an instance and waits up to d for it to be running (Lima's default is 10 minutes, `limactl start --timeout`,
// available since Lima 2.0). The first start of a base VM runs the whole provisioning (apt, Docker, Claude Code) and can take longer.
func (c Client) StartWithTimeout(ctx context.Context, name string, d time.Duration) error {
	_, err := c.RunLines(ctx, "start", "--tty=false", "--timeout="+d.String(), name)
	return err
}

func (c Client) Stop(ctx context.Context, name string) error {
	_, err := c.R.Run(ctx, "stop", name)
	return err
}
func (c Client) Delete(ctx context.Context, name string) error {
	_, err := c.R.Run(ctx, "delete", "--force", name)
	return err
}

// Shell runs a command in the VM.
func (c Client) Shell(ctx context.Context, name string, cmd ...string) ([]byte, error) {
	return c.R.Run(ctx, append([]string{"shell", "--tty=false", name, "--"}, cmd...)...)
}

func (c Client) List(ctx context.Context) ([]Instance, error) {
	out, err := c.R.Run(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	var res []Instance
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var i Instance
		if err := json.Unmarshal([]byte(line), &i); err != nil {
			return nil, err
		}
		res = append(res, i)
	}
	return res, nil
}

// InstanceDir returns the Lima directory of an instance (respects LIMA_HOME).
func InstanceDir(name string) (string, error) {
	home := os.Getenv("LIMA_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(h, ".lima")
	}
	return filepath.Join(home, name), nil
}

// RepinHostKey deletes the SSH host key pinned by Lima so it is pinned again on the next start
// after the guest keys were deliberately regenerated.
func RepinHostKey(name string) error {
	d, err := InstanceDir(name)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(d, "ssh.known_hosts")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
