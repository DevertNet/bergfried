# Testing OpenCode

OpenCode is a sandbox-level service next to Claude (which runs per worktree).
Everything below is manual (real Lima VM, macOS app); CI only runs the Go tests and builds the app.

## New sandbox

1. Install the [OpenCode desktop app](https://opencode.ai/download) on the Mac (once).
2. Create a sandbox with **New sandbox** (no runtime choice; the worktree `main` is created without starting Claude).
3. Add a worktree with **Worktree +**, e.g. `api`. It only creates the folder `~/work/api`.
4. Sign in:
   - **Sign in to OpenCode …** → terminal with `opencode auth login` (choose a provider).
   - **Sign in to Claude …** → terminal with `claude auth login` (only needed if you want Claude).
5. Click **Start service** in the OpenCode card. The status shows "service running".
6. Start Claude in a worktree (**Start Claude**); its status becomes "Claude runs · Remote Control active". Claude and OpenCode can use the same worktree, but not at the same time on the same files.

## OpenCode desktop app (Claude not required)

OpenCode is a sandbox-level service, not a worktree. Connect works without any started Claude; it starts the service and lets the desktop app open the existing worktrees.

1. In the **OpenCode card** click **Connect desktop** (or Sandbox menu → Shell / SSH → **Connect desktop app …**). If OpenCode is not installed, Connect installs the latest version first.
2. The sheet opens immediately with "Preparing …" (the service is restarted on the sandbox port); then a one-time link appears (valid 5 minutes) with **Copy link** and the port (42xxx).
3. OpenCode desktop app → add server → paste the link. Do not open it in the browser first if you want the desktop app.
4. The desktop app opens the sandbox worktrees as projects. Send a prompt and check that file/SSH/GitHub requests still appear in **Bergfried**.
5. Add another worktree: it appears in the desktop app without a new link (one service and one link per sandbox).
6. Click **Connect desktop** again after the link expired: a fresh link is issued; the saved connection keeps working. Errors are shown in the sheet with **Try again**.
7. **Stop service** stops it; **Start service** brings it back. The state survives a sandbox stop/start.

Check that the link uses a port in **42000–42999** and is **not** reachable from another device on the LAN (it is bound to `127.0.0.1` on the Mac). A link on the OpenCode default **49374** is wrong: a local OpenCode on the Mac can occupy it; click Connect again.

## Existing VM (no OpenCode installed)

1. Take an old sandbox (created before this change) and start it.
2. Click **Connect desktop** or **Start service** in the OpenCode card.
3. The job output should show "Installing OpenCode in the VM" (latest version, user-local; OpenCode is not in the Debian repositories, so the official installer runs in the VM, not `apt`). Leftovers of earlier attempts are removed first; the full log is `/tmp/opencode-install.log` in the VM and its tail is shown if the install fails.
4. Claude worktrees in the same VM keep running during the installation.
5. Stop and start the sandbox: the service starts again (same port) and the desktop connection still works. Claude worktrees with **Start Claude** restart with `--continue`.

## Migration from the old "agent" model

- Old agents become worktrees; `runtime=opencode` rows become worktrees with Claude stopped.
- Old OpenCode TUI windows are killed by the daemon (on sandbox start and by the monitor) because OpenCode is no longer a per-worktree TUI.
- Folders, git branches and OpenCode sessions on the server remain. Claude can be started in any of these worktrees later.

## Cleanup and edge cases

- Remove a worktree with its directory: its OpenCode sessions are deleted from the server.
- Remove only the worktree entry (keep the directory): sessions stay.
- Delete the sandbox: the desktop connection is dead.
- Two sandboxes with OpenCode get different local ports and separate links.
- Two worktrees in **one** sandbox share **one** OpenCode service and **one** desktop link; each project gets its own MCP process.

## Development sandbox limits

- The development VM has no macOS, Lima or real Bergfried daemon: the SwiftUI app is built by CI only, and the maintainer checks the real VM, Lima port forwarding and the desktop app.
- The install log (`/tmp/opencode-install.log`) and the pairing sheet's port are the first places to look when a test fails.
