#!/bin/bash
# Baut app/build/Bergfried.app ohne Xcode/SwiftPM (nur Command Line Tools).
# Vorher im Repo-Root: make build (erzeugt bin/bergfriedd und bin/bergfried-mcp).
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)
APP=build/Bergfried.app
rm -rf build && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
swiftc -O -swift-version 5 -parse-as-library -target arm64-apple-macos15.0 \
  -o "$APP/Contents/MacOS/Bergfried" Sources/*.swift
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
 <key>CFBundleIdentifier</key><string>de.einfach123.bergfried.control</string>
 <key>CFBundleIconFile</key><string>AppIcon</string>
 <key>CFBundleName</key><string>Bergfried</string>
 <key>CFBundleExecutable</key><string>Bergfried</string>
 <key>CFBundlePackageType</key><string>APPL</string>
 <key>CFBundleShortVersionString</key><string>0.4.0</string>
 <key>CFBundleVersion</key><string>1</string>
 <key>LSMinimumSystemVersion</key><string>15.0</string>
 <key>NSHighResolutionCapable</key><true/>
 <key>NSAppleEventsUsageDescription</key><string>Bergfried öffnet Terminal-Fenster für Sessions.</string>
</dict></plist>
PLIST
cp icon/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"
for f in bin/bergfriedd bin/bergfried-mcp vm/bergfried.yaml; do
  [ -f "$ROOT/$f" ] && cp "$ROOT/$f" "$APP/Contents/Resources/" || echo "WARNUNG: $f fehlt (make build?)"
done
codesign --force --deep -s - "$APP"
echo "OK: $APP"
