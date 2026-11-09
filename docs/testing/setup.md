# Manual test: first sandbox (CLI)

Prerequisites: Docker running, `brew install lima`. Go is not needed locally.

```bash
make test build
export BERGFRIED_TEMPLATE=vm/bergfried.yaml
./bin/bergfriedd base build          # once, a few minutes (image, apt, Docker, Claude Code)
```

1. `./bin/bergfriedd create shop` — clones the base VM (seconds), starts it, renews machine id and SSH host keys. With a repo: `--repo https://github.com/<user>/<repo>.git` (lands in `~/work/main`).
2. `./bin/bergfriedd login shop` — **once per VM**: `claude auth login` (claude.ai subscription). Remote Control does not work with `setup-token`.
3. `./bin/bergfriedd agent add shop main` — tmux window, worktree, `COMPOSE_PROJECT_NAME=shop-main`, `claude --remote-control 'shop/main'`.
4. Open the agent `shop/main` in the Claude app (phone) or claude.ai/code and write something.
5. `stop shop`, then `start shop`: agents restart with `claude --continue`, the history is there.
6. `delete shop` warns about unpushed commits (`--force` skips). `list` and `audit` show status and log.

## Checks
- In the VM `mount | grep shared` shows exactly two virtiofs mounts, `in` with `ro`; `ls /Users` shows nothing of your home.
- `docker ps` works in the VM; no VM Docker socket is forwarded to the Mac.
- Two sandboxes have different `/etc/machine-id`.

## Notes
- The first start of Claude in a fresh clone shows onboarding (theme) until `login` has run.
- The VM user has passwordless `sudo` (Lima default). The boundary is the VM, not the Linux user.
