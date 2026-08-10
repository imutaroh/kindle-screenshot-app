#!/usr/bin/env python3
"""
Kindle 自動スクショアプリ - Web UI版エントリーポイント
"""

import os
from src.server import app

if __name__ == '__main__':
    # macOS の AirPlay Receiver が 5000 を使うことがあるため、デフォルトは 5001
    port = int(os.environ.get('PORT', '5001'))

    print("=" * 50)
    print("📚 Kindle 自動スクショアプリ - Web UI版")
    print("=" * 50)
    print(f"\n🌐 ブラウザで以下のURLにアクセスしてください:")
    print(f"   http://localhost:{port}")
    print("\n⚠️  Kindleをフルスクリーンで表示してから実行してください。")
    print("=" * 50)

    # debug=False: バックグラウンドのキャプチャスレッドをリローダーで二重起動させないため
    app.run(debug=False, host='127.0.0.1', port=port, use_reloader=False)

