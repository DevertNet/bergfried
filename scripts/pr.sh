#!/bin/bash
# Pull requests with plain curl + jq and the sandbox token (no gh needed).
#   scripts/pr.sh open <base> "<title>" ["<body>"]   open a PR from the current branch into <base> (dev or main)
#   scripts/pr.sh merge <number>                     merge a PR (squash into dev, merge commit into main), only if CI is green on its head commit
# REPO=owner/name overrides the repository derived from `git remote get-url origin`; GITHUB_API overrides the API base (tests).
set -euo pipefail

repo="${REPO:-$(git remote get-url origin | sed -E 's#^(https://github.com/|git@github.com:)##; s#\.git$##')}"
base_api="${GITHUB_API:-https://api.github.com}"
token="$(sed -nE 's#^https://x-access-token:([^@]+)@github.com.*#\1#p' "$HOME/.git-credentials" 2>/dev/null | head -1 || true)"
[ -n "$token" ] || { echo "No GitHub token in ~/.git-credentials (ask for one with request_github_token)." >&2; exit 2; }
api() { curl -fsS -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" "$@"; }

case "${1:-}" in
  open)
    [ $# -ge 3 ] || { echo "usage: pr.sh open <base> <title> [body]" >&2; exit 2; }
    base="$2"; title="$3"; body="${4:-}"
    head="$(git rev-parse --abbrev-ref HEAD)"
    [ "$head" != "$base" ] || { echo "current branch is the base branch" >&2; exit 2; }
    payload=$(jq -n --arg t "$title" --arg h "$head" --arg b "$base" --arg body "$body" '{title:$t, head:$h, base:$b, body:$body}')
    api -X POST -d "$payload" "$base_api/repos/$repo/pulls" | jq -r '"PR #\(.number): \(.html_url)"'
    ;;
  merge)
    [ $# -eq 2 ] || { echo "usage: pr.sh merge <number>" >&2; exit 2; }
    n="$2"
    pr=$(api "$base_api/repos/$repo/pulls/$n")
    state=$(echo "$pr" | jq -r .state); sha=$(echo "$pr" | jq -r .head.sha); base=$(echo "$pr" | jq -r .base.ref)
    [ "$state" = "open" ] || { echo "PR #$n is $state" >&2; exit 1; }
    runs=$(api "$base_api/repos/$repo/actions/runs?head_sha=$sha&per_page=30")
    total=$(echo "$runs" | jq '.workflow_runs | length')
    bad=$(echo "$runs" | jq '[.workflow_runs[] | select(.status != "completed" or (.conclusion != "success" and .conclusion != "skipped"))] | length')
    if [ "$total" -eq 0 ]; then echo "No CI run for $sha yet - not merging." >&2; exit 1; fi
    if [ "$bad" -ne 0 ]; then
      echo "CI is not green for $sha ($bad of $total runs unfinished or failed) - not merging. See scripts/ci-status.sh." >&2
      exit 1
    fi
    # squash into dev (one commit per change); a real merge commit into main so dev and main do not diverge
    method=squash; [ "$base" = "main" ] && method=merge
    api -X PUT -d "{\"merge_method\":\"$method\"}" "$base_api/repos/$repo/pulls/$n/merge" | jq -r '"merged into '"$base"': \(.sha)"'
    ;;
  *)
    echo "usage: pr.sh open <base> <title> [body] | merge <number>" >&2; exit 2 ;;
esac
