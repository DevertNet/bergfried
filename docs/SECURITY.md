# Security model

## What Bergfried protects against
An assistant (or anything it runs, including prompt-injected instructions) misusing your Mac: reading your files, using your SSH keys, or reaching your credentials.
**It is not a defence against a malicious VM escaping the hypervisor**, and it does not stop the assistant from doing harm *inside* its own VM or with the GitHub token you give it.

## What the assistants can and cannot do

| | Assistant in the VM |
|---|---|
| See your home, keys, host Docker | **Never.** Only `shared/in` (ro), `shared/out` and the shared `skills` folder (ro) are mounted. |
| Read your files | Only copies you approve, one request at a time. Blocked paths are refused without asking. |
| Use your SSH keys | Only during a grant you create (time window, key, target host key). The private key never leaves the Mac. Keys that are not loaded in your ssh-agent can be granted too, but only with the passphrase you type for that grant (see below). |
| Move data between sandboxes | **Never by itself.** Copying/moving a folder to another sandbox exists only in the app (no MCP tool). It runs through the daemon from VM to VM, so a sandbox cannot reach another sandbox's files on its own. |
| Use GitHub | Only through **your GitHub App** (see below): a short-lived token while a git grant you approved is active; merge, pull requests and CI re-runs run in the daemon after you allowed each one. |
| Reach the network | Yes (the VM has normal outbound access). |
| `sudo` in the VM | Yes (Lima default); assistants are told to install missing software themselves with `sudo apt-get install`. The boundary is the VM, not the Linux user. |

## Invariants (covered by tests, please do not weaken)
- The VM never sees your home, keys or the host Docker. Mounts: only `shared/in` (ro), `shared/out` and the shared `skills` folder (ro).
  Skills are instructions for the assistants, so the folder is read-only for the VM; changes are made on the Mac or through a proposal the user allowed. Whether the read-only mount is really enforced on the host side still has to be verified with a real Lima VM.
- A skills proposal is applied only after the user allowed it, and the app shows the full diff. Validated before the request and again before applying: the path shape and depth, no hidden names, the reserved `synced` name, text only (UTF-8, no NUL/control/invisible characters), 30 files / 128 KB per file / 512 KB in total, no symlinks, and no overwrite of a file that changed since the proposal (hash check). Audit events: `skills.requested`, `skills.allowed`, `skills.denied`, `skills.expired`, `skills.apply_failed`.
- Skills content is visible to every sandbox: never put secrets in a skill. A skill can contain scripts that run in the VM, so only add skills you trust; on the Mac you are responsible for what you put there.
- The private SSH key never leaves the Mac. For keys in your ssh-agent signing happens upstream (the daemon never sees the key), only with a grant plus a host-key binding; agent forwarding is refused. The one exception is the passphrase-key mode below.
- Blocklist: case-insensitive compare (macOS), `..`/absolute paths/symlink escapes fail (`index.Resolve`), symlinks are never followed or copied. `PUT /v1/blocklist` cannot remove the three fixed entries (`.ssh`, `Library`, `.config`).
- Assistant-provided text (reason, name, user, host) is display data only: sanitised, never interpolated into shell or AppleScript source; host names and users are validated by regex before they reach `~/.ssh/config` blocks.
- Releasing an SSH task (`release_ssh`, `Service.Release`) only reduces access: it revokes one grant and needs no approval. It works only for grants of the calling sandbox (identity = socket); an unknown id and an id of another sandbox get the same answer (`No SSH task <id> in this sandbox.` plus the caller's active tasks), so a sandbox cannot probe for foreign ids.
- All sockets are 0600 in 0700 directories; `~/.bergfried` is 0700.
- `get_host_address` returns the Mac's LAN IPv4 address(es) and `.local` name to the assistant without asking (every device on your network sees them anyway; no VPN/virtual interfaces). It is audited as `host.address` with the number of addresses only. It does not open any port.
- Everything is audited (`audit` table; app "Audit log", CSV export with formula protection). Audit failures are not swallowed.

## SSH keys with a passphrase that are not loaded (deliberate exception)
The app also offers key pairs from `~/.ssh` (`<name>` plus `<name>.pub`) that are **not** in your ssh-agent. For such a key the daemon cannot ask an agent to sign, so:
- you type the **passphrase for this grant** in the app; the daemon reads the private key file, decrypts it **in its own memory** and signs itself for as long as the grant lasts (until expiry or revocation, or until the daemon stops);
- the key is **never added to your ssh-agent**, never written to disk, the passphrase is not stored, not logged and not audited (the slice is zeroed after use), and a new grant needs the passphrase again;
- when the grant ends the decrypted key is dropped and, for ed25519 keys, zeroed (other key types are only released to the garbage collector: best effort); extending a grant after that is refused;
- every use still needs a valid grant and the target host key binding, and the audit log says `source=key file, decrypted in memory …`.
What changes compared with ssh-agent keys: **the decrypted key lives in the daemon process** for the length of the grant (a bug in or a memory dump of the daemon could expose it, and the daemon reads private key files from `~/.ssh`). Assistants in the VM still never see the key, only signatures within the grant. Choose short durations for such grants. Tests: `daemon/internal/broker/localkeys_test.go`.

## GitHub: your own GitHub App (deliberate trade-offs)
The private key of the app lives on the Mac only (`~/.bergfried/github-app/`, 0600, never mounted into a VM). There is no standing token in a VM.
- **Git grants** (like SSH grants): during a grant you approved, a credential helper in the VM receives an installation token narrowed to the granted repositories and `contents: read|write`. It expires after at most 60 minutes and is revoked on GitHub when the grant ends (`release_git_access`, expiry, "Revoke", sandbox stop or delete). The assistant can read that token while it lives, and can **push to any branch of the granted repos (including `main`, including force pushes)** unless GitHub protects the branch — private repos on the Free plan have no branch protection. Administration is not part of the app, so no repositories can be created. The app holds the **Workflows** permission, but a token only gets it for a repository whose grant you ticked *Also allow changes to workflow files* (the assistant may ask for it with `workflows=true`; the card is pre-ticked and warns, there is no one-click allow, and you decide): such repos get their own token (`workflows: write` + `contents: write`), other repos of the owner keep a token without it. Under that tick the assistant can change any workflow file of the repo, and workflow code runs on GitHub with the repository's secrets, so it can leak or misuse them; use it rarely and briefly (see [GITHUB.md](GITHUB.md#workflow-files)). Without the tick GitHub rejects such pushes.
- **Full repo access** (your own offer for one repo and 1–24 hours, see [GITHUB.md](GITHUB.md)): while it runs the sandbox gets git read/write, pull requests and CI re-runs **without a card**; merging only if you ticked it. Opening a pull request also needs no card with a plain git grant with write access (the branch had to be pushed under that grant); CI re-runs and merging are only free inside full repo access. It is bound to one repository in one sandbox, the assistant cannot end it (you or the deadline do), and every automatically allowed action is audited (`git.action.auto`). Choose short durations and leave merge and workflow files off unless you need them. Extending a grant or window (`git.extended`) is possible only from the app; the assistant cannot do it. The app's one-click *Allow N min* on a git request allows exactly the level that was asked for at most 15 minutes; use *Decide …* to lower it.
- **Releasing is a rule, not a barrier.** If an assistant forgets, the timer ends the grant. A copied token is useless after revocation.
- **Merge, pull requests, CI re-runs** (pull requests: only without a write grant) never use a token in the VM: the daemon mints a token for that single call (one repo, minimal permissions), executes and revokes it. You approve each call; the card shows PR title and branches as read from GitHub.
- The grant belongs to the **sandbox**: all assistants of a VM share the Linux user, so one assistant's grant can be used by another in the same VM. Several grants of a sandbox share one token (union of repos and levels).
- Tokens are stored in the database (0600) only so the daemon can revoke them; they never appear in API responses, audit entries or logs.
- Older sandboxes with a personal access token keep the old trade-off (token readable in the VM, valid for a year, all permissions at all times) until you switch them to the app in the sandbox details. Details: [GITHUB.md](GITHUB.md).

## Updates
The app can update itself from GitHub releases. **Releases are not signed**: whoever can publish a release in this repository can ship code that runs on your Mac.
Releases are not technically gated: a push to `dev` or a tag `vX.Y.Z` publishes one. By decision the assistant of the development sandbox publishes dev pre-releases on its own, but makes **stable releases only on the maintainer's explicit request** (rule in `CLAUDE.md`; `scripts/release.sh` and `scripts/pr.sh merge … main` refuse without `--user-approved`; this is a rule, not a barrier). A token of your own account cannot be separated from you by rulesets. For a development sandbox keep the **Deployments** and **Workflows** permissions out of its token (the Bergfried GitHub App itself holds Workflows but hands it out only on your tick, see above). See [UPDATES.md](UPDATES.md) and [design/develop-in-sandbox.md](design/develop-in-sandbox.md).

## OpenCode desktop connection
OpenCode is connected to the OpenCode desktop app with a **one-time pairing link** created by `opencode pair` (without `--remote`). The OpenCode server listens on a stable per-sandbox port on `0.0.0.0` **inside the VM**; Lima forwards it to `127.0.0.1` on the Mac. The link therefore only works on your Mac, not from the internet or the local network. It is valid for 5 minutes and can be used once; the authenticated connection is stored for 30 days. Bergfried uses a dedicated port per sandbox (42000–42999), not the OpenCode default 49374: a local OpenCode service on the Mac can occupy 49374, and a link on that port is not from Bergfried. The daemon restarts the shared service before creating the link and rejects a link on the wrong port. Anyone with access to your Mac during those 5 minutes could use the link, so create it when you need it. The link grants control over all OpenCode sessions in that sandbox (one service, all folders). `--remote` (internet exposure) is never used by Bergfried.

## Logs
`~/.bergfried/daemon.log` and the app's Diagnostics pane contain error texts, paths and VM names, not tokens or file contents; check before pasting a log somewhere public. The log endpoint (`GET /v1/log`) is on the owner-only app socket and reads fixed files only.

## Known gaps
- The decrypted passphrase key is held in Go memory for the duration of a grant (see above); Go gives no guarantee that every copy is wiped.
- A host key shown for an unknown SSH host must be verified by you; an approved grant could be used by the assistant to add its own key to `authorized_keys` on the target. Use a restricted user whose `authorized_keys` is root-owned for important servers.
- "Confirm every SSH connection" exists only in the legacy dialog mode; the app disables it and the daemon refuses it otherwise.
- The folder path shown in requests is claimed from inside the VM. The *sandbox* is derived from the socket and is trustworthy.
- A short time window exists between check and copy of a file (TOCTOU); every file is re-resolved and re-checked at copy time and symlinks are never copied.
