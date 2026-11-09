# Security model

## What Bergfried protects against
An agent (or anything it runs, including prompt-injected instructions) misusing your Mac: reading your files, using your SSH keys, or reaching your credentials.
**It is not a defence against a malicious VM escaping the hypervisor**, and it does not stop the agent from doing harm *inside* its own VM or with the GitHub token you give it.

## What the agent can and cannot do

| | Agent in the VM |
|---|---|
| See your home, keys, host Docker | **Never.** Only `shared/in` (ro) and `shared/out` are mounted. |
| Read your files | Only copies you approve, one request at a time. Blocked paths are refused without asking. |
| Use your SSH keys | Only during a grant you create (time window, key, target host key). The private key never leaves the Mac. |
| Use GitHub | With the fine-grained token **you** created for that sandbox (see below). |
| Reach the network | Yes (the VM has normal outbound access). |
| `sudo` in the VM | Yes (Lima default); agents are told to install missing software themselves with `sudo apt-get install`. The boundary is the VM, not the Linux user. |

## Invariants (covered by tests, please do not weaken)
- The VM never sees your home, keys or the host Docker. Mounts: only `shared/in` (ro) and `shared/out`.
- The private SSH key never leaves the Mac. Signing happens upstream, only with a grant plus a host-key binding; agent forwarding is refused.
- Blocklist: case-insensitive compare (macOS), `..`/absolute paths/symlink escapes fail (`index.Resolve`), symlinks are never followed or copied. `PUT /v1/blocklist` cannot remove the three fixed entries (`.ssh`, `Library`, `.config`).
- Agent-provided text (reason, name, user, host) is display data only: sanitised, never interpolated into shell or AppleScript source; host names and users are validated by regex before they reach `~/.ssh/config` blocks.
- All sockets are 0600 in 0700 directories; `~/.bergfried` is 0700.
- Everything is audited (`audit` table; app "Audit log", CSV export with formula protection). Audit failures are not swallowed.

## GitHub token trade-off (deliberate)
Each sandbox gets one fine-grained personal access token, stored **inside the VM** (`~/.git-credentials`, 0600; `gh` keeps a second copy in `~/.config/gh/hosts.yml`) and used for all git operations and GitHub Actions.
The agent can read it. Within the repositories and permissions you selected it can therefore **push to any branch (including `main`, including force pushes)** — private repos on the Free plan have no branch protection —
and workflows may expose repository secrets. Limits: choose *Only select repositories*, minimal permissions (no Workflows permission, so `.github/workflows` cannot be changed), one-year expiry, one token per sandbox.
Revoking happens on GitHub; Bergfried only deletes its own copies when the sandbox is deleted. Details: [GITHUB.md](GITHUB.md).

## Updates
The app can update itself from GitHub releases. **Releases are not signed**: whoever can publish a release in this repository can ship code that runs on your Mac.
Releases are not technically gated: a push to `dev` or a tag `vX.Y.Z` publishes one. By decision the agent of the development sandbox publishes dev pre-releases on its own, but makes **stable releases only on the maintainer's explicit request** (rule in `CLAUDE.md`; `scripts/release.sh` and `scripts/pr.sh merge … main` refuse without `--user-approved`; this is a rule, not a barrier). A token of your own account cannot be separated from you by rulesets. Keep the **Deployments** and **Workflows** permissions out of sandbox tokens. See [UPDATES.md](UPDATES.md) and [design/develop-in-sandbox.md](design/develop-in-sandbox.md).

## Logs
`~/.bergfried/daemon.log` and the app's Log pane contain error texts, paths and VM names, not tokens or file contents; check before pasting a log somewhere public. The log endpoint (`GET /v1/log`) is on the owner-only app socket and reads fixed files only.

## Known gaps
- A host key shown for an unknown SSH host must be verified by you; an approved grant could be used by the agent to add its own key to `authorized_keys` on the target. Use a restricted user whose `authorized_keys` is root-owned for important servers.
- "Confirm every SSH connection" exists only in the legacy dialog mode; the app disables it and the daemon refuses it otherwise.
- The agent name shown in requests is claimed from inside the VM. The *sandbox* is derived from the socket and is trustworthy.
- A short time window exists between check and copy of a file (TOCTOU); every file is re-resolved and re-checked at copy time and symlinks are never copied.
