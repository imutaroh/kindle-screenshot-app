#!/bin/bash
# PageSnap - ダブルクリック起動スクリプト
# Finder からこのファイルをダブルクリックすると、
# ビルド → サーバ起動 → ブラウザ自動オープン まで一発で実行されます。

set -e

# このスクリプトのあるディレクトリへ移動
cd "$(dirname "$0")"

PORT=5001
URL="http://localhost:${PORT}"

echo "============================================"
echo "📚 PageSnap"
echo "============================================"
echo ""

# go コマンドの存在チェック
if ! command -v go >/dev/null 2>&1; then
    echo "❌ Go がインストールされていません。"
    echo "   https://go.dev/dl/ から Go をインストールしてください。"
    echo ""
    read -p "Enterキーで終了..."
    exit 1
fi

# 既存のサーバが動いていないかチェック → 動いていればブラウザを開くだけ
if curl -s -o /dev/null -w "%{http_code}" "$URL" 2>/dev/null | grep -q "200"; then
    echo "ℹ️  サーバはすでに起動しています。ブラウザで開きます。"
    open "$URL"
    exit 0
fi

echo "🔧 ビルド中..."
go build -o .bin/kindleweb ./cmd/kindleweb

echo ""
echo "🚀 サーバを起動します..."
echo "   URL: $URL"
echo ""
echo "💡 ターミナルウィンドウは閉じないでください。"
echo "   終了するには Ctrl + C を押してください。"
echo ""
echo "🔐 初回実行時は「システム設定 > プライバシーとセキュリティ」で、"
echo "   このターミナルアプリ（Terminal.app / iTerm / Ghostty など）に"
echo "   「画面収録」と「アクセシビリティ」の許可が必要です。"
echo ""

# サーバ起動から少し遅らせてブラウザを開く
(sleep 2 && open "$URL") &

# フォアグラウンドでサーバ実行（ログがターミナルに表示される）
.bin/kindleweb -port "$PORT" -out output
