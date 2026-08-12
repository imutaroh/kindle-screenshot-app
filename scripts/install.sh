#!/bin/zsh
# PageSnap をビルドして /Applications にインストールし、起動し直す。
set -euo pipefail
cd "$(dirname "$0")/.."

./scripts/build.sh
pkill -x PageSnap 2>/dev/null || true
pkill -f "PageSnap.app/Contents/Resources/kindleweb" 2>/dev/null || true
sleep 1
rm -rf /Applications/PageSnap.app
cp -R build/PageSnap.app /Applications/PageSnap.app
open /Applications/PageSnap.app
echo "インストール完了: /Applications/PageSnap.app（Spotlight で「PageSnap」と打てば起動できます）"
