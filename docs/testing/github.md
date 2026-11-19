# Manual test: git and GitHub through your own GitHub App

## Automatic
`make test` covers, among others: `ghapp` (key storage 0600, JWT signature and lifetime, token narrowing, revoke, manifest flow against a local stand-in, wrong `state` refused), `gitaccess` (grants, union token, grace time, expiry, sandbox stop, isolation between sandboxes, merge approval every time, tokens never in the audit log), `rpc` (a foreign sandbox can neither use nor end a grant), `bergfried-mcp` (credential helper protocol), `sandbox` (guidance text without token recipes, helper script) and `store`.

## Credential helper with real git (no GitHub, no VM)
Verified on 2026-11-18 with git 2.54: a helper configured as `credential.https://github.com.helper = !<script>` plus `credential.https://github.com.useHttpPath = true` receives `get` with `path=o/r.git`; if it prints nothing, `git credential fill` fails with "could not read Username" and shows the helper's stderr hint.
```bash
export HOME=$(mktemp -d)   # never touch your real ~/.gitconfig
git config --global credential.https://github.com.useHttpPath true
git config --global --replace-all credential.https://github.com.helper '!/path/to/your/test-helper.sh'
printf 'protocol=https\nhost=github.com\npath=o/r.git\n\n' | GIT_TERMINAL_PROMPT=0 git credential fill
```
Note: `useHttpPath` hides a host-only `~/.git-credentials` entry from the `store` helper (legacy sandboxes), which is why the VM setup only enables it when that file does not exist.

## Setup against real GitHub (not done yet — this is the open verification)
Use a **separate test GitHub account or a throwaway private repo**, never your real repos.
1. Settings → GitHub → "Set up GitHub App …". Check: the browser opens `127.0.0.1:<port>/start`, forwards to GitHub, the form shows the app name `Bergfried-xxxxxx` and the permissions Contents/Pull requests/Actions (write), Metadata (read), **no webhook**. If GitHub rejects the manifest or the `127.0.0.1` redirect, note the exact message in [../GITHUB.md](../GITHUB.md) and fix `ghapp/manifest.go`.
2. After "Create GitHub App" the browser shows "Your Bergfried app is ready" and Settings shows the app. `ls -l ~/.bergfried/github-app` → 0600 files, directory 0700.
3. "Install on GitHub …" → choose *Only select repositories* → the test repo. Settings shows "Installed for <account>".
4. Create a sandbox (no token). In Claude: "clone <test repo>" → the assistant calls `request_git_access` → card in "Requests". Allow read+write for 5 minutes. Clone and push a branch work; `~/.git-credentials` does not exist in the VM; `git config --global credential.https://github.com.helper` shows the Bergfried helper.
4b. Multi-repo: ask the assistant to "clone repo A, B, C and D" (all with the app installed). **One** card lists all four; allow once → four grants (four `Task <id>` rows), one token for all (audit: one `git.token` with four repos). Release with the task_ids ends all four and revokes the token right away. If one repo has no installation, no card appears and the assistant is told which one.
5. Without a grant (after `release_git_access` or expiry) `git push` fails with "could not read Username" plus the Bergfried hint; the audit log shows `git.released`/`git.expired` and `git.token_revoked`. On GitHub the token no longer works.
6. `github_create_pr` and `github_merge_pr`: every call produces a card; the merge card shows the PR title and branches. **Check which permissions are really needed** (merge with `contents:write` + `pull_requests:write`, PR with `pull_requests:write` + `contents:read`) and correct `actionPerms` in `gitaccess/actions.go` if GitHub says otherwise.
7. A deliberately failing workflow: `github_ci_status` (needs a grant) lists the run, `github_rerun_ci` asks you.
8. Two grants for two repos of the same owner: both pushes work; releasing one keeps the other working (new narrower token); the replaced token is revoked after about 2 minutes.
9. Check the new token format (`ghs_…`, possibly longer than 40 characters) works with `git push`.
10. "Forget the app" ends grants; delete the test app on GitHub afterwards.

## Legacy sandbox with a personal token
Sandboxes that still have `~/.git-credentials` keep working. "Switch to GitHub App" in the sandbox details removes the file, `~/.config/gh/hosts.yml` and the Mac copy; afterwards `git push` without a grant fails and a grant works. Revoke the old token on GitHub.

## Local stand-in
`tools/fakegithub` (REST + git over HTTP) still serves the legacy token tests; the GitHub App code is tested with in-process stand-ins (`httptest`) because the real manifest/redirect flow needs github.com.
For manual development runs the daemon honours `BERGFRIED_GITHUB_API` and `BERGFRIED_GITHUB_WEB` (stand-in for `https://github.com`).
