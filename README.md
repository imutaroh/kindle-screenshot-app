# Kindle 自動スクショアプリ

Kindle for Mac の全ページを自動でスクリーンショット取得し、PDFに結合するツール。Web UI 付き。

## クイックスタート（推奨：ダブルクリック起動）

1. **Finder** で `start.command` を**ダブルクリック**
   - 初回は仮想環境の作成と依存パッケージのインストールが走るので少し待つ
   - 完了するとブラウザが自動で開く（http://localhost:5001）
2. **macOSの権限を許可**（初回のみ。次の2つは「システム設定 > プライバシーとセキュリティ」）
   - **画面収録**: ターミナル（または Python）にチェック
   - **アクセシビリティ**: ターミナル（または Python）にチェック
3. **Kindle for Mac で本を開いておく**（フルスクリーン推奨）
4. ブラウザのフォームに**本の名前**を入力 → **🚀 実行開始**
   - Kindle は自動で前面に表示される（5秒のカウントダウン後に開始）
5. 自動でスクショ取得 → PDF結合まで実行。完了後は **📂 保存先を開く** ボタンで Finder が開く

> 同じ本の名前で再実行した場合、既存フォルダには上書きせず `本の名前_2` のように連番フォルダが作られる。

> 終了するときは、起動したターミナルウィンドウで `Ctrl + C`、もしくはウィンドウを閉じる。

### `start.command` がダブルクリックで開けないとき

Finder で `start.command` を**右クリック → 開く**（macOS のセキュリティ警告を許可）。一度開けば次回からはダブルクリックで起動できる。

## 使い方（ターミナル版・上級者向け）

シンプルに対話形式で動かしたい場合：

```bash
source venv/bin/activate
python app.py
```

## 設定

`src/config.py` で以下を変更可能：

- `PAGE_WAIT_TIME`: ページ送り後の待機秒数（デフォルト: 0.5秒）
- `INITIAL_PAGE_WAIT_TIME`: 最初の数ページの待機秒数（初期読み込み用、デフォルト: 2.0秒）
- `START_DELAY`: 実行開始前の待機秒数（デフォルト: 5秒）
- `PDF_PAGES_PER_FILE`: 1つのPDFに含める画像枚数（デフォルト: 100枚）
- `AUTO_STOP_ON_DUPLICATE`: 同じページが連続したら自動停止（デフォルト: True）

## トラブルシューティング

### スクリーンショットが取得できない / ページが送られない

- 「画面収録」「アクセシビリティ」の権限を再確認（ターミナル / Python の両方）
- Kindle がフルスクリーンで**前面**にあるか確認
- アニメーションが遅い場合は `src/config.py` の `PAGE_WAIT_TIME` を上げる

### ブラウザが自動で開かない

手動で http://localhost:5001 にアクセス。

### ポート 5001 が使われている

別ポートで起動：

```bash
PORT=5002 python server.py
```

## ファイル構成

```
kindle-screenshot-app/
├── start.command           # ダブルクリック起動スクリプト（Mac）
├── server.py               # Web UI 版エントリーポイント
├── app.py                  # ターミナル版エントリーポイント
├── requirements.txt
├── src/
│   ├── server.py           # Flask サーバ
│   ├── app.py              # ターミナル版本体
│   ├── screenshot.py       # スクショ・ページ送り
│   ├── pdf_generator.py    # PDF結合
│   ├── utils.py            # 共通処理（出力フォルダの連番作成など）
│   └── config.py           # 設定値
├── templates/index.html    # Web UI
├── static/
│   ├── script.js           # フロントエンドロジック
│   └── style.css
├── output/                 # 出力先（本ごとのフォルダ）
└── docs/
```
