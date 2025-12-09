# Kindle 自動スクショアプリ

Kindle for Mac の全ページを自動でスクリーンショット取得し、PDFに結合するツール

## セットアップ

### 1. 仮想環境の作成と有効化（推奨）

まず、プロジェクトディレクトリに移動：

```bash
cd "/Users/imutaakihiro/Downloads/開発/Kindle 自動スクショアプリ"
```

仮想環境を作成：

```bash
python3 -m venv venv
```

仮想環境を有効化：

```bash
source venv/bin/activate
```

仮想環境が有効になると、ターミナルのプロンプトに `(venv)` が表示されます。

### 2. 依存パッケージのインストール

仮想環境が有効な状態で、以下を実行：

```bash
pip install -r requirements.txt
```

**注意**: 仮想環境を使わない場合（非推奨）：

```bash
pip3 install --user -r requirements.txt
```

### 3. macOSの権限設定

このアプリを使用するには、以下の権限が必要です：

1. **システム環境設定 > セキュリティとプライバシー > プライバシー**
   - **画面収録**: ターミナル（またはPython）にチェック
   - **アクセシビリティ**: ターミナル（またはPython）にチェック

## 使い方（ターミナル版）

### 基本的な使い方

1. **仮想環境を有効化**（まだ有効化していない場合）：
   ```bash
   source venv/bin/activate
   ```

2. Kindle for Mac を起動
3. 対象の本を開く
4. フルスクリーン表示にする
5. 1ページ目に戻す
6. ターミナルで以下を実行：

```bash
python app.py
```

**注意**: 仮想環境が有効な場合は `python`、無効な場合は `python3` を使用

6. 本の名前を入力
7. 設定を確認（必要に応じて変更）
8. 5秒のカウントダウン後に自動実行開始

### 実行中の操作

- `Ctrl + C` で中断可能

## トラブルシューティング

### インストールエラーが出る場合

**SSL証明書エラー**:
```bash
pip3 install --trusted-host pypi.org --trusted-host pypi.python.org --trusted-host files.pythonhosted.org -r requirements.txt
```

**権限エラー**:
```bash
pip3 install --user -r requirements.txt
```

### スクリーンショットが取得できない場合

- システム環境設定で「画面収録」と「アクセシビリティ」の権限を確認
- Kindleがフルスクリーン表示になっているか確認
- 他のアプリが前面にないか確認

### ページ送りがうまくいかない場合

- `config.py` の `PAGE_WAIT_TIME` を増やす（例: 3.0秒）
- Kindleのアニメーション設定を確認

## 使い方（Web UI版）

1. **仮想環境を有効化**（まだ有効化していない場合）：
   ```bash
   source venv/bin/activate
   ```

2. サーバーを起動：
   ```bash
   python server.py
   ```

3. ブラウザで `http://localhost:5000` にアクセス

4. Web UIから設定を入力して実行開始

## ファイル構成

```
kindle-screenshot-app/
├── venv/               # 仮想環境（.gitignoreで除外）
├── src/                # ソースコード
│   ├── __init__.py
│   ├── app.py          # ターミナル版メインスクリプト
│   ├── server.py       # Flaskサーバー（Phase 2）
│   ├── screenshot.py   # スクショ・ページ送り処理
│   ├── pdf_generator.py # PDF結合処理
│   └── config.py       # 設定値管理
├── app.py              # ターミナル版エントリーポイント
├── server.py           # Web UI版エントリーポイント
├── scripts/            # 実行可能なスクリプト
├── requirements.txt    # 依存パッケージ
├── output/             # 出力先（本ごとのフォルダ）
├── templates/          # HTMLテンプレート
│   └── index.html
├── static/             # 静的ファイル
│   ├── style.css
│   └── script.js
└── docs/
    ├── 要件定義書.md
    ├── Output.md
    └── ProjectStructure.md
```

## 設定

`config.py` で以下の設定を変更できます：

- `PAGE_WAIT_TIME`: ページ送り後の待機秒数（デフォルト: 2.0秒）
- `START_DELAY`: 実行開始前の待機秒数（デフォルト: 5秒）
- `PDF_PAGES_PER_FILE`: 1つのPDFに含める画像枚数（デフォルト: 100枚）
- `MAX_PAGES`: 最大ページ数（0 = 無制限）

