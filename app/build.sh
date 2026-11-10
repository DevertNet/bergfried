#!/bin/bash
# Builds app/build/Bergfried.app without Xcode/SwiftPM (Command Line Tools only).
# Run `make build` in the repo root first (creates bin/bergfriedd and bin/bergfried-mcp).
# VERSION=1.2.3 app/build.sh stamps the version into Info.plist (default: 0.0.0-dev).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)
VERSION=${VERSION:-0.0.0-dev}
# Rebuild the Go helpers with stripped debug info. The release workflow builds them before
# this script; doing it here keeps the release smaller without changing the workflow.
if command -v go >/dev/null 2>&1; then
  echo "Building stripped Go helpers …"
  mkdir -p "$ROOT/bin"
  ( cd "$ROOT" && \
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -buildid= -X github.com/DevertNet/bergfried/daemon/internal/api.Version=$VERSION" \
      -o bin/bergfriedd ./daemon/cmd/bergfriedd && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -buildid=" \
      -o bin/bergfried-mcp ./mcp/cmd/bergfried-mcp )
else
  echo "WARNING: go not found – using the existing Go helpers (possibly not stripped)"
fi
APP=build/Bergfried.app
rm -rf build && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
swiftc -O -swift-version 5 -parse-as-library -target arm64-apple-macos15.0 \
  -o "$APP/Contents/MacOS/Bergfried" Sources/*.swift
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
 <key>CFBundleIdentifier</key><string>de.einfach123.bergfried</string>
 <key>CFBundleDevelopmentRegion</key><string>en</string>
 <key>CFBundleLocalizations</key><array><string>en</string><string>de</string></array>
 <key>CFBundleIconFile</key><string>AppIcon</string>
 <key>CFBundleName</key><string>Bergfried</string>
 <key>CFBundleExecutable</key><string>Bergfried</string>
 <key>CFBundlePackageType</key><string>APPL</string>
 <key>CFBundleShortVersionString</key><string>${VERSION}</string>
 <key>CFBundleVersion</key><string>${VERSION}</string>
 <key>LSMinimumSystemVersion</key><string>15.0</string>
 <key>NSHighResolutionCapable</key><true/>
 <key>NSAppleEventsUsageDescription</key><string>Bergfried opens Terminal windows for sandboxes.</string>
</dict></plist>
PLIST
cp icon/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
cp -R Resources/*.lproj "$APP/Contents/Resources/"   # localisations (en, de)
for f in bin/bergfriedd bin/bergfried-mcp vm/bergfried.yaml; do
  [ -f "$ROOT/$f" ] && cp "$ROOT/$f" "$APP/Contents/Resources/" || echo "WARNING: $f is missing (run make build?)"
done
codesign --force --deep -s - "$APP"
echo "OK: $APP"
