# Manual test: git and GitHub through your own GitHub App

## Automatic
`make test` covers, among others: `ghapp` (key storage 0600, JWT signature and lifetime, token narrowing, revoke, manifest flow against a local stand-in, wrong `state` refused), `gitaccess` (grants, union token, grace time, expiry, sandbox stop, isolation between sandboxes, merge approval every time, full repo access: per repo and sandbox, merge only when ticked, not releasable by the assistant, 24 h limit, token re-minted after 60 min, tokens never in the audit log), `rpc` (a foreign sandbox can neither use nor end a grant), `bergfried-mcp` (credential helper protocol), `sandbox` (guidance text without token recipes, helper script) and `store`.

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
1. Settings → GitHub → "Set up GitHub App …". Check: the browser opens `127.0.0.1:<port>/start`, forwards to GitHub, the form shows the app name `Bergfried-xxxxxx` and the permissions Contents/Pull requests/Actions/Workflows (write), Metadata (read), **no webhook**. If GitHub rejects the manifest or the `127.0.0.1` redirect, note the exact message in [../GITHUB.md](../GITHUB.md) and fix `ghapp/manifest.go`.
2. After "Create GitHub App" the browser shows "Your Bergfried app is ready" and Settings shows the app. `ls -l ~/.bergfried/github-app` → 0600 files, directory 0700.
3. "Install on GitHub …" → choose *Only select repositories* → the test repo. Settings shows "Installed for <account>".
4. Create a sandbox (no token). In Claude: "clone <test repo>" → the assistant calls `request_git_access` → card in "Requests". Allow read+write for 5 minutes. Clone and push a branch work; `~/.git-credentials` does not exist in the VM; `git config --global credential.https://github.com.helper` shows the Bergfried helper.
4b. Multi-repo: ask the assistant to "clone repo A, B, C and D" (all with the app installed). **One** card lists all four; allow once → four grants (four `Task <id>` rows), one token for all (audit: one `git.token` with four repos). Release with the task_ids ends all four and revokes the token right away. If one repo has no installation, no card appears and the assistant is told which one.
5. Without a grant (after `release_git_access` or expiry) `git push` fails with "could not read Username" plus the Bergfried hint; the audit log shows `git.released`/`git.expired` and `git.token_revoked`. On GitHub the token no longer works.
6. `github_create_pr` with an active read+write git grant: no card, the PR opens (audit `git.action.auto`); with a read-only grant or none it produces a card. `github_merge_pr`: every call produces a card; the merge card shows the PR title and branches. On a git grant row, *Allow full access* → duration opens full repo access for that repository including merging (badges FULL ACCESS · MERGE appear); a full access row has *Edit* (duration from now, merge, workflow files; *Save* replaces it). In *Grant full repo access …* a progress indicator shows while the repository list loads. **Check which permissions are really needed** (merge with `contents:write` + `pull_requests:write`, PR with `pull_requests:write` + `contents:read`) and correct `actionPerms` in `gitaccess/actions.go` if GitHub says otherwise.
7. A deliberately failing workflow: `github_ci_status` (needs a grant) lists the run, `github_rerun_ci` asks you.
8. Two grants for two repos of the same owner: both pushes work; releasing one keeps the other working (new narrower token); the replaced token is revoked after about 2 minutes.
9. Check the new token format (`ghs_…`, possibly longer than 40 characters) works with `git push`.
10. "Forget the app" ends grants; delete the test app on GitHub afterwards.

## Full repo access (manual)
1. Requests tab → *Grant full repo access …*: pick a sandbox, enter `owner/name`, 2 hours, merge off (the sheet says that workflow files only come with the extra tick and repository settings never) → *Open window*. The list shows the repo with the badge FULL ACCESS and a countdown.
2. Grant it for a second repo in the **same** sandbox: both are listed. Grant it for the first repo again with 1 hour: the old row disappears, one row remains.
3. In the sandbox ask the assistant to push a branch and open a PR: no card appears; the audit log shows `git.window.used` (if it called `request_git_access`) and `git.action.auto` for the PR. Ask it to merge: a card appears (merge off). Reopen the window with *Also merge pull requests*: the merge runs without a card.
4. The assistant calls `release_git_access` on its task id: nothing changes and it is told only you can end it.
5. *Revoke*: `git push` fails again, the token is revoked on GitHub.
6. A normal git request: tick *Allow full repo access …* in the sheet; the duration list changes to 1–24 h and all repos of that request get full repo access.

## Workflow files (manual, not verified against real GitHub)
1. Existing app: Settings → GitHub shows an orange note "… no Workflows permission …" per installation that lacks it (needs a real app). Add *Workflows: Read and write* in the app settings, accept it for the installation, *Check again*: the note is gone.
2. Grant full repo access for a test repo **with** *Also allow changes to workflow files* (orange warning appears). In the sandbox change and push `.github/workflows/ci.yml`: the push works. Audit log: `git.token … workflows=write`.
3. A second repo of the same owner with a normal write grant: pushing a workflow change there is **rejected** by GitHub (its token has no `workflows`), normal pushes work.
4. Without the permission in the app (step 1 not done): the grant with the tick stays, the credential helper prints a hint naming "Workflows: Read and write"; repos without the tick still work.
5. Lower a write grant with the tick to read-only in the sheet: no workflow access.
6. Let the assistant ask: "change .github/workflows/ci.yml in <repo>" → `request_git_access` with `workflows=true`: the card says "incl. workflow files", shows the orange warning, has **no** *Allow 15 min* button; *Decide …* opens the sheet with the workflow box ticked; *Allow* grants it. Without `workflows` the push of a workflow file is rejected and the assistant is told how to ask.
7. Settings → GitHub: after step 1 (app permission) but before accepting it on the installation the note says "the app has … but the installation … has not accepted it yet" with *Review permissions …*; after accepting and *Check again* it is gone.

## Legacy sandbox with a personal token
Sandboxes that still have `~/.git-credentials` keep working. "Switch to GitHub App" in the sandbox details removes the file, `~/.config/gh/hosts.yml` and the Mac copy; afterwards `git push` without a grant fails and a grant works. Revoke the old token on GitHub.

## Local stand-in
`tools/fakegithub` (REST + git over HTTP) still serves the legacy token tests; the GitHub App code is tested with in-process stand-ins (`httptest`) because the real manifest/redirect flow needs github.com.
For manual development runs the daemon honours `BERGFRIED_GITHUB_API` and `BERGFRIED_GITHUB_WEB` (stand-in for `https://github.com`).
