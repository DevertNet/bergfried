# Architecture

Bergfried runs Claude Code and OpenCode agents inside isolated Linux VMs (**sandboxes**) on your Mac. An agent sees nothing of your machine unless you approve it,
one request at a time. You control everything from a native macOS app. Claude agents can be driven from the phone through Claude Code Remote Control, OpenCode agents from the OpenCode desktop app.

```mermaid
flowchart LR
  phone["Phone / claude.ai/code<br/>(Remote Control)"]
  desktop["OpenCode desktop app"]
  subgraph vm["Sandbox = Lima VM (Linux)"]
    agent["Claude Code / OpenCode agent<br/>tmux window + git worktree"]
    mcp["bergfried-mcp<br/>(MCP server)"]
    agent --- mcp
  end
  subgraph mac["Your Mac"]
    daemon["bergfriedd<br/>daemon"]
    app["Bergfried.app<br/>approvals · status"]
    data["Your files<br/>ssh-agent keys"]
    daemon <-->|"HTTP/JSON<br/>app.sock"| app
    daemon -.->|"only after approval"| data
  end
  phone <-->|"Anthropic cloud"| agent
  desktop <-->|"one-time link over Lima port forwarding<br/>(local only, no internet)"| agent
  mcp <-->|"gRPC over a per-sandbox<br/>unix socket (identity = socket)"| daemon
```

## Components

| Component | Where | What it does |
|---|---|---|
| `bergfriedd` | Mac (LaunchAgent, started by the app) | One Go binary: SQLite state, file-name index, per-sandbox sockets, SSH broker, monitor, HTTP/JSON API for the app. |
| `Bergfried.app` | Mac | SwiftUI control app (built with `swiftc`, no Xcode): sandboxes, agents, requests, SSH grants, audit log, diagnostics (logs), settings, updates (stable/dev channel). |
| `bergfried-mcp` | inside each VM | MCP server for the agent (`list_files`, `search_files`, `request_file`, `request_folder`, `request_ssh`, `request_github_token`) plus the `ssh-hook` subcommand. |
| Lima VM | `~/.lima/bergfried-<name>` | One VM per sandbox, cloned from a base VM named after the hash of the template (seconds instead of minutes). |
| `tools/fakegithub` | dev only | Fake GitHub (REST + git over HTTP) for tests. |

## How a request flows

```mermaid
sequenceDiagram
  participant A as Agent (VM)
  participant D as bergfriedd (Mac)
  participant U as You (app)
  A->>D: request_file("Documents/report.pdf", reason)
  D->>D: blocklist check, resolve path (no symlinks)
  D->>U: notification + request card
  U->>D: Allow / Deny
  D-->>A: read-only copy in shared/in (or "denied")
  D->>D: audit log entry
```

SSH works the same way, but the daemon never hands out a key: it speaks the ssh-agent protocol to the VM and signs **upstream with your real agent**,
only while a grant is active (time window, key, target host key).

## Key design points

1. **One sandbox = one project = one Lima VM**, cloned from a base VM `bergfried-base-<hash of vm/bergfried.yaml>`. The base image contains `git`, `gh`, `git-lfs`, `make`, `jq`, `tmux`, Docker, headless `chromium`, Claude Code and OpenCode. The base VM is built on creation of the first sandbox with a new template hash or on "Build now"; existing sandboxes keep their image (agents are told to `sudo apt-get install` what is missing).
   The template is editable in the app and validated by `sandbox.CheckTemplate` (no mounts of your home, no `forwardAgent`, no Docker socket, both Bergfried sockets required). Your copy lives in `~/.bergfried/template/`:
   it is taken from the bundled template once, and at daemon start it is **replaced by a newer bundled template if you never edited it** (the copy equals the last adopted one, recorded in `seed.sha256`, or a known earlier bundled one; audit event `template.adopt`). An edited copy is kept and the app warns; "Reset template" adopts the bundled one.
2. **Exactly two mounts per sandbox:** `~/.bergfried/sandboxes/<name>/shared/in` (read-only) and `shared/out` (read-write). Existing folders are *copied* in (`importer`), never mounted.
3. **Identity of a sandbox = the socket.** For each running sandbox the daemon listens on `~/.lima/<vm>/sock/bergfried.sock` (gRPC `SandboxAPI`) and `ssh-agent.sock`.
   Lima forwards both into the VM (`/run/user/<uid>/bergfried.sock`, `bergfried-agent.sock`) with `reverse` port forwards. The agent *name* is only metadata claimed from inside the VM (same Linux user, not a security boundary).
   Claude passes it via `BERGFRIED_AGENT`; OpenCode's shared server starts one MCP process per project directory, so `bergfried-mcp` derives the agent from its working directory (`~/work/<agent>`) and only falls back to the environment variable.
4. **Files:** the index covers your home **minus the blocklist** (`~/.ssh`, `~/Library`, `~/.config`, `.env*`, keys, …). Requests for blocked paths are refused without asking. Approved files are copied to `shared/in/<sandbox>/…`.
   Building the index makes macOS ask for folder access (again after every update, because the app is ad-hoc signed), so the first build after an install or update waits behind `index.Gate`: the app explains it and the user confirms (`POST /v1/index/start`); the confirmed daemon version is stored in `~/.bergfried/index-ack`. A daemon not started from the app bundle is not gated.
5. **SSH:** the broker implements the ssh-agent protocol with destination constraints (`session-bind@openssh.com`). It signs only with an active grant **and** a matching target host key (fingerprint recorded at grant time from `known_hosts`/`ssh-keyscan`).
   A root-owned `Match exec` hook in the VM turns a plain `ssh host` into a request; interactive `ssh` waits for your decision.
   Keys that are not in the agent (passphrase-protected key files in `~/.ssh`) are offered too: the passphrase typed for the grant decrypts the key into the daemon's memory for the length of the grant (`broker/localkeys.go`; see [SECURITY.md](SECURITY.md)).
6. **GitHub:** one fine-grained token per sandbox, stored in the VM (`~/.git-credentials`, also used to log in `gh`) and used by plain `git` over HTTPS. See [GITHUB.md](GITHUB.md).
7. **Agents:** one tmux window per agent (tmux session `bf`), one git worktree `~/work/<agent>` (`main` = `~/work/main`), `COMPOSE_PROJECT_NAME=<sandbox>-<agent>`.
   Each agent stores its runtime (`claude` or `opencode`); Claude runs `claude --continue --remote-control '<sandbox>/<agent>'`, OpenCode runs `opencode --continue`.
   The **monitor** polls running VMs (login per runtime, installed state per runtime, Remote Control status and link for Claude, OpenCode busy state, disk usage), restarts only the agents of a runtime after its login,
   answers Claude onboarding prompts with Enter and activates Remote Control (confirmation `y`, restart or `/remote-control`, max. 3 attempts). OpenCode is connected to the desktop app
   through a one-time local link (`opencode pair`, no internet): the OpenCode server listens on a stable per-sandbox port on `0.0.0.0` and Lima forwards it to `127.0.0.1` on the Mac.
   Before a link is created the daemon restarts the shared OpenCode service so it really uses that port (the config may already be right while the running process is stale), then returns the link and restarts the TUI windows in the background.
   OpenCode is installed on demand in older VMs with the official installer (latest version, user-local, no root/apt); leftovers of earlier attempts are removed first and the install log is in `/tmp/opencode-install.log`. All OpenCode agents of a sandbox share **one** OpenCode service and **one** desktop link; each agent still has its own TUI process, worktree and MCP process.
8. **UI:** the sandbox overview is one alphabetical card list. Each card has the state/actions, a compact metadata line, one card each for Claude and OpenCode (installed, signed in, sign-in/connect) and an agent table with runtime, status, worktree and actions. Settings has a secondary sidebar: General, Updates, Base VM, Notifications, Files & index, Terminal, Advanced (template editor behind Advanced). Base-build output follows new lines while the view is at the bottom.
9. **App ↔ daemon** speak HTTP/JSON over `~/.bergfried/app.sock` (0600). gRPC is used VM ↔ daemon and CLI ↔ daemon. The app is the decision maker; the daemon runs with `--no-dialog`.

## Data on your Mac

`~/.bergfried` (0700): `bergfried.db`, `blocklist.json`, `hosts.json`, `settings.json` (update channel), `template/` (`bergfried.yaml`, `revision`, `seed.sha256`), `index-ack`, `update/` (staged releases, backups, `update.log`), `sandboxes/<name>/{shared/{in,out},github-token}`, `admin.sock`, `app.sock`, `daemon.log` (see [DEVELOPMENT.md](DEVELOPMENT.md#logs)).
Naming: VMs are `bergfried-<sandbox>` (≤ 24 characters, `base` reserved), base VMs `bergfried-base-<hash>`.

## Code layout

```
daemon/cmd/bergfriedd        CLI + serve
daemon/internal/             access, api (+monitor), broker, ghtoken, github, importer, index, lima, notify, rpc, sandbox, store, update
mcp/cmd/bergfried-mcp        MCP server + ssh-hook (runs in the VM)
proto/                       protobuf definitions; generated code in proto/gen (checked in)
app/                         SwiftUI app sources, build.sh, icon
vm/bergfried.yaml            default Lima template
tools/fakegithub             test double for GitHub
```
