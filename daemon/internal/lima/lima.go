// Package lima kapselt limactl. Alle Aufrufe laufen über Runner, damit Tests ohne Lima auskommen.
package lima

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type ExecRunner struct{ Bin string }

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "limactl"
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("limactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type Client struct{ R Runner }

type Instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"memory"`
	Disk   int64  `json:"disk"`
}

// CreateArgs baut den limactl-Aufruf: genau zwei Mounts, nie Home.
func CreateArgs(name, template, inDir, outDir string) []string {
	return []string{"create", "--name=" + name, "--tty=false",
		"--mount", inDir, "--mount", outDir + ":w", template}
}

func (c Client) Create(ctx context.Context, name, template, inDir, outDir string) error {
	_, err := c.R.Run(ctx, CreateArgs(name, template, inDir, outDir)...)
	return err
}
func (c Client) Start(ctx context.Context, name string) error {
	_, err := c.R.Run(ctx, "start", "--tty=false", name)
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

// Shell führt ein Kommando in der VM aus.
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
