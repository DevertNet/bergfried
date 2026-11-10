package sandbox

import (
	"fmt"
	"strings"
)

// Runtime is the agent CLI that runs in a tmux window.
type Runtime string

const (
	RuntimeClaude   Runtime = "claude"
	RuntimeOpenCode Runtime = "opencode"
)

// OpenCodeVersion is pinned so that a sandbox keeps a tested version. Users can
// still change the template or install a newer OpenCode by hand.
const OpenCodeVersion = "2.2.6"

// ParseRuntime accepts the stored/API value. Empty means Claude (old rows, old app).
func ParseRuntime(s string) (Runtime, error) {
	switch Runtime(strings.ToLower(strings.TrimSpace(s))) {
	case "", RuntimeClaude:
		return RuntimeClaude, nil
	case RuntimeOpenCode:
		return RuntimeOpenCode, nil
	}
	return "", fmt.Errorf("unknown agent runtime %q (allowed: claude, opencode)", s)
}

func (r Runtime) Valid() bool { return r == RuntimeClaude || r == RuntimeOpenCode }

func (r Runtime) DisplayName() string {
	if r == RuntimeOpenCode {
		return "OpenCode"
	}
	return "Claude Code"
}

// ProcessName is the comm/argv0 of the foreground process in the agent window.
func (r Runtime) ProcessName() string {
	if r == RuntimeOpenCode {
		return "opencode"
	}
	return "claude"
}

// LoginCommand runs in an interactive terminal in the VM.
func (r Runtime) LoginCommand() string {
	if r == RuntimeOpenCode {
		return `"$HOME/.opencode/bin/opencode" auth login`
	}
	return `"$HOME/.local/bin/claude" auth login`
}

// AgentCmd builds the shell line for a tmux window. Names are validated (only [a-z0-9-]).
// --continue restores the history; if there is none (new agent) the fallback starts fresh.
// Claude gets its agent identity through BERGFRIED_AGENT; OpenCode does not, because its
// shared server starts the MCP processes and the environment of the first client would leak
// to all other agents. The MCP server derives the agent from its working directory there.
func (r Runtime) AgentCmd(sandbox, agent string) string {
	env := fmt.Sprintf("export COMPOSE_PROJECT_NAME=%s-%s", sandbox, agent)
	if r == RuntimeOpenCode {
		return fmt.Sprintf(`cd ~/work/%[2]s && %[3]s && (~/.opencode/bin/opencode --continue || exec ~/.opencode/bin/opencode)`, sandbox, agent, env)
	}
	rc := fmt.Sprintf("--remote-control '%s/%s'", sandbox, agent)
	return fmt.Sprintf(`cd ~/work/%[2]s && %[3]s BERGFRIED_AGENT=%[1]s/%[2]s && (~/.local/bin/claude --continue %[4]s || exec ~/.local/bin/claude %[4]s)`, sandbox, agent, env, rc)
}
