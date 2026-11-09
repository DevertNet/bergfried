# Manual test: updates

Automatic: `make test` covers version comparison, release parsing, hash/bundle-id/version checks, staging and the swap script (including rollback) in `daemon/internal/update`.

Manual: a first real run (dev channel update with progress and errors, rollback, release `v0.1.0` onwards) was confirmed by the maintainer on 2026-11-09; the checklist below stays for regression runs. See [../UPDATES.md](../UPDATES.md). The repository is public, so the daemon can fetch releases without a token.

Channels and rollback:
- Settings → Updates → channel "Dev": after the next push to `dev` the pre-release `X.Y.Z-dev.N` is offered; "Stable" shows only plain releases; switching back does not downgrade.
- After installing a dev build run `…/Bergfried.app/Contents/Resources/bergfriedd update rollback`: the previous version and database come back; check `~/.bergfried/update/update.log`.

Basic update:
1. Build and install an older version: `VERSION=0.0.1 app/build.sh`, copy the bundle to `/Applications`, open it, "Install and start daemon".
2. Push a tag `v0.0.2` (or run the release workflow) and wait for the release.
3. App → Settings → Updates → "Check now" → release notes appear → "Install" → confirm. App and daemon restart; Settings shows 0.0.2; `~/.bergfried/update/Bergfried.app.previous` and the DB backup exist.
4. Corrupt the zip or `SHA256SUMS` of a test release → the install must be refused with a hash error.
5. Check that notifications still work afterwards (permissions may be reset by the new ad-hoc signature).
6. During step 3 the Settings line shows each stage (check, download with MB/percent, checksum, unpack, signature, backup, installing); with a corrupted zip it shows "Update failed: …" and the full error is in the Log pane.
7. After the restart the app explains the macOS folder prompts before the index starts (once per version).
