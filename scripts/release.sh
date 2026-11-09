#!/bin/bash
# Publishes a stable release: tags the current main commit vX.Y.Z; .github/workflows/release.yml then builds and publishes it.
#   scripts/release.sh vX.Y.Z --user-approved [--dry-run]
# Stable releases only happen when the USER explicitly asks for one; --user-approved states that (agents: never pass it on your own initiative).
# Refuses unless: you are on an up-to-date, clean main; the version is higher than the latest stable tag; the tag does not exist;
# CI is green for the commit. Uses plain curl + jq and the sandbox token (no gh). GITHUB_API overrides the API base (tests).
set -euo pipefail

ver="${1:-}"; shift || true
dry=""; approved=""
for a in "$@"; do case "$a" in --dry-run) dry=--dry-run ;; --user-approved) approved=1 ;; *) echo "unknown option $a" >&2; exit 2 ;; esac; done
[[ "$ver" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: release.sh vX.Y.Z --user-approved [--dry-run]" >&2; exit 2; }
[ -n "$dry" ] || [ -n "$approved" ] || { echo "Stable releases need the user's explicit instruction; pass --user-approved only if the user asked for this release (use --dry-run to check)." >&2; exit 2; }
repo="${REPO:-$(git remote get-url origin | sed -E 's#^(https://github.com/|git@github.com:)##; s#\.git$##')}"
base_api="${GITHUB_API:-https://api.github.com}"
token="$(sed -nE 's#^https://x-access-token:([^@]+)@github.com.*#\1#p' "$HOME/.git-credentials" 2>/dev/null | head -1 || true)"
[ -n "$token" ] || { echo "No GitHub token in ~/.git-credentials." >&2; exit 2; }

[ "$(git rev-parse --abbrev-ref HEAD)" = "main" ] || { echo "not on main" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "working tree is not clean" >&2; exit 1; }
git fetch -q origin main --tags
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "main is not up to date with origin/main" >&2; exit 1; }
! git rev-parse -q --verify "refs/tags/$ver" >/dev/null || { echo "tag $ver already exists" >&2; exit 1; }
last="$(git tag --list 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1 || true)"
if [ -n "$last" ] && [ "$(printf '%s\n%s\n' "$last" "$ver" | sort -V | tail -1)" != "$ver" ]; then
  echo "$ver is not higher than the latest stable release $last" >&2; exit 1
fi

sha="$(git rev-parse HEAD)"
runs=$(curl -fsS -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" "$base_api/repos/$repo/actions/runs?head_sha=$sha&per_page=30")
total=$(echo "$runs" | jq '.workflow_runs | length')
bad=$(echo "$runs" | jq '[.workflow_runs[] | select(.status != "completed" or (.conclusion != "success" and .conclusion != "skipped"))] | length')
[ "$total" -gt 0 ] || { echo "No CI run for $sha - wait for CI." >&2; exit 1; }
[ "$bad" -eq 0 ] || { echo "CI is not green for $sha ($bad of $total runs unfinished or failed) - see scripts/ci-status.sh." >&2; exit 1; }

echo "Releasing $ver at ${sha:0:7} (previous: ${last:-none}). Changes:"
git log --oneline "${last:+$last..}HEAD" | head -40
[ "$dry" = "--dry-run" ] && { echo "(dry run: nothing tagged)"; exit 0; }
git tag -a "$ver" -m "Bergfried ${ver#v}"
git push origin "$ver"
echo "Tag pushed. The release workflow builds it: https://github.com/$repo/actions"
