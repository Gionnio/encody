#!/bin/bash
# Compila motore Go + app SwiftUI e crea build/Encody.app (motore incluso nel bundle).
# Requisiti: Go, Xcode Command Line Tools (Swift 5.9+), macOS 14+.
#
# Uso: ./build_app.sh            → build/Encody.app
#      ./build_app.sh --install  → anche installata in /Applications e avviata
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(pwd)"
BUILD="$ROOT/build"
APP="$BUILD/Encody.app"
VERSION="1.0.2"
BUILD_NUMBER="3"

echo "▸ Motore Go"
mkdir -p "$BUILD"
(cd engine && go build -trimpath -ldflags="-s -w" -o "$BUILD/encody" .)

echo "▸ App SwiftUI"
(cd app && swift build -c release)
BIN_DIR="$(cd app && swift build -c release --show-bin-path)"

echo "▸ Bundle"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN_DIR/Encody" "$APP/Contents/MacOS/Encody"
cp "$BUILD/encody" "$APP/Contents/Resources/encody"
# Lingue: italiano (base, le chiavi sono il testo italiano) e inglese
cp -R "$ROOT/app/Resources/"*.lproj "$APP/Contents/Resources/"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>Encody</string>
    <key>CFBundleDisplayName</key><string>Encody</string>
    <key>CFBundleIdentifier</key><string>com.github.gionnio.Encody</string>
    <key>CFBundleExecutable</key><string>Encody</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>${VERSION}</string>
    <key>CFBundleVersion</key><string>${BUILD_NUMBER}</string>
    <key>CFBundleDevelopmentRegion</key><string>it</string>
    <key>CFBundleLocalizations</key><array><string>it</string><string>en</string></array>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>LSApplicationCategoryType</key><string>public.app-category.video</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSHumanReadableCopyright</key><string>Copyright © 2026 Gionnio. MIT License.</string>
</dict>
</plist>
PLIST

# Icona: rigenerata con iconutil dall'iconset (icon/), altrimenti AppIcon.icns già pronto
ICNS="$BUILD/AppIcon.icns"
if [ -d "$ROOT/icon/AppIcon.iconset" ] && command -v iconutil >/dev/null; then
    iconutil -c icns "$ROOT/icon/AppIcon.iconset" -o "$ICNS"
elif [ -f "$ROOT/AppIcon.icns" ]; then
    cp "$ROOT/AppIcon.icns" "$ICNS"
fi
if [ -f "$ICNS" ]; then
    cp "$ICNS" "$APP/Contents/Resources/AppIcon.icns"
    /usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string AppIcon" "$APP/Contents/Info.plist"
fi

codesign --force --deep --sign - "$APP" >/dev/null
touch "$APP" # forza il Finder/Dock a rileggere l'icona
echo "✅ $APP"
echo "   Il motore resta usabile anche da terminale: $BUILD/encody"

if [ "${1:-}" = "--install" ]; then
    echo "▸ Installo in /Applications"
    osascript -e 'quit app "Encody"' 2>/dev/null || true
    sleep 1
    rm -rf /Applications/Encody.app
    cp -R "$APP" /Applications/
    codesign --force --deep --sign - /Applications/Encody.app >/dev/null
    open /Applications/Encody.app
    echo "✓ Encody installata. Dopo una reinstallazione macOS può chiedere di nuovo il permesso per le notifiche."
fi
