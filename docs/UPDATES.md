# Updates

Bergfried updates itself from **GitHub releases** of this repository. One release = one asset: `Bergfried-<version>-macos-arm64.zip` (the app bundle; the daemon `bergfriedd`, `bergfried-mcp` and the VM template are inside `Contents/Resources`), plus `SHA256SUMS`.

## Channels
Settings → Updates → **Update channel**:
- **Stable** (default): releases tagged `vX.Y.Z`.
- **Dev**: pre-releases `vX.Y.Z-dev.N` built from every push to the branch `dev` (`.github/workflows/dev-release.yml`; X.Y.Z = next patch after the latest stable tag, N = CI run number), **plus** stable releases. The highest version wins (`0.1.0 < 0.1.1-dev.3 < 0.1.1-dev.17 < 0.1.1`). There is no automatic downgrade: after `0.1.1-dev.17` you stay on dev until `0.1.1` or newer is published. Only the newest 10 dev pre-releases are kept.
The channel is stored in `~/.bergfried/settings.json`. The daemon asks `GET /repos/<owner>/<repo>/releases` (the `/latest` endpoint never returns pre-releases) and ignores drafts.

## Rollback
Every update keeps the previous bundle (`~/.bergfried/update/Bergfried.app.previous`) and a database backup (`~/.bergfried/update/backup-<old version>/`). If an update breaks Bergfried:
```bash
/Applications/Bergfried.app/Contents/Resources/bergfriedd update rollback            # app + database
/Applications/Bergfried.app/Contents/Resources/bergfriedd update rollback --keep-database   # app only
```
(`--app PATH` if the bundle lives elsewhere.) The helper stops daemon and app, restores both and restarts; log: `~/.bergfried/update/update.log`. Database migrations are forward-only, so restore the backup when the new version changed the schema.

## Flow
1. **Release:** pushing a tag `vX.Y.Z` runs `.github/workflows/release.yml` on a macOS runner: tests, build (`go build` for darwin/arm64 and linux/arm64, `app/build.sh` with `VERSION`), zip, `SHA256SUMS`, GitHub release with generated notes.
2. **Check:** the daemon asks `GET /repos/<owner>/<repo>/releases` (public repo, no token; filtered by channel) at start and every 6 hours; the app shows "Update available" (banner in the sandbox list and in Settings → Updates) with the release notes.
3. **Install** (button, after a confirmation; the app shows each step: release check, download with MB and percent, checksum, unpacking, signature check, database backup, "installing – app and daemon restart now"; errors appear as "Update failed: …", full text in the Log pane): the daemon downloads zip and `SHA256SUMS` to `~/.bergfried/update/<version>/`, verifies the SHA-256, unzips, checks bundle id, version (= tag) and that the bundle contains `bergfriedd` and `bergfried-mcp`, runs `codesign --verify`,
   backs up the database (`~/.bergfried/update/backup-<old version>/`) and starts a detached helper script.
4. **Swap:** the helper waits for app and daemon to exit, moves the current bundle to `~/.bergfried/update/Bergfried.app.previous`, copies the new bundle in place (restoring the old one on failure), restarts the LaunchAgent and reopens the app.
5. **VMs:** `bergfried-mcp` inside a VM is replaced at the next sandbox start; the app lists sandboxes that should be restarted.

Updating works only when the daemon runs from an app bundle (LaunchAgent installed by the app). A daemon started by hand (`./bin/bergfriedd serve`) reports "not running from an app bundle".

## Trust model — releases are **not signed**
The SHA-256 only protects against corrupted downloads, not against forgery. Anyone who can publish a release in this repository can ship code that runs on your Mac with your rights.
**Nothing gates a release:** whoever can push to `dev` (dev pre-release) or push a tag `vX.Y.Z` (stable release) publishes one — by decision this includes the agent of the development sandbox for dev pre-releases; stable releases are made by the agent only on the maintainer's explicit request (rule in `CLAUDE.md`, `--user-approved` flag in the scripts, not a technical gate) ([design/develop-in-sandbox.md](design/develop-in-sandbox.md)). Installing in the app still needs your confirmation. Keep the Workflows and Deployments permissions out of sandbox tokens.
Mitigations that exist: you confirm every install, the release notes are shown, the previous version is kept, the database is backed up.
Possible later hardening: signed manifests (ssh-keygen signatures or Secure Enclave key), a separate releases repository, GitHub tag/branch protection (needs a paid plan for private repos; this repository is public).

## Gotchas
- The app is ad-hoc signed, so macOS may reset permissions (notifications) and asks for folder access again after an update. The app therefore explains the folder prompts before the first index build of each version (`index.Gate`). A Developer ID certificate would keep the permissions.
- A newer bundled VM template is adopted automatically if you never edited yours; the new base VM is built on demand (see [ARCHITECTURE.md](ARCHITECTURE.md)).
- A zip downloaded in a **browser** carries the quarantine attribute and Gatekeeper blocks the ad-hoc signed, non-notarized app on first launch (confirmed with v0.1.0): remove it with `xattr -dr com.apple.quarantine Bergfried.app` (see the README). A zip downloaded by the daemon itself carries no quarantine attribute, and in-app updates were not blocked in the maintainer's real runs with dev builds. Notarization needs a paid Apple Developer account.
- Database migrations are forward-only; to roll back restore the backup and the previous bundle manually.

## Configuration (development)
`BERGFRIED_UPDATE_API` (API base, default `https://api.github.com`) and `BERGFRIED_UPDATE_REPO` (default `DevertNet/bergfried`) let tests and forks point the updater elsewhere.
