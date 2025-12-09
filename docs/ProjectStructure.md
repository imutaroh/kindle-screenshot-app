# プロジェクト構造の整理

## 現在の構造（問題点）

現在、`.py`ファイルがルートディレクトリに散らばっています：

```
Kindle 自動スクショアプリ/
├── app.py              # ルートに散らばっている
├── server.py           # ルートに散らばっている
├── screenshot.py       # ルートに散らばっている
├── pdf_generator.py    # ルートに散らばっている
├── config.py           # ルートに散らばっている
├── requirements.txt
├── README.md
├── output/
├── templates/
├── static/
└── docs/
```

## 推奨される整理後の構造

### オプション1: `src/` ディレクトリにまとめる（推奨）

```
Kindle 自動スクショアプリ/
├── src/                    # ソースコードをまとめる
│   ├── __init__.py
│   ├── app.py              # ターミナル版メインスクリプト
│   ├── server.py           # Flaskサーバー
│   ├── screenshot.py       # スクショ・ページ送り処理
│   ├── pdf_generator.py    # PDF結合処理
│   └── config.py           # 設定値管理
├── scripts/                # 実行可能なスクリプト
│   └── create_pdf.py       # PDF作成のみのスタンドアロン
├── templates/              # HTMLテンプレート
│   └── index.html
├── static/                 # 静的ファイル
│   ├── style.css
│   └── script.js
├── output/                 # 出力先
├── docs/                   # ドキュメント
│   ├── 要件定義書.md
│   └── Output.md
├── venv/                   # 仮想環境（.gitignoreで除外）
├── requirements.txt        # 依存パッケージ
├── README.md
└── .gitignore
```

**メリット**:
- ✅ ソースコードが1箇所にまとまる
- ✅ ルートディレクトリがすっきり
- ✅ 標準的なPythonプロジェクト構造

**実行方法の変更**:
```bash
# 変更前
python app.py

# 変更後
python src/app.py
# または
cd src && python app.py
```

### オプション2: パッケージ構造にする

```
Kindle 自動スクショアプリ/
├── kindle_screenshot/      # パッケージ名
│   ├── __init__.py
│   ├── app.py
│   ├── server.py
│   ├── screenshot.py
│   ├── pdf_generator.py
│   └── config.py
├── scripts/                # 実行可能なスクリプト
│   └── create_pdf.py
├── templates/
├── static/
├── output/
├── docs/
├── venv/
├── requirements.txt
├── README.md
└── .gitignore
```

**メリット**:
- ✅ よりPythonらしい構造
- ✅ パッケージとしてインストール可能
- ✅ `from kindle_screenshot import ...` でインポート可能

**実行方法**:
```bash
python -m kindle_screenshot.app
```

### オプション3: 最小限の整理（シンプル）

```
Kindle 自動スクショアプリ/
├── lib/                    # ライブラリ（共通モジュール）
│   ├── __init__.py
│   ├── screenshot.py
│   ├── pdf_generator.py
│   └── config.py
├── app.py                  # ターミナル版（ルートに残す）
├── server.py               # Web UI版（ルートに残す）
├── scripts/
│   └── create_pdf.py
├── templates/
├── static/
├── output/
├── docs/
├── venv/
├── requirements.txt
├── README.md
└── .gitignore
```

**メリット**:
- ✅ 最小限の変更
- ✅ エントリーポイント（app.py, server.py）はルートに残る
- ✅ 共通モジュールだけを整理

**実行方法**:
```bash
# 変更なし
python app.py
python server.py
```

## 推奨：オプション1（src/ディレクトリ）

最も標準的で、将来の拡張にも対応しやすい構造です。

### 移行手順

1. `src/` ディレクトリを作成
2. `.py`ファイルを `src/` に移動
3. `src/__init__.py` を作成（空ファイルでOK）
4. インポート文を修正（必要に応じて）
5. 実行方法を更新

### インポートの変更

```python
# 変更前（app.py内）
from screenshot import capture_all_pages
from pdf_generator import create_pdfs_from_directory
import config

# 変更後（src/app.py内）
from src.screenshot import capture_all_pages
from src.pdf_generator import create_pdfs_from_directory
from src import config
# または、同じディレクトリ内なので変更不要
```

## 現在の構造を維持する場合

現在の構造でも問題なく動作しますが、以下の点に注意：

- ✅ 小規模なプロジェクトなら問題なし
- ⚠️ ファイルが増えるとルートが散らかる
- ⚠️ 他のプロジェクトと混同しやすい

## まとめ

**推奨**: オプション1（`src/`ディレクトリ）で整理する

理由：
- 標準的なPythonプロジェクト構造
- ルートディレクトリがすっきり
- 将来の拡張に対応しやすい
- 他の開発者にも分かりやすい

