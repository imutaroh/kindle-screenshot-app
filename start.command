#!/bin/bash
# Kindle 自動スクショアプリ - ダブルクリック起動スクリプト
# Finder からこのファイルをダブルクリックすると、
# 仮想環境のセットアップ → サーバ起動 → ブラウザ自動オープン まで一発で実行されます。

set -e

# このスクリプトのあるディレクトリへ移動
cd "$(dirname "$0")"

PORT=5001
URL="http://localhost:${PORT}"

echo "============================================"
echo "📚 Kindle 自動スクショアプリ"
echo "============================================"
echo ""

# Python3 の存在チェック
if ! command -v python3 >/dev/null 2>&1; then
    echo "❌ python3 が見つかりません。"
    echo "   https://www.python.org/ から Python 3 をインストールしてください。"
    echo ""
    read -p "Enterキーで終了..."
    exit 1
fi

# venv 作成
if [ ! -d "venv" ]; then
    echo "🔧 仮想環境を作成中..."
    python3 -m venv venv
fi

# venv 有効化
source venv/bin/activate

# 依存パッケージのインストール（差分のみ）
echo "📦 依存パッケージを確認中..."
pip install --quiet --disable-pip-version-check -r requirements.txt

# 既存のサーバが動いていないかチェック → なければブラウザを開くタイマーを仕込む
if curl -s -o /dev/null -w "%{http_code}" "$URL" 2>/dev/null | grep -q "200"; then
    echo "ℹ️  サーバはすでに起動しています。ブラウザで開きます。"
    open "$URL"
else
    echo ""
    echo "🚀 サーバを起動します..."
    echo "   URL: $URL"
    echo ""
    echo "💡 ターミナルウィンドウは閉じないでください。"
    echo "   終了するには Ctrl + C を押してください。"
    echo ""

    # サーバ起動から少し遅らせてブラウザを開く
    (sleep 2 && open "$URL") &

    # フォアグラウンドでサーバ実行（ログがターミナルに表示される）
    PORT=$PORT python server.py
fi
