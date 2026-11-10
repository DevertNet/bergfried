# Manual test: first sandbox (CLI)

Prerequisites: Docker running, `brew install lima`. Go is not needed locally.

```bash
make test build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd base build          # once, a few minutes (image, apt, Docker, Claude Code, OpenCode)
```

1. `./bin/bergfriedd create shop` — clones the base VM (seconds), starts it, renews machine id and SSH host keys. With a repo: `--repo https://github.com/<user>/<repo>.git` (lands in `~/work/main`).
2. `./bin/bergfriedd login shop` — **once per VM and runtime**: `claude auth login` (claude.ai subscription). For OpenCode: `./bin/bergfriedd login shop opencode` (`opencode auth login`). Remote Control does not work with `setup-token`.
3. `./bin/bergfriedd agent add shop main` — Claude agent by default: tmux window, worktree, `COMPOSE_PROJECT_NAME=shop-main`, `claude --remote-control 'shop/main'`.
4. For OpenCode: `./bin/bergfriedd agent add shop oc --runtime opencode` — same window/worktree model, `opencode --continue`. Both runtimes can run side by side.
5. Open a Claude agent in the Claude app (phone) or claude.ai/code. For OpenCode: `./bin/bergfriedd pair shop` prints a one-time link for the OpenCode desktop app (valid 5 minutes; the daemon uses a dedicated port 42xxx).
6. `stop shop`, then `start shop`: agents restart with `--continue`, the history is there.
7. `delete shop` warns about unpushed commits (`--force` skips). `list` and `audit` show status and log.

## Checks
- In the VM `mount | grep shared` shows exactly two virtiofs mounts, `in` with `ro`; `ls /Users` shows nothing of your home.
- `docker ps` works in the VM; no VM Docker socket is forwarded to the Mac.
- Two sandboxes have different `/etc/machine-id`.
- OpenCode: `command -v opencode` and `~/.opencode/bin/opencode --version` work; `opencode pair` (or the app Connect button) returns a link on a port in 42000–42999, not 49374.
- Two OpenCode agents: both tmux windows connect to **one** shared service (`pgrep -a opencode`), each project gets its own MCP process.

## Notes
- The first start of Claude in a fresh clone shows onboarding (theme) until `login` has run. OpenCode without a provider login shows its `/connect` flow; the app starts the agents again after a login.
- The VM user has passwordless `sudo` (Lima default). The boundary is the VM, not the Linux user.
