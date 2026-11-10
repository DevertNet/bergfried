# Testing OpenCode agents

OpenCode is a second agent runtime next to Claude Code. Both can run in the same sandbox.
Everything below is manual (real Lima VM, macOS app); CI only runs the Go tests and builds the app.

## New sandbox with both runtimes

1. Install the [OpenCode desktop app](https://opencode.ai/download) on the Mac (once).
2. Create a sandbox with "New sandbox"; choose **OpenCode** for the first agent (`main`).
3. Add a second agent with **Agent +** and choose **Claude Code**.
4. Start the sandbox. Both agent windows should appear; each has its own worktree.
5. Sign in:
   - **Sign in to OpenCode …** → terminal with `opencode auth login` (choose a provider).
   - **Sign in to Claude …** → terminal with `claude auth login`.
   After each login only the agents of that runtime restart.
6. Wait until the status shows "OpenCode runs" and "Remote Control active" (Claude).

## OpenCode desktop app (no agent required)

OpenCode is a sandbox-level service, not an agent. Connect works in a sandbox without an OpenCode agent; it starts the service and lets the desktop app open the existing worktrees.

1. In the **OpenCode runtime card** click **Connect** (or Sandbox menu → Shell / SSH → **Connect desktop app …**). If OpenCode is not installed, Connect installs the latest version first.
2. The sheet opens immediately with "Preparing …" (the service is restarted on the sandbox port); then a one-time link appears (valid 5 minutes) with **Copy link** and the port (42xxx).
3. OpenCode desktop app → add server → paste the link. Do not open it in the browser first if you want the desktop app.
4. The desktop app can open the sandbox worktrees as projects; existing OpenCode agents appear as their own projects. Send a prompt and check that the file/SSH/GitHub requests still appear in **Bergfried** (not only in OpenCode).
5. Add another OpenCode agent: it appears in the desktop app without a new link (the service and link are sandbox-level).
6. Click **Connect** again after the link expired: a fresh link is issued; the saved connection keeps working. Errors are shown in the sheet with **Try again**.

Check that the link uses a port in **42000–42999** and is **not** reachable from another device on the LAN (it is bound to `127.0.0.1` on the Mac). A link on the OpenCode default **49374** is wrong: a local OpenCode on the Mac can occupy it; click Connect again.

## Existing VM (no OpenCode installed)

1. Take an old sandbox (created before this change) and start it.
2. Add an agent with **OpenCode**.
3. The job output should show "Installing OpenCode in the VM" (latest version, user-local; OpenCode is not in the Debian repositories, so the official installer runs in the VM, not `apt`), then the agent starts. Leftovers of earlier attempts are removed first; the full log is `/tmp/opencode-install.log` in the VM and its tail is shown if the install fails.
4. Claude agents in the same VM keep running during the installation.
5. Stop and start the sandbox: the OpenCode agent continues its session (`opencode --continue`) and the desktop connection still works (same port).

## Cleanup and edge cases

- Remove an OpenCode agent with its directory: its sessions disappear from the desktop app.
- Remove only the agent (keep the directory): sessions stay.
- Delete the sandbox: the desktop connection is dead.
- Two sandboxes with OpenCode get different local ports and separate links.
- Two OpenCode agents in **one** sandbox share **one** OpenCode service and **one** desktop link; they appear as separate projects. Each window has its own TUI process and worktree; the shared server is the only long-running OpenCode process (plus one MCP process per project).

## Development sandbox limits

- The development VM has no macOS, Lima or real Bergfried daemon: the SwiftUI app is built by CI only, and the maintainer checks the real VM, Lima port forwarding and the desktop app.
- The install log (`/tmp/opencode-install.log`) and the pairing sheet's port are the first places to look when a test fails.
