# Manual test: updates

Automatic: `make test` covers version comparison, release parsing, hash/bundle-id/version checks, staging and the swap script (including rollback) in `daemon/internal/update`.

Manual (needs a published release; **not done yet**, see [../UPDATES.md](../UPDATES.md)):
1. Build and install an older version: `VERSION=0.0.1 app/build.sh`, copy the bundle to `/Applications`, open it, "Install and start daemon".
2. Push a tag `v0.0.2` (or run the release workflow) and wait for the release.
3. App → Settings → Updates → "Check now" → release notes appear → "Install" → confirm. App and daemon restart; Settings shows 0.0.2; `~/.bergfried/update/Bergfried.app.previous` and the DB backup exist.
4. Corrupt the zip or `SHA256SUMS` of a test release → the install must be refused with a hash error.
5. Check that notifications still work afterwards (permissions may be reset by the new ad-hoc signature).
