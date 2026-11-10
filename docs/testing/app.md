# Manual test: control app (macOS)

```bash
make build            # bin/bergfriedd, bin/bergfried-mcp (Docker)
app/build.sh          # app/build/Bergfried.app (swiftc, ad-hoc signed, binaries + template inside the bundle)
open app/build/Bergfried.app
```

1. **Settings → General → "Install and start daemon"** (runs in the background with a status text) creates a LaunchAgent (`~/Library/LaunchAgents/de.einfach123.bergfried.plist`, `bergfriedd serve --no-dialog`, log `~/.bergfried/daemon.log`). Alternatively run `bergfriedd serve --no-dialog` in a terminal.
2. **Settings → Base VM → "Build now"** (once, a few minutes).
3. **Sandboxes → "New sandbox"** (no runtime choice), then **"Worktree +"** to add a folder, **Start Claude** in a worktree row, sign in per runtime (Claude and/or OpenCode), start/stop the sandbox, delete (warning for unpushed commits, "Delete anyway" after confirmation).
4. **Requests:** allow/deny file and folder requests (large folders need a second confirmation). Every **Deny** opens a small sheet with an optional **note for the assistant**; the note is returned with the denial. SSH and GitHub token sheets have the same note field. The quick Deny action in a macOS notification has no note field; use the Requests tab for that. SSH requests: "Decide …" opens a scrollable sheet with duration, key, user, scope, host-key confirmation for new hosts and the note (returned with allow and deny). Active grants with countdown, extend, revoke.
5. **Audit log:** filter and CSV export (formulas are neutralised). **Diagnostics:** daemon log (every action logs started/done/FAILED), updater log, Lima log per VM; "Copy all".
6. **Menu bar:** shield icon with the number of open requests; file requests can be allowed/denied right there.
7. **Index explanation:** after the daemon starts (first install and after every update) a dialog explains the macOS folder prompts; the index only starts after "Continue".
8. **Notifications** with action buttons. On first start macOS asks for notification permission — answer it before doing anything else with the app.

## Features worth checking
- **Edit the VM template:** Settings → **Advanced → Template (bergfried.yaml)**. Only what passes `limactl validate` and keeps isolation is saved. A changed template means a new base VM (build once); existing sandboxes are unchanged; older base VMs are removed after a rebuild. File: `~/.bergfried/template/bergfried.yaml`.
- **Use an existing folder:** "New sandbox" → source "Folder" (CLI: `create <name> --import FOLDER`). The folder is **copied** (never mounted) to `~/work/main`: only below your home, never the home itself, never blocked paths; without `.env`/keys/symlinks/`node_modules`/caches; credentials in `.git/config` are stripped. A preview shows files and size first.
- **Overview:** sandboxes are one **alphabetical card list** (no Running/Stopped groups). Each card has the state/actions, metadata, a **Claude** card (installed, signed in; status only), an **OpenCode** card (installed, signed in, service running, Start/Stop service, Connect desktop) and a **worktree table** with Worktree, Claude state and actions. There are no runtime chips and no runtime column. The OpenCode card shows "service running" even without a Claude worktree. A stopped sandbox shows "not checked" until it starts. The daemon polls running VMs every 5 s (unstable) or 20 s (all green).
- **Base VM banner:** when no base exists, the banner offers **Build now** and can be dismissed with **×**; building shows the live output.
- **Login:** the sign-in button opens iTerm (if installed, otherwise Terminal) with `claude auth login` or `opencode auth login`. After a Claude login the daemon restarts the running Claude worktrees (`--continue`); a new OpenCode login is picked up by the service. Claude onboarding prompts are answered with the preselection.
- **OpenCode desktop app:** in the OpenCode runtime card click **Connect desktop** (or Sandbox menu → Shell / SSH → Connect desktop app …). It works without Claude (sandbox-level service) and installs OpenCode if needed. **Start service / Stop service** control it without creating a link; the state survives a sandbox stop/start. A sheet opens immediately with "Preparing …", then a one-time link on the sandbox port (42xxx), valid for 5 minutes, with Copy and Open in browser. Do not open it in the browser first if you want the desktop app. Errors are shown in the sheet with Try again.
- **Update size:** when a release is available, the sandbox banner, Settings → Updates and the install button show the zip size (e.g. 12.4 MB).
- **Job output:** expanding the live output follows new lines while you are at the bottom; scrolling up pauses it and shows "Jump to latest".
- **Daemon version mismatch:** if the daemon version differs from the app, an orange banner offers **Restart daemon**. This is the first thing to check if windows seem to run the wrong thing (a second, older daemon may still be driving the VM; `pgrep -af bergfriedd` on the Mac).
- **Remote Control is watched and activated:** if Claude is logged in and a running worktree has no active RC after ~15–20 s the daemon answers an open "Enable Remote Control?" prompt with `y`, restarts Claude if only a shell runs, or types `/remote-control` when Claude is idle. Max. 3 attempts per worktree, then "RC cannot be activated – restart". Everything is in the audit log (`worktree.rc_enable`).
- **Worktrees:** a new sandbox creates the worktree `main` **without starting Claude**. "Worktree +" creates only the folder (no Claude, branch `worktree/<name>`). Each row shows Worktree, Claude state and actions: **Start Claude / Stop Claude**, iTerm, and a menu (copy RC link, restart Claude, remove worktree). The separate RC-link button was removed; copied links are still in the menu. Removing: keep the directory or delete it (git worktree, branch `worktree/<name>`, Docker stack) with a check for unsaved work; `main` cannot be removed – stop Claude instead.
- **Claude vs OpenCode:** Start Claude only affects the selected worktree; Start/Stop service only affects the sandbox-wide OpenCode service. Both may use the same worktree, but not at the same time on the same files.
- **Migration:** after updating from an older version, old OpenCode TUI windows are ended automatically (their worktrees become normal worktrees with Claude stopped); the folders and git history stay. Start Claude in a worktree only if you want it.
- **Shell/SSH into the VM:** sandbox row → "Shell / SSH": a normal shell (`limactl shell`) in iTerm/Terminal, "Copy SSH command" (`ssh -F ~/.lima/<vm>/ssh.config lima-<vm>`, also usable for VS Code Remote-SSH), sign-in entries for the runtimes in use and **Connect desktop app** for OpenCode.
- **Language:** the app follows the macOS language (English or German).

## Technical notes
- The app talks HTTP/JSON over `~/.bergfried/app.sock` (0600). `Bergfried --selftest` checks the connection.
- With Command Line Tools only, `@State` is a macro without plugin; the app uses `@Local` (`Local.swift`).
- Notifications need an ad-hoc signed bundle; without an Apple developer certificate macOS may treat them more strictly. The dock badge, bouncing dock icon and the menu bar counter work regardless.
- Assistants never see the UI (CI only compiles it); the maintainer checks layout, dialogs, notification buttons and the menu bar.
