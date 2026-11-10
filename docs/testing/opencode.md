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

## OpenCode desktop app

1. Sandbox menu → **Connect desktop app …**.
2. A one-time link appears (valid 5 minutes) with **Copy link**.
3. OpenCode desktop app → add server → paste the link.
4. The app shows every OpenCode agent of the sandbox as a project. Send a prompt and check that the file/SSH/GitHub requests still appear in **Bergfried** (not only in OpenCode).
5. Add another OpenCode agent: it appears in the desktop app without a new link.
6. Click **Connect desktop app …** again after the link expired: a fresh link is issued; the saved connection keeps working.

Check that the link is **not** reachable from another device on the LAN (it is bound to `127.0.0.1` on the Mac).

## Existing VM (no OpenCode installed)

1. Take an old sandbox (created before this change) and start it.
2. Add an agent with **OpenCode**.
3. The job output should show "Installing OpenCode in the VM" (pinned version, user-local; OpenCode is not in the Debian repositories, so this uses the official installer, not `apt`), then the agent starts.
4. Claude agents in the same VM keep running during the installation.
5. Stop and start the sandbox: the OpenCode agent continues its session (`opencode --continue`) and the desktop connection still works (same port).

## Cleanup and edge cases

- Remove an OpenCode agent with its directory: its sessions disappear from the desktop app.
- Remove only the agent (keep the directory): sessions stay.
- Delete the sandbox: the desktop connection is dead.
- Two sandboxes with OpenCode get different local ports and separate links.

## What was not verified while developing in a sandbox

- The SwiftUI changes (agent picker, runtime labels, pairing sheet) were built by CI only.
- Real `opencode pair` output, Lima port forwarding and the OpenCode desktop app were not available inside the development sandbox.
