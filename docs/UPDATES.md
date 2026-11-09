# Updates

Bergfried updates itself from **GitHub releases** of this repository. One release = one asset: `Bergfried-<version>-macos-arm64.zip` (the app bundle; the daemon `bergfriedd`, `bergfried-mcp` and the VM template are inside `Contents/Resources`), plus `SHA256SUMS`.

## Flow
1. **Release:** pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml` on a macOS runner: tests, build (`go build` for darwin/arm64 and linux/arm64, `app/build.sh` with `VERSION`), zip, `SHA256SUMS`, GitHub release with generated notes.
2. **Check:** the daemon asks `GET /repos/<owner>/<repo>/releases/latest` (public repo, no token) at start and every 6 hours; the app shows "Update available" (banner in the sandbox list and in Settings → Updates) with the release notes.
3. **Install** (button, after a confirmation): the daemon downloads zip and `SHA256SUMS` to `~/.bergfried/update/<version>/`, verifies the SHA-256, unzips, checks bundle id, version (= tag) and that the bundle contains `bergfriedd` and `bergfried-mcp`, runs `codesign --verify`,
   backs up the database (`~/.bergfried/update/backup-<old version>/`) and starts a detached helper script.
4. **Swap:** the helper waits for app and daemon to exit, moves the current bundle to `~/.bergfried/update/Bergfried.app.previous`, copies the new bundle in place (restoring the old one on failure), restarts the LaunchAgent and reopens the app.
5. **VMs:** `bergfried-mcp` inside a VM is replaced at the next sandbox start; the app lists sandboxes that should be restarted.

Updating works only when the daemon runs from an app bundle (LaunchAgent installed by the app). A daemon started by hand (`./bin/bergfriedd serve`) reports "not running from an app bundle".

## Trust model — releases are **not signed**
The SHA-256 only protects against corrupted downloads, not against forgery. Anyone who can publish a release in this repository can ship code that runs on your Mac with your rights.
Because the per-sandbox GitHub token has `Contents: write` on its repositories, **never include this repository in a sandbox token** unless you accept that an agent could publish a release.
Mitigations that exist: you confirm every install, the release notes are shown, the previous version is kept, the database is backed up.
Possible later hardening: signed manifests (ssh-keygen signatures or Secure Enclave key), a separate releases repository, GitHub tag/branch protection (needs a paid plan for private repos; this repository is public).

## Gotchas
- The app is ad-hoc signed, so macOS may reset permissions (notifications) after an update. A Developer ID certificate would keep them.
- A zip downloaded in a **browser** carries the quarantine attribute and Gatekeeper blocks the ad-hoc signed, non-notarized app on first launch (confirmed with v0.1.0): remove it with `xattr -dr com.apple.quarantine Bergfried.app` (see the README). A zip downloaded by the daemon itself carries no quarantine attribute, so in-app updates should not be blocked (**untested**). Notarization needs a paid Apple Developer account.
- Database migrations are forward-only; to roll back restore the backup and the previous bundle manually.

## Configuration (development)
`BERGFRIED_UPDATE_API` (API base, default `https://api.github.com`) and `BERGFRIED_UPDATE_REPO` (default `DevertNet/bergfried`) let tests and forks point the updater elsewhere.
