# Manual test: first sandbox (CLI)

Prerequisites: Docker running, `brew install lima`. Go is not needed locally.

```bash
make test build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd base build          # once, a few minutes (image, apt, Docker, Claude Code, OpenCode)
```

1. `./bin/bergfriedd create shop` — clones the base VM (seconds), starts it, renews machine id and SSH host keys. With a repo: `--repo https://github.com/<user>/<repo>.git` (lands in `~/work/main`).
2. `./bin/bergfriedd login shop` — **once per VM and runtime**: `claude auth login` (claude.ai subscription). For OpenCode: `./bin/bergfriedd login shop opencode` (`opencode auth login`). Remote Control does not work with `setup-token`.
3. `./bin/bergfriedd claude start shop work/main` — starts Claude in the existing folder `~/work/main` (no git worktree, no branch): tmux window, `COMPOSE_PROJECT_NAME=shop-main`, `claude --remote-control 'shop/main'`. `claude stop|restart shop work/main` controls it; `claude list shop` shows entries.
5. For OpenCode: `./bin/bergfriedd opencode start shop` — **one service per sandbox**, independent of Claude folders. `opencode stop shop` stops it. `./bin/bergfriedd pair shop` prints a one-time link for the OpenCode desktop app (valid 5 minutes; the daemon uses a dedicated port 42xxx).
6. Open Claude in the Claude app (phone) or claude.ai/code. `stop shop`, then `start shop`: Claude entries restart with `--continue`, the history is there; stale windows of other entries are killed.
7. `delete shop` warns about unpushed commits (`--force` skips). `list` and `audit` show status and log.

## Checks
- In the VM `mount | grep shared` shows exactly two virtiofs mounts, `in` with `ro`; `ls /Users` shows nothing of your home.
- `docker ps` works in the VM; no VM Docker socket is forwarded to the Mac.
- Two sandboxes have different `/etc/machine-id`.
- OpenCode: `command -v opencode` and `~/.opencode/bin/opencode --version` work; `opencode pair` (or **Connect desktop** in the app) returns a link on a port in 42000–42999, not 49374.
- OpenCode is one service (`pgrep -a opencode`) regardless of how many folders exist; each project gets its own MCP process.

## Notes
- The first start of Claude in a fresh clone shows onboarding (theme) until `login` has run. OpenCode without a provider login shows its `/connect` flow; the app uses the login for the next Claude start; a fresh OpenCode login is picked up by the service.
- The VM user has passwordless `sudo` (Lima default). The boundary is the VM, not the Linux user.
