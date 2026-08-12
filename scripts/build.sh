#!/bin/zsh
# PageSnap（Go サーバー + Swift 殻）をビルドし、PageSnap.app に束ねてad-hoc署名する。
set -euo pipefail
cd "$(dirname "$0")/.."

# ① Go 製 Web サーバー本体（kindleweb）
mkdir -p build
go build -o build/kindleweb ./cmd/kindleweb

# ② Swift 殻（AppKit + WKWebView）
(cd macos && swift build -c release)

# ③ .app に組み立て
APP=build/PageSnap.app
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp macos/.build/release/PageSnap "$APP/Contents/MacOS/PageSnap"
cp build/kindleweb "$APP/Contents/Resources/kindleweb"
[ -f assets/AppIcon.icns ] && cp assets/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key><string>ja</string>
    <key>CFBundleExecutable</key><string>PageSnap</string>
    <key>CFBundleIdentifier</key><string>com.imutaroh.pagesnap</string>
    <key>CFBundleName</key><string>PageSnap</string>
    <key>CFBundleDisplayName</key><string>PageSnap</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>1.0.0</string>
    <key>CFBundleVersion</key><string>1</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <!-- 通常のDockアプリ（メニューバー常駐ではない）なので LSUIElement は付けない -->
    <key>LSApplicationCategoryType</key><string>public.app-category.productivity</string>
    <key>ITSAppUsesNonExemptEncryption</key><false/>
    <key>NSHumanReadableCopyright</key><string>Copyright © 2026 imutaroh. All rights reserved.</string>
</dict>
</plist>
PLIST

# ローカル開発は ad-hoc 署名・非サンドボックス（~/Documents/PageSnap の読み書き・子プロセス起動のため）
codesign --force --deep -s - --identifier com.imutaroh.pagesnap "$APP"
echo "Built: $APP"
