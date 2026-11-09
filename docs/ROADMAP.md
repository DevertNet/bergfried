# Roadmap

Living to-do list. `[x]` done, `[ ]` open. Order inside a section is rough priority.

## Done
- [x] Lima template, base VM cloning, `bergfriedd` CLI (create/start/stop/delete/list), Remote Control from the phone
- [x] File requests: name index with blocklist, `bergfried-mcp`, copy to `shared/in`, folder import
- [x] SSH broker: ssh-agent protocol with destination constraints, grants with expiry, `ssh` hook with interactive wait
- [x] macOS control app (SwiftUI, no Xcode): sandboxes, agents, requests, grants, audit log, VM template editor, notifications, menu bar
- [x] Agents per sandbox (tmux window + worktree), Remote Control monitor/automation
- [x] GitHub: one fine-grained token per sandbox in the VM, guided creation, `request_github_token`
- [x] Rename "session" → "sandbox" everywhere, migration of existing installs
- [x] English repository, app in English and German
- [x] Updates from GitHub releases (build by CI, install button in the app, step-by-step progress and errors)
- [x] Release channels stable/dev, CI on every branch, `scripts/` (PR, CI status, release), rollback; developing Bergfried inside a sandbox ([design/develop-in-sandbox.md](design/develop-in-sandbox.md), [design/release-channels.md](design/release-channels.md)); no rulesets by decision
- [x] First end-to-end runs confirmed by the maintainer (2026-11-09): real GitHub with a fine-grained token, published release, in-app update via the dev channel, rollback, app UI
- [x] VM image with `make`, `chromium` (headless), `gh`, `git-lfs`; agent hints for browser, apt and `gh`; unedited template updates are adopted automatically
- [x] Index explanation before the first index build after an install or update (`index.Gate`); Diagnostics pane in the app

## Next
- [ ] Per-connection SSH confirmation as its own app request (currently only in the legacy dialog mode)
- [ ] Port overview per sandbox ("open shop on localhost:8080") — Lima already forwards guest ports to `127.0.0.1`
- [ ] Resource limits and idle auto-stop, `caffeinate` option
- [ ] Update Claude Code in all VMs from the app

## Later
- [ ] VPS host (Debian, SSH link, KVM check): [design/vps-host.md](design/vps-host.md)
- [ ] macOS sandboxes / second VM template (postponed; CI builds replace it for now): [design/macos-sandboxes.md](design/macos-sandboxes.md)
- [ ] Agent template proposals: [design/template-proposals.md](design/template-proposals.md)
- [ ] File browser/editor and "open in VS Code"
- [ ] Several GitHub owners/accounts per sandbox ([GITHUB.md](GITHUB.md))
- [ ] Signed releases (or a separate releases repository) — see the trust notes in [UPDATES.md](UPDATES.md)
- [ ] Developer ID signing and notarisation (keeps macOS permissions across updates)
- [ ] Xcode project around the same sources once Xcode is available
- [ ] Egress proxy hook for sandboxes
- [ ] Phone approvals (postponed by design)
- [ ] `pr_feedback` (read review comments) — only if GitHub access via the API proves useful
