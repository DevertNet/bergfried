# Manual test: git and GitHub with the sandbox token

## Automatic
`make test` (among others: `ghtoken` against a local GitHub stand-in, `github`, `sandbox` — token only over stdin —, `api` — token never in responses or the audit log).

## Mechanism locally (no VM, no real GitHub)
With `tools/fakegithub` (REST + git over HTTP, basic auth):
```bash
make fakegithub
S=/private/tmp/bft; mkdir -p $S/repos/o $S/home
git init -q --bare $S/repos/o/r.git && git -C $S/repos/o/r.git config http.receivepack true
./bin/fakegithub -addr 127.0.0.1:8099 -root $S/repos -token github_pat_TEST0123456789abcdef &
# fresh HOME so neither ~/.gitconfig nor the keychain is touched
export HOME=$S/home GIT_TERMINAL_PROMPT=0
printf 'http://x-access-token:github_pat_TEST0123456789abcdef@127.0.0.1:8099\n' > $HOME/.git-credentials && chmod 600 $HOME/.git-credentials
git config --global credential.helper store
git clone http://127.0.0.1:8099/o/r.git $S/work   # works; with a wrong token: "Authentication failed"
```
Afterwards stop `fakegithub` and delete `/private/tmp/bft`. (Verified on 2026-11-08: clone and push with the `store` helper work, a wrong token fails.)

## Manually against real GitHub (done once by the maintainer on 2026-11-09; repeat after changes to the token flow)
1. Create a private test repo. New sandbox with a git repo: in the dialog click "Create token on GitHub …" — check the form is pre-filled (name `bergfried-<sandbox>`, one-year expiry, permissions Contents/Pull requests/Actions/Metadata), choose *Only select repositories* → only the test repo, paste the token, create.
2. In the VM (Shell): `git -C ~/work/main push` to a branch; `ls -l ~/.git-credentials` (0600).
3. Second repo: in Claude `git clone https://github.com/<owner>/<other-repo>` → fails → the assistant calls `request_github_token` → card in "Requests" → add the repo to the token on GitHub → "Check access".
4. A workflow with a deliberate failure: the assistant reads runs/logs with `curl` and the token and re-runs the job (check the Actions permission).
5. Compare permission names and behaviour with [../GITHUB.md](../GITHUB.md) and fix the docs if they differ. Revoke the test token on GitHub afterwards.
