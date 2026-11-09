# Working rules for agents in this repo

Project **Bergfried**: Claude Code agents isolated in Lima VMs (Mac; Debian VPS later), with approvals for files/SSH/GitHub.
The repository is **English** (docs, code comments, commit messages, app strings via localisation). **Talk to the user in the language they write in** (usually German).

**Read first:** [`README.md`](README.md), [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md), [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md) (conventions, decisions, pitfalls), [`docs/ROADMAP.md`](docs/ROADMAP.md) (what is next), [`docs/SECURITY.md`](docs/SECURITY.md).

## Terminology
An isolated VM with its agents is a **sandbox** — everywhere (app, API `/v1/sandboxes`, code, DB, folder `~/.bergfried/sandboxes`). It was called "session" earlier;
the word remains only for other programs' things (tmux session `bf`, Claude/Remote Control sessions, OpenSSH's `session-bind@openssh.com` — never rename that).

## How to work
- **Plan first** for larger changes: show the plan (files, commands, tests) briefly and ask if something is unclear.
- Small, testable steps; manual test guides live in `docs/testing/`.
- **Do not guess about external tools** (Lima, OpenSSH, Claude Code, macOS, GitHub): check docs/`--help`/logs and record the result.
- Security-relevant code (broker, blocklist, socket permissions, importer, template check, updater) **always with tests**.
- Report honestly what is **not** verified (for example UI never seen).

## Build and test (all Go runs in Docker; no local Go needed)
```bash
make test          # go test ./... in golang:1.26
make vet && make fmt
make build         # bin/bergfriedd (darwin/arm64), bin/bergfried-mcp (linux/arm64)
make proto         # regenerate protobuf/gRPC; generated code is checked in (proto/gen)
app/build.sh       # app/build/Bergfried.app (swiftc; no Xcode, no SwiftPM)
```
Run the daemon by hand (the app can also install it as a LaunchAgent):
```bash
export BERGFRIED_HOME=$HOME/.bergfried BERGFRIED_TEMPLATE=$PWD/vm/bergfried.yaml
./bin/bergfriedd serve --no-dialog
```

## Testing with real VMs and SSH
- Create test sandboxes with unique names and **delete them afterwards** (`bergfriedd delete <name> --force`). **Never change or delete the user's existing sandboxes** (read only).
- **Never test SSH with the user's real keys**: own key, own `ssh-agent` (short socket path), test sshd in Docker — see `docs/DEVELOPMENT.md`.
- **Never use the user's real GitHub token or repos for tests**; use `tools/fakegithub` (`docs/testing/github.md`).

## Git
- Work on branches `agent/<topic>`; merge to `main` as the user asks. No secrets in commits; nothing from `~/.bergfried`.
- Short English commit subjects; update docs in the same commit when behaviour changes.

## Pitfalls (short; details in docs/DEVELOPMENT.md)
- zsh: `/usr/bin/log` instead of `log`; BSD `sed -i ''`. gofmt realigns structs (check before scripted replacements).
- `INSERT` with explicit column lists; new columns via `ALTER TABLE` (migration in `store.migrate`).
- `limactl`: separate stdout/stderr; delete the pinned host key after `firstBoot`; template file names without a leading dot.
- App: no `@State` (macro without plugin) → `@Local`; **do not quit/restart the app while a macOS permission prompt is open.**
