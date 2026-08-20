# kindle-screenshot-app

Kindle for Mac の全ページを自動スクリーンショットする Web アプリ（Go 学習プロジェクト）。
もとは Python 版として実装され、その後 Go に移植された。

## 設計方針

- **pure Go**（CGo なし）。macOS 操作は標準コマンドに逃がす
  - スクショ: `screencapture -x -m`（メインディスプレイのみ・無音）
  - ページ送り・Kindle前面化: `osascript`（AppleScript）
- `go build` 一発で単一バイナリ。venv も依存セットアップも不要

## フェーズ計画

- [x] **① CLI**: スクショ → ページ送り → pHash 自動停止（後に Web UI へ一本化し CLI は廃止）
- [x] **② PDF結合**: 自前の最小PDFライター（`internal/pdf`）で PNG → PDF
- [x] **③ Web UI**: `net/http` + `embed` で Python 版の Web UI を移植

## 使い方

Python 版と同じ Web UI 体験を、依存ゼロの単一バイナリ（`kindleweb`）で提供する。

**起動方法1: `start.command` をダブルクリック**（推奨）

Finder でこのリポジトリを開き、`start.command` をダブルクリックするとビルド → サーバ起動 →
ブラウザ自動オープンまで一発で行われる。既にサーバが起動していればブラウザを開くだけ。

**起動方法2: ターミナルから直接**

```bash
go run ./cmd/kindleweb
# または
go build -o kindleweb ./cmd/kindleweb
./kindleweb
```

起動したら `http://localhost:5001` をブラウザで開く。本の名前・最大ページ数・PDF結合枚数・
ページめくり方向・PNG自動削除を画面から設定して実行できる。

フラグ:

- `-port`: 待受ポート（デフォルト `5001`。環境変数 `PORT` があればそれを既定値として使う。`-port` の明示指定が最優先）
- `-out`: 本の出力先ルートフォルダ（デフォルト `output`）

```bash
./kindleweb -port 8080 -out ~/Documents/kindle-books
```

## 必要な権限（初回のみ）

`kindleweb`（`start.command` 経由でもターミナルから直接でも）は、
起動に使うターミナルアプリ（Terminal.app / iTerm / Ghostty など）に
「システム設定 > プライバシーとセキュリティ」で以下を許可する:

- **画面収録**（screencapture 用）
- **アクセシビリティ**（ページ送りのキー送信用）

Apple 署名済みのターミナルアプリ自体に権限を紐付けるため、アプリを再ビルドしても許可が外れることはない。

## テスト

```bash
go test ./...                                        # ユニットテスト
KINDLESNAP_SMOKE=1 go test ./internal/capture/ -v    # 実際に画面をキャプチャするスモークテスト
```

## 構成

```
cmd/kindleweb/      # Web UI サーバーのエントリーポイント（-port / -out フラグ）
internal/capture/   # screencapture / osascript のラッパー（マウス座標・画面サイズ取得も含む）
internal/dedupe/    # pHash による同一ページ判定（自動停止用）
internal/output/    # 出力フォルダの連番作成（上書き防止）
internal/pdf/       # 連番PNG → PDF結合（自前の最小PDFライター）
internal/session/   # Web UI 用キャプチャワーカー（状態管理・進捗）
internal/server/    # Web UI の HTTP ルーティング（net/http のみ、フレームワーク不使用）
web/                # Web UI の静的資産（HTML/CSS/JS）を go:embed でバイナリに同梱
start.command       # ダブルクリック起動スクリプト（ビルド → サーバ起動 → ブラウザオープン）
```

> このリポジトリは、もともとの Python 版アプリのリポジトリに Go 実装を統合したもの。
> Python 版のコードは削除されたが、git 履歴には残っている。
