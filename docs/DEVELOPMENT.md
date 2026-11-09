# Development

Go runs in Docker (no local Go needed). Lima runs natively on the Mac. The app is built with `swiftc` (Command Line Tools only, no Xcode, no SwiftPM).

```bash
make test          # go test ./... in golang:1.26
make vet && make fmt
make build         # bin/bergfriedd (darwin/arm64), bin/bergfried-mcp (linux/arm64)
make proto         # regenerate protobuf/gRPC (Docker); generated code in proto/gen is checked in
make fakegithub    # bin/fakegithub, local GitHub stand-in for tests
app/build.sh       # app/build/Bergfried.app (VERSION=1.2.3 app/build.sh to stamp a version)
```
Run the daemon by hand (the app can also install it as a LaunchAgent):
```bash
export BERGFRIED_HOME=$HOME/.bergfried BERGFRIED_TEMPLATE=$PWD/vm/bergfried.yaml
./bin/bergfriedd serve --no-dialog
```

## Branches, CI and builds
- Work on `agent/<topic>` branches; `ci.yml` runs Go tests, `scripts/check-strings.py` (German translations) and a macOS app build on **every** branch and pull request.
- Merge into **`dev`** (pull request, CI green) to publish a dev pre-release (`dev-release.yml`); merge `dev` into `main` and tag `vX.Y.Z` for a stable release — **stable releases only on the explicit request of the maintainer** (agents too: `scripts/pr.sh merge <n> --user-approved` into `main` and `scripts/release.sh vX.Y.Z --user-approved` refuse without that flag).
- `scripts/pr.sh open <base> "<title>"` / `scripts/pr.sh merge <n>` (merges only with green CI) and `scripts/release.sh vX.Y.Z --user-approved [--dry-run]` (tags a green, up-to-date `main`) do the pull-request and release steps with plain curl + the token.
- `scripts/ci-status.sh [branch]` shows the latest runs and the end of a failed job's log with plain `curl` + the token from `~/.git-credentials` (works without `gh`; sandboxes also have `gh`).
- App strings: use `L("…")`/`LT("…")` (English key) and add the German text to `app/Resources/de.lproj/Localizable.strings`; text that is identical in German goes into `app/Resources/same-in-german.txt`; `scripts/check-strings.py` enforces both.

## Logs
- App: menu item **Diagnostics** (daemon log with start and failure of every action, updater log, Lima's host-agent log per VM; "Copy all"). Long errors in the orange banner are cut: "Show log" opens the full text.
- Files: `~/.bergfried/daemon.log` (stdout/stderr of the daemon; every job logs `started`, `done` or `FAILED: <full error>`), `~/.bergfried/update/update.log` (swap helper), `~/.lima/bergfried-<name>/ha.stderr.log` (Lima host agent; VM boot problems), and in the VM `/var/log/cloud-init-output.log` (provisioning).
- API: `GET /v1/log?source=daemon|update|lima[&vm=bergfried-<name>]` returns the last 256 KB (app socket only).

## Releasing
Stable releases only on the maintainer's explicit request (see above). Flow: PR `dev` → `main` (`scripts/pr.sh merge <n> --user-approved`), then `scripts/release.sh vX.Y.Z --user-approved` (tags `main`; needs a clean, up-to-date checkout of `main` and green CI). The tag runs `.github/workflows/release.yml` (tests, build, zip, `SHA256SUMS`, GitHub release, no approval gate); the in-app updater picks it up ([UPDATES.md](UPDATES.md)). Choose the version by semver (fix → patch, feature → minor); after a stable release the dev base version moves to the next patch.
`.github/workflows/ci.yml` runs `go vet`, `go test`, the strings check and the app build on every push to every branch and on pull requests.
Local test build with a version: `make build VERSION=1.2.3 && VERSION=1.2.3 app/build.sh`.

## Conventions
- Go: one module `github.com/DevertNet/bergfried`; `daemon/internal/*`, `mcp/cmd/bergfried-mcp`, `proto/gen/…` (generated, do not edit).
- Plan first for larger changes; small testable steps; security-relevant code (broker, blocklist, socket permissions, importer, template check, updater) always gets tests.
- User-visible app text is localised (English + German, `app/Resources/*.lproj`); everything else in the repo is English.
- SQLite: `INSERT` with explicit column lists; new columns via `ALTER TABLE` in `store.migrate`.
- No secrets in the repo; never commit anything from `~/.bergfried`, tokens or keys.

## Testing with real VMs and SSH
- Create test sandboxes with unique names and delete them afterwards (`bergfriedd delete <name> --force`). Never touch a user's existing sandboxes.
- Never test SSH with real keys: own key (`ssh-keygen`), own `ssh-agent` (short socket path, e.g. `/private/tmp/bft/agent.sock`, Unix limit ≈ 104 chars), a test sshd in Docker (`lscr.io/linuxserver/openssh-server`, port 2222, `PUBLIC_KEY`),
  a `known_hosts` test file with `[host.lima.internal]:2222`, daemon started with `--upstream-agent <test socket> --known-hosts <test file>`. Target from the VM: `host.lima.internal:2222`.
- Simulate interactive `ssh` in the VM: `script -qec "ssh …" /dev/null` (stderr is then a TTY).
- Call the MCP server without Claude: JSON-RPC over stdio (see [testing/files.md](testing/files.md)); identity = socket.
- GitHub without real GitHub: `tools/fakegithub` ([testing/github.md](testing/github.md)). Never use real tokens or repos for tests.

## Decisions worth knowing
- **Claude login:** `claude setup-token` does **not** work with Remote Control ("requires a full-scope login token"). So: one login per VM (`claude auth login`, button in the app); credentials stay in the VM.
- **No Xcode, no SwiftPM:** the dev Mac has only Command Line Tools (SwiftPM is broken there). `@State` is a macro without plugin in this SDK → replacement `@Local` (`app/Sources/Local.swift`). No third-party dependencies.
- **App ↔ daemon over HTTP/JSON** instead of gRPC for Swift (grpc-swift needs SwiftPM). gRPC stays for VM ↔ daemon and CLI ↔ daemon.
- **Import instead of mount** for existing folders (keeps "exactly two mounts", and performance).
- **Base VM named after the template hash:** a changed template means building a new base once; old base VMs are removed afterwards (only names matching `^bergfried-base-([0-9a-f]{8}|v[0-9]+)$`).
- Go 1.26, pure Go (`modernc.org/sqlite`, no cgo).

## Pitfalls (each cost time once)
- **zsh:** `log` is a shell function → use `/usr/bin/log show …`. BSD `sed -i ''`.
- **limactl wrapper:** keep stdout and stderr apart (warnings on stderr broke the JSON). `limactl validate` needs file names without a leading dot. Lima **pins the VM's SSH host key**: after regenerating keys (`firstBoot`) delete `ssh.known_hosts` (`lima.RepinHostKey`) or every `start` hangs.
- **Clones** inherit machine id and SSH host keys from the base → `firstBoot` renews both.
- **gofmt** realigns struct fields, which breaks scripted string replacements on struct definitions; grep the current shape first.
- **Unix socket paths** are limited to ~104 characters.
- **Notification permission:** never quit or restart the app while the macOS prompt is open — that counts as "denied" and macOS never asks again (only System Settings or a new bundle id help). Current bundle id: `de.einfach123.bergfried`.
- **Terminal start** goes through a `.command` file + `open -a` (AppleScript for iTerm failed silently without automation rights).
- **tmux default window** (`bash`/`shell`) is not an agent; the monitor only looks at registered agents. "Is Claude running?" is detected with `ps -t <tty>` (`pane_current_command` wrongly shows `bash`).
- **Race in tests:** right after `create` the sandbox socket is briefly not open; an `ssh` in that second triggers no request.
- **Docker Go builds:** `make build` builds `bergfriedd` for darwin/arm64 and `bergfried-mcp` for linux/arm64 (a VPS host would need linux/amd64).
- **Empty lists as JSON `null`** break Swift decoding (`Detail.Normalize`).
- **Secret redaction:** names like `DB_PASSWORD` slipped past `\b` before "password" (after `_` there is no word boundary).
- **OpenSSH protocol names** such as `session-bind@openssh.com` must never be touched by renames.
- **Renaming "session" → "sandbox"** once broke exactly that; the fix and a test exist.

## Remote Control detection (monitor)
- The banner "/remote-control is active" appears at the **start** of the output and scrolls away. The monitor reads the whole tmux scrollback (`history-limit 20000`) and remembers active RC **per claude PID** (`stickyRC`): the same process still running means RC is active.
- `/remote-control` while RC is active **opens a dialog** ("This session is available in the Claude mobile app …", with "Disconnect this session"). The monitor treats it as "RC active" and closes it with **Escape** (never Enter).
  Escape hatch in the app: agent menu → "Remote Control is running – mark as active".
- A new claude PID (restart) resets the counters and "cannot activate".
