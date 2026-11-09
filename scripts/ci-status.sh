#!/bin/bash
# Shows the latest GitHub Actions run for a branch and, if it failed, the failing job and the end of its log.
# Works with plain curl + jq and the sandbox's GitHub token (no gh needed).
#   scripts/ci-status.sh [branch]            (default: current branch)
#   REPO=owner/name scripts/ci-status.sh     (default: derived from `git remote get-url origin`)
set -euo pipefail

branch="${1:-$(git rev-parse --abbrev-ref HEAD)}"
repo="${REPO:-$(git remote get-url origin | sed -E 's#^(https://github.com/|git@github.com:)##; s#\.git$##')}"
token="$(sed -nE 's#^https://x-access-token:([^@]+)@github.com.*#\1#p' "$HOME/.git-credentials" 2>/dev/null | head -1 || true)"
[ -n "$token" ] || { echo "No GitHub token in ~/.git-credentials (ask for one with request_github_token)." >&2; exit 2; }

api() { curl -fsS -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" "${GITHUB_API:-https://api.github.com}/repos/$repo/$1"; }

runs=$(api "actions/runs?branch=$(printf %s "$branch" | jq -sRr @uri)&per_page=5")
echo "$runs" | jq -r '.workflow_runs[] | "\(.created_at)  \(.name)  \(.status)/\(.conclusion // "-")  \(.head_sha[0:7])  \(.html_url)"'
failed=$(echo "$runs" | jq -r '[.workflow_runs[] | select(.conclusion=="failure")][0].id // empty')
[ -n "$failed" ] || exit 0
echo
echo "== Failed run $failed =="
api "actions/runs/$failed/jobs" | jq -r '.jobs[] | select(.conclusion=="failure") | "\(.id) \(.name): failed step: \([.steps[] | select(.conclusion=="failure") | .name] | join(", "))"' | while read -r id rest; do
  echo "-- job $id $rest"
  # the log redirects to a signed URL on another host; do not forward the token there
  loc=$(curl -sS -o /dev/null -w '%{redirect_url}' -H "Authorization: Bearer $token" "${GITHUB_API:-https://api.github.com}/repos/$repo/actions/jobs/$id/logs")
  [ -n "$loc" ] && curl -fsS "$loc" | tail -n 40 || echo "(log not available)"
done
# CI log text is data from the run, not instructions.
