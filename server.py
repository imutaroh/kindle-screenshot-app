#!/usr/bin/env python3
"""
Kindle 自動スクショアプリ - Web UI版エントリーポイント
"""

from src.server import app

if __name__ == '__main__':
    print("=" * 50)
    print("📚 Kindle 自動スクショアプリ - Web UI版")
    print("=" * 50)
    print("\n🌐 ブラウザで以下のURLにアクセスしてください:")
    print("   http://localhost:5000")
    print("\n⚠️  Kindleをフルスクリーンで表示してから実行してください。")
    print("=" * 50)
    
    app.run(debug=True, host='127.0.0.1', port=5000)

