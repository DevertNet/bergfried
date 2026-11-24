# Browser for the assistants (design notes, postponed)

Status: **postponed by decision (2026-11-18).** Nothing is implemented. This page keeps the analysis of two options so it need not be redone.
Today the VM image has headless `chromium` only (see [ARCHITECTURE.md](../ARCHITECTURE.md)).

## Option A: agweb on the Mac, bridged by the daemon

[agweb](https://github.com/paul-nameless/agweb) (MIT, Swift, macOS 15.4+) is a WebKit browser for coding agents: one window that never takes focus, a dashboard of all agents' tabs, per-workspace tabs, named profiles, replies that report only what changed.
The `agweb` CLI talks to the app over a Unix socket (`<AGWEB_ROOT>/agweb.sock`, one JSON request line in, one JSON response out). `AGWEB_ROOT` selects a separate instance with its own profiles.
Read from the README and `Protocol.swift` only; **not run, not verified.** The project is very young (v0.1 to v0.4.0 within a week, one maintainer, unversioned protocol), so a version would have to be pinned.

Why it is not "just install it": the assistants run in Linux VMs, agweb runs on the Mac. Passing the socket into the VM would break the "VM sees nothing of the Mac" boundary:
- `upload`, `screenshot`, `pdf`, `download`, `video-start`, `state-save`/`state-load` take Mac paths.
- `fill --secret-cmd` runs a shell command on the Mac.
- `file://` URLs open Mac files; the browser sits in the Mac's network (loopback, LAN, router).

### Plan
0. **Spike on the Mac** (manual, no code; cannot be done from a VM): start agweb with a short `AGWEB_ROOT`, send raw JSON with `nc -U`. Check framing, long-running `ask`, large output, `open -g` from a LaunchAgent, real focus behaviour, signing/notarisation, minimum macOS, `file://` and `localhost` behaviour, whether a WebKit proxy can be set. Record in `docs/testing/browser.md`.
1. **Daemon:** package `daemon/internal/browser` starts and supervises an agweb instance under `~/.bergfried/agweb` (not the user's own). Table-driven allowlist with argument validation; workspace forced to `bf-<sandbox>-<name>`, one profile per sandbox. Blocked: `--secret-cmd`, `file:`, Mac paths, `state-*`, `update`, `quit`, `close-all`, `install-*`. Files travel as bytes over gRPC (the daemon stages uploads temporarily and returns screenshots, PDFs, downloads); nothing stays on the Mac. Audit every command (command and host, not content), size and concurrency limits. New RPC in `proto/`. Tests against a fake agweb socket (like `tools/fakegithub`) are mandatory.
2. **VM side:** an `agweb` shim in `bergfried-mcp` forwarding to the daemon, MCP tools `request_browser` / `release_browser` (like SSH and git grants), a shared skill derived from agweb's `SKILL.md` (MIT, with attribution).
3. **App:** request card ("allow browser for sandbox X for N minutes"), browser card in the sandbox details (grant state, show dashboard, close tabs, quit), settings (off by default, agweb path and version). Strings in English and German (`scripts/check-strings.py`).
4. **Hardening:** the daemon sees only URLs in `open`/`goto`; redirects, links and `eval "location=…"` bypass that. Real protection from loopback/LAN needs a proxy hook in agweb (patch or fork), which fits the roadmap item "Egress proxy hook". Until then the browser has the Mac's network position: document it under "Known gaps" in [SECURITY.md](../SECURITY.md).
5. **Docs** in the same PR: README, ARCHITECTURE, SECURITY (table row, invariants, gaps), ROADMAP, this page, `docs/testing/browser.md`.
6. **Release** via `dev`; no stable release in the same session (security-relevant code).

Open decisions: grants with a time window (recommended) or always on; loopback/LAN reachable (recommended: no, only after approval); install agweb separately and detect it (recommended) or bundle it; one profile per sandbox (recommended) or shared logins.
The feature would be Mac-only (agweb has no Linux build), so a VPS host needs a second backend.

## Option B: visible Chromium in the VM, watched from the Mac

- Chromium runs headed on a virtual display (Xvfb) in the VM, which is blocked by fewer sites than headless.
- Assistants drive it through a standard MCP (Playwright MCP or chrome-devtools-mcp).
- A VNC service with noVNC shows the display; Lima forwards the port to `127.0.0.1` of the Mac (like the OpenCode link). The sandbox card gets a "Watch browser" button. The user handles logins, MFA and CAPTCHAs in that window.

Pros: the VM boundary is unchanged (the browser lives in the VM's network, no Mac paths, no Mac profiles, no command filter), works on the Debian VPS, little security-relevant daemon code.
Cons: larger image (Xvfb, VNC, noVNC, possibly Playwright's own arm64 Chromium); one window per sandbox instead of an overview; the VNC port must be bound to `127.0.0.1` and protected by a password or token.
Not tried.

Stages: (1) template and skill only (Xvfb, noVNC, Playwright MCP, guidance text); (2) button and port handling in the app; (3) route the browser through the egress proxy once it exists, to approve domains.

## Recommendation when this is picked up again
Start with option B stages 1 and 2. Consider option A later as an optional Mac-only backend if agweb's dashboard and focus behaviour are wanted; run the spike first.
