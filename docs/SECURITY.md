# Security model

## What Bergfried protects against
An agent (or anything it runs, including prompt-injected instructions) misusing your Mac: reading your files, using your SSH keys, or reaching your credentials.
**It is not a defence against a malicious VM escaping the hypervisor**, and it does not stop the agent from doing harm *inside* its own VM or with the GitHub token you give it.

## What the agent can and cannot do

| | Agent in the VM |
|---|---|
| See your home, keys, host Docker | **Never.** Only `shared/in` (ro) and `shared/out` are mounted. |
| Read your files | Only copies you approve, one request at a time. Blocked paths are refused without asking. |
| Use your SSH keys | Only during a grant you create (time window, key, target host key). The private key never leaves the Mac. Keys that are not loaded in your ssh-agent can be granted too, but only with the passphrase you type for that grant (see below). |
| Use GitHub | With the fine-grained token **you** created for that sandbox (see below). |
| Reach the network | Yes (the VM has normal outbound access). |
| `sudo` in the VM | Yes (Lima default); agents are told to install missing software themselves with `sudo apt-get install`. The boundary is the VM, not the Linux user. |

## Invariants (covered by tests, please do not weaken)
- The VM never sees your home, keys or the host Docker. Mounts: only `shared/in` (ro) and `shared/out`.
- The private SSH key never leaves the Mac. For keys in your ssh-agent signing happens upstream (the daemon never sees the key), only with a grant plus a host-key binding; agent forwarding is refused. The one exception is the passphrase-key mode below.
- Blocklist: case-insensitive compare (macOS), `..`/absolute paths/symlink escapes fail (`index.Resolve`), symlinks are never followed or copied. `PUT /v1/blocklist` cannot remove the three fixed entries (`.ssh`, `Library`, `.config`).
- Agent-provided text (reason, name, user, host) is display data only: sanitised, never interpolated into shell or AppleScript source; host names and users are validated by regex before they reach `~/.ssh/config` blocks.
- All sockets are 0600 in 0700 directories; `~/.bergfried` is 0700.
- Everything is audited (`audit` table; app "Audit log", CSV export with formula protection). Audit failures are not swallowed.

## SSH keys with a passphrase that are not loaded (deliberate exception)
The app also offers key pairs from `~/.ssh` (`<name>` plus `<name>.pub`) that are **not** in your ssh-agent. For such a key the daemon cannot ask an agent to sign, so:
- you type the **passphrase for this grant** in the app; the daemon reads the private key file, decrypts it **in its own memory** and signs itself for as long as the grant lasts (until expiry or revocation, or until the daemon stops);
- the key is **never added to your ssh-agent**, never written to disk, the passphrase is not stored, not logged and not audited (the slice is zeroed after use), and a new grant needs the passphrase again;
- when the grant ends the decrypted key is dropped and, for ed25519 keys, zeroed (other key types are only released to the garbage collector: best effort); extending a grant after that is refused;
- every use still needs a valid grant and the target host key binding, and the audit log says `source=key file, decrypted in memory …`.
What changes compared with agent keys: **the decrypted key lives in the daemon process** for the length of the grant (a bug in or a memory dump of the daemon could expose it, and the daemon reads private key files from `~/.ssh`). Agents in the VM still never see the key, only signatures within the grant. Choose short durations for such grants. Tests: `daemon/internal/broker/localkeys_test.go`.

## GitHub token trade-off (deliberate)
Each sandbox gets one fine-grained personal access token, stored **inside the VM** (`~/.git-credentials`, 0600; `gh` keeps a second copy in `~/.config/gh/hosts.yml`) and used for all git operations and GitHub Actions.
The agent can read it. Within the repositories and permissions you selected it can therefore **push to any branch (including `main`, including force pushes)** — private repos on the Free plan have no branch protection —
and workflows may expose repository secrets. Limits: choose *Only select repositories*, minimal permissions (no Workflows permission, so `.github/workflows` cannot be changed), one-year expiry, one token per sandbox.
Revoking happens on GitHub; Bergfried only deletes its own copies when the sandbox is deleted. Details: [GITHUB.md](GITHUB.md).

## Updates
The app can update itself from GitHub releases. **Releases are not signed**: whoever can publish a release in this repository can ship code that runs on your Mac.
Releases are not technically gated: a push to `dev` or a tag `vX.Y.Z` publishes one. By decision the agent of the development sandbox publishes dev pre-releases on its own, but makes **stable releases only on the maintainer's explicit request** (rule in `CLAUDE.md`; `scripts/release.sh` and `scripts/pr.sh merge … main` refuse without `--user-approved`; this is a rule, not a barrier). A token of your own account cannot be separated from you by rulesets. Keep the **Deployments** and **Workflows** permissions out of sandbox tokens. See [UPDATES.md](UPDATES.md) and [design/develop-in-sandbox.md](design/develop-in-sandbox.md).

## OpenCode desktop connection
OpenCode agents are connected to the OpenCode desktop app with a **one-time pairing link** created by `opencode pair` (without `--remote`). The OpenCode server listens on a stable per-sandbox port on `0.0.0.0` **inside the VM**; Lima forwards it to `127.0.0.1` on the Mac. The link therefore only works on your Mac, not from the internet or the local network. It is valid for 5 minutes and can be used once; the authenticated session is stored for 30 days. Anyone with access to your Mac during those 5 minutes could use the link, so create it when you need it. The link grants control over all OpenCode sessions of that sandbox. `--remote` (internet exposure) is never used by Bergfried.

## Logs
`~/.bergfried/daemon.log` and the app's Diagnostics pane contain error texts, paths and VM names, not tokens or file contents; check before pasting a log somewhere public. The log endpoint (`GET /v1/log`) is on the owner-only app socket and reads fixed files only.

## Known gaps
- The decrypted passphrase key is held in Go memory for the duration of a grant (see above); Go gives no guarantee that every copy is wiped.
- A host key shown for an unknown SSH host must be verified by you; an approved grant could be used by the agent to add its own key to `authorized_keys` on the target. Use a restricted user whose `authorized_keys` is root-owned for important servers.
- "Confirm every SSH connection" exists only in the legacy dialog mode; the app disables it and the daemon refuses it otherwise.
- The agent name shown in requests is claimed from inside the VM. The *sandbox* is derived from the socket and is trustworthy.
- A short time window exists between check and copy of a file (TOCTOU); every file is re-resolved and re-checked at copy time and symlinks are never copied.
