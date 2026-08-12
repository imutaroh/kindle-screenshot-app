# kindle-screenshot-app

Kindle for Mac の全ページを自動スクリーンショットする CLI（Go 学習プロジェクト）。
もとは Python 版として実装され、その後 Go に移植された。

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
ページめくり方向・PNG自動削除を画面から設定して実行できる。

フラグ:

- `-port`: 待受ポート（デフォルト `5001`。環境変数 `PORT` があればそれを既定値として使う。`-port` の明示指定が最優先）
- `-out`: 本の出力先ルートフォルダ（デフォルト `output`）

```bash
./kindleweb -port 8080 -out ~/Documents/kindle-books
```

実行中はホットコーナーで手動制御できる（画面**左上角**にマウス移動＝取得済み分でPDF化して終了、
**右上角**＝キャンセル）。マルチディスプレイ環境では `NSScreen.mainScreen` 基準の座標になるため、
Kindle を副ディスプレイに出している場合は角判定がずれることがある。

### macOSアプリとして使う（PageSnap）

ターミナルを開かず、Spotlight や Dock からブラウザ不要で起動できる macOS ネイティブアプリ版。
中身は AppKit + WKWebView の薄い殻が `kindleweb` をサブプロセスとして起動し、そのままウィンドウ内に表示する。

```bash
./scripts/install.sh
```

`build.sh`（Go バイナリのビルド → Swift 殻のビルド → `.app` に組み立て → ad-hoc署名）を実行したうえで
`/Applications/PageSnap.app` に配置し、既存プロセスを終了させてから起動し直す。以降は Spotlight で
「PageSnap」と検索するか、Dock から起動できる。

初回起動時、「システム設定 > プライバシーとセキュリティ」で **PageSnap** に以下の権限を追加する:

- **画面収録**（screencapture 用）
- **アクセシビリティ**（ページ送りのキー送信用）

データ（キャプチャした本の PNG/PDF）は `~/Documents/PageSnap` に保存される（Web UI 版の `-out output` に相当）。

ウィンドウを閉じる・Cmd+Q のどちらで終了しても、キャプチャ実行中なら「中断されますがよろしいですか」の
確認ダイアログが出る。承諾すると、サーバーの子プロセスに `SIGTERM` を送ってから終了する。

アプリアイコンを作り直す場合は `swift scripts/make_icon.swift` を実行すると
`assets/AppIcon.icns`（および `assets/icon_1024.png`・`assets/AppIcon.iconset/`）を再生成する。

## 必要な権限（初回のみ）

CLI（`kindlesnap`）・Web UI（`kindleweb`をターミナルから直接実行する場合）は、実行するターミナルに
「システム設定 > プライバシーとセキュリティ」で以下を許可する:

- **画面収録**（screencapture 用）
- **アクセシビリティ**（ページ送りのキー送信用）

`kindlesnap`（CLI）・`kindleweb`（Web UI）・PageSnap（macOSアプリ）のいずれも同じ権限が必要
（macOSアプリ版は上記「macOSアプリとして使う」の通り、権限は PageSnap 自体に付与する）。

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
macos/              # macOSアプリ殻（PageSnap）の Swift Package。AppKit + WKWebView
scripts/            # build.sh・install.sh・make_icon.swift（PageSnap.app のビルド/配布/アイコン生成）
assets/             # PageSnap のアプリアイコン（AppIcon.icns 等。make_icon.swift の生成物）
```

> このリポジトリは、もともとの Python 版アプリのリポジトリに Go 実装を統合したもの。
> Python 版のコードは削除されたが、git 履歴には残っている。
