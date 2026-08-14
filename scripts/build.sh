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
# ヘルパー実行ファイルは Contents/MacOS/ に置く（Resources/ に実行ファイルを置くと
# 署名が nested code として正しく扱われず、TCC が別アプリ扱いして
# 画面収録・アクセシビリティの許可が効かなくなる）
cp build/kindleweb "$APP/Contents/MacOS/kindleweb"
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
    <key>NSAppleEventsUsageDescription</key><string>Kindle アプリを前面に出し、ページをめくるために使用します。</string>
</dict>
</plist>
PLIST

# ローカル開発は ad-hoc 署名・非サンドボックス（~/Documents/PageSnap の読み書き・子プロセス起動のため）
# --deep は非推奨かつヘルパーの識別子を引き継がない（Goが付ける "a.out" のまま残る）ため、
# 内側の実行ファイルから順に、明示的な識別子で署名する。
codesign --force -s - --identifier com.imutaroh.pagesnap.kindleweb "$APP/Contents/MacOS/kindleweb"
codesign --force -s - --identifier com.imutaroh.pagesnap "$APP"
codesign --verify --strict --verbose=2 "$APP"
echo "Built: $APP"
