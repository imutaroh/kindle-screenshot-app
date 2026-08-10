# kindle-screenshot-go

Kindle for Mac の全ページを自動スクリーンショットする CLI（Go 学習プロジェクト）。
Python 版 [kindle-screenshot-app](../kindle-screenshot-app) の Go 移植。

## 設計方針

- **pure Go**（CGo なし）。macOS 操作は標準コマンドに逃がす
  - スクショ: `screencapture -x -m`（メインディスプレイのみ・無音）
  - ページ送り・Kindle前面化: `osascript`（AppleScript）
- `go build` 一発で単一バイナリ。venv も依存セットアップも不要

## フェーズ計画

- [x] **① CLI**: スクショ → ページ送り → pHash 自動停止（このリポジトリの現状）
- [x] **② PDF結合**: 自前の最小PDFライター（`internal/pdf`）で PNG → PDF
- [x] **③ Web UI**: `net/http` + `embed` で Python 版の Web UI を移植

## 使い方

### CLI（kindlesnap）

```bash
go build -o kindlesnap ./cmd/kindlesnap
./kindlesnap -book "本の名前"            # 日本語の本（左めくり）
./kindlesnap -book "Some Book" -dir right  # 英語の本（右めくり）
```

実行すると Kindle を自動で前面に出し、5秒カウントダウン後にキャプチャ開始。
最終ページで同じ画面が2回続くと自動停止する。途中で止めるには `Ctrl+C`。

主なフラグは `./kindlesnap -h` を参照。

### Web UI（kindleweb）

Python 版と同じ Web UI 体験を、依存ゼロの単一バイナリで提供する。

```bash
go run ./cmd/kindleweb
# または
go build -o kindleweb ./cmd/kindleweb
./kindleweb
```

起動したら `http://localhost:5001` をブラウザで開く。本の名前・最大ページ数・PDF結合枚数・
ページめくり方向・PNG自動削除を画面から設定して実行でき、`/reader` では生成したPDFをその場で読める。

フラグ:

- `-port`: 待受ポート（デフォルト `5001`。環境変数 `PORT` があればそれを既定値として使う。`-port` の明示指定が最優先）
- `-out`: 本の出力先ルートフォルダ（デフォルト `output`。`/api/books` や `/reader` の一覧・配信もこのフォルダ配下を対象にする）

```bash
./kindleweb -port 8080 -out ~/Documents/kindle-books
```

実行中はホットコーナーで手動制御できる（画面**左上角**にマウス移動＝取得済み分でPDF化して終了、
**右上角**＝キャンセル）。マルチディスプレイ環境では `NSScreen.mainScreen` 基準の座標になるため、
Kindle を副ディスプレイに出している場合は角判定がずれることがある。

## 必要な権限（初回のみ）

「システム設定 > プライバシーとセキュリティ」で、実行するターミナルに以下を許可:

- **画面収録**（screencapture 用）
- **アクセシビリティ**（ページ送りのキー送信用）

`kindlesnap`（CLI）・`kindleweb`（Web UI）のどちらも同じ権限が必要。

## テスト

```bash
go test ./...                                        # ユニットテスト
KINDLESNAP_SMOKE=1 go test ./internal/capture/ -v    # 実際に画面をキャプチャするスモークテスト
```

## 構成

```
cmd/kindlesnap/     # CLI エントリーポイント（フラグ・メインループ・シグナル処理）
cmd/kindleweb/      # Web UI サーバーのエントリーポイント（-port / -out フラグ）
internal/capture/   # screencapture / osascript のラッパー（マウス座標・画面サイズ取得も含む）
internal/dedupe/    # pHash による同一ページ判定（自動停止用）
internal/output/    # 出力フォルダの連番作成（上書き防止）
internal/pdf/       # 連番PNG → PDF結合（自前の最小PDFライター）
internal/session/   # Web UI 用キャプチャワーカー（状態管理・ホットコーナー・進捗）
internal/server/    # Web UI の HTTP ルーティング（net/http のみ、フレームワーク不使用）
web/                # Web UI の静的資産（HTML/CSS/JS）を go:embed でバイナリに同梱
```
