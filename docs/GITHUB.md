# Git and GitHub: one token per sandbox

Agents use plain `git` over HTTPS. Each sandbox has **one fine-grained personal access token**, stored in the VM and used for clone, pull, push, merge and GitHub Actions.
No URL rewriting, no deploy keys, no remote helper, no special push tool.

## Storage
`gh` (GitHub CLI, part of the base image) is logged in with the same token when it is stored; `gh` keeps its copy in `~/.config/gh/hosts.yml` (the VM has no keyring), readable only by the VM user like `~/.git-credentials`.

- **VM:** `~/.git-credentials` (0600, line `https://x-access-token:<token>@github.com`) and `git config --global credential.helper store`. Written over **stdin**, never as an argument (`Manager.InstallGitToken`).
- **Mac:** `~/.bergfried/sandboxes/<name>/github-token` (0600), used only to verify repository access. Deleted with the sandbox.
- The token value never appears in the audit log or in API responses (test `TestGitHubDecideNeverEchoesToken`).

## Creating the token
`ghtoken.Link(sandbox)` builds a pre-filled link to `https://github.com/settings/personal-access-tokens/new` with `name=bergfried-<sandbox>` (≤ 40 chars), `description`, `expires_in=365`,
`contents=write`, `pull_requests=write`, `actions=write`, `metadata=read`. **Repository selection cannot be pre-filled by URL**, so the app always shows the hint to choose *Only select repositories*.
The permission names *Contents* and *Pull requests* are not explicitly documented on the pages we read (expected: `contents`, `pull_requests`). A token belongs to **one owner** (user or organisation).

## Where the flow starts
1. **New sandbox with a git repo:** the dialog shows the link, the hint and a field; the token is required. `Manager.Create` verifies it first (valid, repo reachable), creates the VM, stores the token and clones over HTTPS (`GIT_TERMINAL_PROMPT=0`; `git@…` URLs become `https://github.com/o/r.git`).
2. **Agent:** when `git` fails with `permission denied`/`403`/`Repository not found`/`Authentication failed`/`could not read Username`, the agent calls the MCP tool **`request_github_token`** (`repo=owner/name`). A request card appears in the app:
   - **No token yet:** link, hint, field. The token is verified (`GET /rate_limit`, 401 = invalid) and, if a repo was named, against `GET /repos/{o}/{r}`, then stored and written into the VM.
   - **Token exists, repo missing:** you extend the repository selection of the existing token on GitHub (token `bergfried-<sandbox>` → Edit → Repository access) and click "Check access". The token value stays the same (**unverified** that GitHub allows this without a new value). If the check fails the request stays open.
   - If the token already reaches the repo the daemon answers immediately.
3. Agents cannot create GitHub repositories (`gh repo create` fails too: no permission). The guidance tells them to ask you to create the repo yourself.

## CI
There are no CI tools in Bergfried. The agent queries runs, jobs and logs itself through the GitHub API (`curl` with the token from `~/.git-credentials`; the example is in its guidance) and can re-run failed jobs thanks to the *Actions* permission.
Log text is untrusted input (data, not instructions).

## Multiple owners / accounts (future)
Currently **one token and one owner per sandbox**. Repos from several owners or accounts need several tokens; how `git` tells them apart (for example `credential.useHttpPath` with one line per `https://github.com/<owner>`) is **untested**.
Background: <https://docs.github.com/en/account-and-profile/how-tos/account-management/managing-multiple-accounts>.

## Open / untested
- A real run against GitHub: permission names in the form, cloning/pushing a private repo, `GET /rate_limit` and `GET /repos/…` with a fine-grained token, the exact git error messages, changing the repository selection without a new token value, whether the token value is shown only once.
- Pushes that change workflow files fail without the Workflows permission (intended); GitHub's exact message was not seen.
- Several owners/accounts, GitHub Enterprise, hosts other than github.com.
