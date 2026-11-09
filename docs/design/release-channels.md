# Design: release channels (stable and dev) — agreed, not started

Part of the larger plan [develop-in-sandbox.md](develop-in-sandbox.md).

**Why:** developing Bergfried inside a Linux sandbox works for Go, docs and tests, but the app can only be built on macOS. Instead of a 25 GB macOS VM ([macos-sandboxes.md](macos-sandboxes.md), postponed) CI builds the app on a macOS runner, and **GitHub releases carry the builds**. The app can follow one of two channels.

## Channels
| | stable | dev |
|---|---|---|
| Source | tag `vX.Y.Z` on `main` | every push to the branch `dev` |
| GitHub release | normal release | **pre-release** `vX.Y.Z-dev.N` (N = CI run number; X.Y.Z = next patch after the latest stable tag) |
| Who gets it | everyone (default) | users who switch the channel in Settings → Updates |
| Retention | kept | the newest 10 dev pre-releases are kept, older ones (and their tags) are deleted by the workflow |

Ordering follows semver: `0.1.0 < 0.1.1-dev.3 < 0.1.1-dev.17 < 0.1.1`. The **stable** channel ignores pre-releases. The **dev** channel offers the highest version among stable and dev releases, so a stable release newer than your dev build is offered as well. There is no automatic downgrade: after `0.1.1-dev.17` you stay on dev until `0.1.1` (or newer) appears.

## Updater changes (`daemon/internal/update`)
- Setting `update_channel` (`stable` default | `dev`) stored in `~/.bergfried/settings.json` (0600); `GET/PUT /v1/update/channel`; `GET /v1/update` reports the channel.
- Check uses `GET /repos/<owner>/<repo>/releases?per_page=30` (the `/latest` endpoint never returns pre-releases), filters by channel (drafts are always ignored; dev accepts only tags matching `-dev.N`), picks the highest version with a complete asset set.
- `Newer()` learns pre-release ordering (`dev.N` numeric). Everything else (SHA-256 check, bundle checks, swap helper with rollback, database backup) stays as is.
- Tests: ordering table, channel filtering with a fake release list, persistence of the setting, switching the channel re-checks.

## App
Settings → Updates: a picker **Stable / Dev** with a warning for dev ("builds from the development branch; may be broken"); the banner and release notes show the channel. Strings in English and German.

## Workflows
- `ci.yml`: runs on every push to every branch and on pull requests (Go tests, macOS app build) — no release.
- `dev-release.yml` (new): on push to `dev` → tests, build with version `X.Y.Z-dev.N`, zip + `SHA256SUMS`, publish a **pre-release**, prune old dev pre-releases. Uses only `GITHUB_TOKEN` (`contents: write`).
- `release.yml` (existing): tags `vX.Y.Z` only (rejects anything else).
- Workflow files are edited only by you: the sandbox token has no Workflows permission.

## Trust and security
- **The dev channel is "whatever lands on `dev`".** If a sandbox token includes this repository, an agent can push to `dev` and thereby publish a pre-release that dev-channel users can install (each install still needs your confirmation in the app). Keep the stable channel for everything you rely on and use dev only on a Mac where that is acceptable.
- **Protect on GitHub (free for public repos; you set this up):** a ruleset for `main` (pull request required, no bypass for the token's account), a ruleset for tags `v*` (creation restricted, no bypass for the token's account), `dev` left open for agents. Without the tag ruleset an agent could still create a stable `v*` tag.
- Releases remain **unsigned** ([../UPDATES.md](../UPDATES.md)).

## GitHub limits (docs: "About releases", checked 2026-11-09)
Up to **1000 assets per release**, each file under **2 GiB**; **no limit on the total size of a release and no bandwidth limit**. The docs state no limit on the number of releases. Our assets are ~25 MB each, so the retention of 10 dev pre-releases is for tidiness only.

## Plan
1. Docs: this file, `UPDATES.md`, `ROADMAP.md`, `DEVELOPMENT.md`.
2. Updater: channel setting, release list, pre-release ordering, tests.
3. API and app: channel picker, strings (EN/DE), status field.
4. Workflows: `ci.yml` for all branches, `dev-release.yml`, check `release.yml` rejects non-`vX.Y.Z` tags.
5. Create the branch `dev`; run the dev workflow once and install the pre-release from the dev channel (needs the repository to be public or a token for the download).
6. You: set up the rulesets above.

## Limits for the development loop
Still manual: installing a build, notification permission and other macOS prompts, screenshots; no end-to-end tests with real VMs inside a sandbox; no signing/notarisation. Feedback loop for Swift changes: push to `dev` → CI (about 2–3 minutes) → install the pre-release. UI work is easier on your Mac with `tools/demoapi`.
