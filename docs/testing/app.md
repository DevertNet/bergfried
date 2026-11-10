# Manual test: control app (macOS)

```bash
make build            # bin/bergfriedd, bin/bergfried-mcp (Docker)
app/build.sh          # app/build/Bergfried.app (swiftc, ad-hoc signed, binaries + template inside the bundle)
open app/build/Bergfried.app
```

1. **Settings → General → "Install and start daemon"** (runs in the background with a status text) creates a LaunchAgent (`~/Library/LaunchAgents/de.einfach123.bergfried.plist`, `bergfriedd serve --no-dialog`, log `~/.bergfried/daemon.log`). Alternatively run `bergfriedd serve --no-dialog` in a terminal.
2. **Settings → Base VM → "Build now"** (once, a few minutes).
3. **Sandboxes → "New sandbox"**, choose Claude Code or OpenCode for the first agent, then "Agent +" (runtime per agent), sign in per runtime (Claude and/or OpenCode), start/stop, delete (warning for unpushed commits, "Delete anyway" after confirmation).
4. **Requests:** allow/deny file and folder requests (large folders need a second confirmation). Every **Deny** opens a small sheet with an optional **note for the agent**; the note is returned with the denial. SSH and GitHub token sheets have the same note field. The quick Deny action in a macOS notification has no note field; use the Requests tab for that. SSH requests: "Decide …" opens a scrollable sheet with duration, key, user, scope, host-key confirmation for new hosts and the note (returned with allow and deny). Active grants with countdown, extend, revoke.
5. **Audit log:** filter and CSV export (formulas are neutralised). **Diagnostics:** daemon log (every action logs started/done/FAILED), updater log, Lima log per VM; "Copy all".
6. **Menu bar:** shield icon with the number of open requests; file requests can be allowed/denied right there.
7. **Index explanation:** after the daemon starts (first install and after every update) a dialog explains the macOS folder prompts; the index only starts after "Continue".
8. **Notifications** with action buttons. On first start macOS asks for notification permission — answer it before doing anything else with the app.

## Features worth checking
- **Edit the VM template:** Settings → **Advanced → Template (bergfried.yaml)**. Only what passes `limactl validate` and keeps isolation is saved. A changed template means a new base VM (build once); existing sandboxes are unchanged; older base VMs are removed after a rebuild. File: `~/.bergfried/template/bergfried.yaml`.
- **Use an existing folder:** "New sandbox" → source "Folder" (CLI: `create <name> --import FOLDER`). The folder is **copied** (never mounted) to `~/work/main`: only below your home, never the home itself, never blocked paths; without `.env`/keys/symlinks/`node_modules`/caches; credentials in `.git/config` are stripped. A preview shows files and size first.
- **Overview:** sandboxes are one **alphabetical card list** (no Running/Stopped groups). Each card has the state/actions, metadata, one **Claude** and one **OpenCode** card (installed, signed in, sign-in/connect) and an agent table with runtime, status, worktree and actions. A stopped sandbox shows "not checked" for runtime state until it starts. A window that still runs the other runtime is repaired once automatically (audit `agent.runtime_repaired`); if it persists, use the agent menu → Restart. The daemon polls running VMs every 5 s (unstable) or 20 s (all green).
- **Base VM banner:** when no base exists, the banner offers **Build now** and can be dismissed with **×**; building shows the live output.
- **Login:** the sign-in button opens iTerm (if installed, otherwise Terminal) with `claude auth login` or `opencode auth login`. After a login the daemon restarts only the agents of that runtime (`--continue`); Claude onboarding prompts are answered with the preselection.
- **OpenCode desktop app:** in the OpenCode runtime card click **Connect** (or Sandbox menu → Shell / SSH → Connect desktop app …). It works without an OpenCode agent (sandbox-level service) and installs OpenCode if needed. A sheet opens immediately with "Preparing …", then a one-time link on the sandbox port (42xxx), valid for 5 minutes, with Copy and Open in browser. Do not open it in the browser first if you want the desktop app. Errors are shown in the sheet with Try again.
- **Update size:** when a release is available, the sandbox banner, Settings → Updates and the install button show the zip size (e.g. 12.4 MB).
- **Job output:** expanding the live output follows new lines while you are at the bottom; scrolling up pauses it and shows "Jump to latest".
- **Remote Control is watched and activated:** if Claude is logged in and an agent has no active RC after ~15–20 s the daemon answers an open "Enable Remote Control?" prompt with `y`, restarts an agent that is only a shell, or types `/remote-control` when Claude is idle. Max. 3 attempts per agent, then "RC cannot be activated – restart". Everything is in the audit log (`agent.rc_enable`).
- **First agent:** a new sandbox starts agent `main` automatically; older sandboxes without an agent get `main` once.
- **Agents:** one row per agent with runtime chip, status, worktree, "Open RC" (Claude only) and a menu (copy link, restart, tmux, remove …). Removing: "stop agent only" or "delete agent and directory" (worktree, branch `agent/<name>`, Docker stack) with a check for unsaved work; `main`'s directory is never deleted.
- **Shell/SSH into the VM:** sandbox row → "Shell / SSH": a normal shell (`limactl shell`) in iTerm/Terminal, "Copy SSH command" (`ssh -F ~/.lima/<vm>/ssh.config lima-<vm>`, also usable for VS Code Remote-SSH), sign-in entries for the runtimes in use and **Connect desktop app** for OpenCode.
- **Language:** the app follows the macOS language (English or German).

## Technical notes
- The app talks HTTP/JSON over `~/.bergfried/app.sock` (0600). `Bergfried --selftest` checks the connection.
- With Command Line Tools only, `@State` is a macro without plugin; the app uses `@Local` (`Local.swift`).
- Notifications need an ad-hoc signed bundle; without an Apple developer certificate macOS may treat them more strictly. The dock badge, bouncing dock icon and the menu bar counter work regardless.
- Agents never see the UI (CI only compiles it); the maintainer checks layout, dialogs, notification buttons and the menu bar.
