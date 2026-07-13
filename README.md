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
- [ ] **② PDF結合**: `pdfcpu` or `gopdf` で PNG → PDF
- [ ] **③ Web UI**: `net/http` + `embed` で Python 版の Web UI を移植

## 使い方

```bash
go build -o kindlesnap ./cmd/kindlesnap
./kindlesnap -book "本の名前"            # 日本語の本（左めくり）
./kindlesnap -book "Some Book" -dir right  # 英語の本（右めくり）
```

実行すると Kindle を自動で前面に出し、5秒カウントダウン後にキャプチャ開始。
最終ページで同じ画面が2回続くと自動停止する。途中で止めるには `Ctrl+C`。

主なフラグは `./kindlesnap -h` を参照。

## 必要な権限（初回のみ）

「システム設定 > プライバシーとセキュリティ」で、実行するターミナルに以下を許可:

- **画面収録**（screencapture 用）
- **アクセシビリティ**（ページ送りのキー送信用）

## テスト

```bash
go test ./...                                        # ユニットテスト
KINDLESNAP_SMOKE=1 go test ./internal/capture/ -v    # 実際に画面をキャプチャするスモークテスト
```

## 構成

```
cmd/kindlesnap/     # CLI エントリーポイント（フラグ・メインループ・シグナル処理）
internal/capture/   # screencapture / osascript のラッパー
internal/dedupe/    # pHash による同一ページ判定（自動停止用）
internal/output/    # 出力フォルダの連番作成（上書き防止）
```
