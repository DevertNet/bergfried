#!/bin/bash
# Builds the app from this checkout and offers it to the installed Bergfried via the update channel "Local"
# (Settings -> Updates -> Channel -> Local). Nothing goes to GitHub.
#   scripts/local-release.sh
# Writes Bergfried-<base>-local.<timestamp>-macos-arm64.zip + SHA256SUMS to ~/.bergfried/local-update (older local builds are removed).
# <base> = X.Y.Z of the highest tag (stable or dev), so the build looks like what it is built from. Then press "Check now" and "Install" in the app.
set -euo pipefail
cd "$(dirname "$0")/.."
last="$(git tag --list 'v[0-9]*' | sed -E 's/^v//; s/-.*//' | sort -V | tail -1)"
ver="${last:-0.0.0}-local.$(date +%Y%m%d%H%M%S)"
out="${BERGFRIED_HOME:-$HOME/.bergfried}/local-update"
echo "Building $ver …"
make build VERSION="$ver"
VERSION="$ver" app/build.sh
( cd app/build && ditto -c -k --keepParent Bergfried.app "Bergfried-$ver-macos-arm64.zip" && shasum -a 256 "Bergfried-$ver-macos-arm64.zip" > SHA256SUMS )
mkdir -p "$out" && chmod 700 "$out"
rm -f "$out"/Bergfried-*-macos-arm64.zip "$out"/SHA256SUMS
mv "app/build/Bergfried-$ver-macos-arm64.zip" app/build/SHA256SUMS "$out/"
echo "OK: $out – in the app: Settings → Updates → Channel \"Local\" → Check now → Install."
