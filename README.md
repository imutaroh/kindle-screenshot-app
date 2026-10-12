# kindle-screenshot-app

Kindle for Mac で開いている本を1ページずつ撮影し、**PDF** と **Markdown**（縦書き日本語の OCR）にする
コマンドラインツール `kindlesnap`（Go 学習プロジェクト）。

主な使い手は Claude Code。リポジトリのプロジェクトスキル `kindle-to-md`
（`.claude/skills/kindle-to-md/SKILL.md`）に手順があり、Claude Code に「Kindle の本を PDF と Markdown にして」と
頼むと、事前チェック → 本を選んで開始ページを開く（画面を見てクリック）→ 撮影 → 結果の報告 までを進める。人が直接コマンドを打ってもよい。

> 出力は本人の私的利用に限る。PDF・Markdown を共有・公開しない。

## 仕組み

- **pure Go**（CGo なし・外部 Go モジュールなし）。macOS の操作は標準コマンドに任せる
  - 撮影: `screencapture -l <ウィンドウID>`（Kindle のウィンドウだけを撮る。背面や別ディスプレイでも撮れる）
  - ページ送り: `osascript`（JXA）から `CGEventPostToPid` で Kindle のプロセスへキーを直接送る（前面化しない）
  - ページが変わったかは画素の差分で判定し、描画が落ち着いてから撮る
- PDF は自前の最小 PDF ライターで作る（無劣化。余白とタイトルバーを自動トリミング）
- OCR は Apple Vision の `RecognizeDocumentsRequest`（macOS 26）。Swift の小さなヘルパーをバイナリに埋め込み、
  初回に `swiftc` でビルドして `~/Library/Caches/kindlesnap/` にキャッシュする。
  方式を選んだ理由は [docs/md-conversion-decision.md](docs/md-conversion-decision.md)

## 必要なもの

- **macOS 26 以降**（OCR が使う Vision の API）
- **Kindle for Mac**
- **Go**（ビルド用）
- **Xcode Command Line Tools**（`xcode-select --install`。OCR ヘルパーのビルドに `swiftc` を使う。Markdown が要らなければ不要）
- 権限（初回のみ）: 「システム設定 > プライバシーとセキュリティ」で、**kindlesnap を起動するターミナルアプリ**
  （Claude Code を動かしているターミナル。Terminal.app / iTerm / Ghostty など）に次を許可し、ターミナルを再起動する
  - **画面収録**（screencapture 用）
  - **アクセシビリティ**（ページ送りのキーとクリック用）

権限はターミナルアプリ自体に付くので、kindlesnap を作り直しても外れない。

## ビルド

```bash
go build -o kindlesnap ./cmd/kindlesnap
```

## サブコマンド

出力はすべて JSON。長く動く `capture` と `md` は1行1イベントの JSON Lines を流し、最後の行が `{"type":"result",...}`。
終了コード: 0 成功 / 1 失敗 / 2 引数の誤り / 3 事前チェックの失敗 / 4 中断。

| コマンド | 内容 |
| --- | --- |
| `kindlesnap doctor [--launch]` | 事前チェック（macOS・Kindle の起動とウィンドウ・画面収録・アクセシビリティ・OCR）。`--launch` で Kindle をバックグラウンド起動して最大30秒待つ。失敗した項目には `fix`（対処）が付く |
| `kindlesnap capture --book NAME [...]` | 撮影 → PDF → Markdown → PNG 削除。今表示しているページから撮り始める |
| `kindlesnap status` | 実行中の capture の状態（`<bookDir>/status.json` の中身） |
| `kindlesnap finish` | 実行中の capture を終了し、そこまでのページで PDF と Markdown を作る（本の最後で一時停止したとき） |
| `kindlesnap stop` | 中断。撮れたページの PDF は作るが、Markdown は作らず PNG を残す |
| `kindlesnap md --dir BOOKDIR [--title T]` | フォルダの `page_*.png` から Markdown を作り直す |
| `kindlesnap peek [--out FILE]` | Kindle のウィンドウを PNG に撮る（Claude が画面を見るため） |
| `kindlesnap click --x X --y Y [--double]` | Kindle のウィンドウ内（左上からのポイント座標）をクリックする。本物のマウスが動く。他のウィンドウが Kindle に重なっているとそちらをクリックするので、Kindle を前面に出してから使う（peek の画像には重なりが写らない） |
| `kindlesnap park` | マウスカーソルを Kindle のウィンドウの外（画面上）へ動かす。click で本を開いた後、capture の前に使う（カーソルが乗っているとページ送りの矢印が写り込む）。本物のマウスが動く |

`capture` のオプション:

- `--book NAME`（必須）: 出力フォルダ名とファイル名
- `--out DIR`: 出力先のルート。既定は `$KINDLESNAP_OUT`、無ければ `~/Documents/KindleBooks`
- `--direction left|right`: ページをめくる方向。縦書き（右綴じ）は `left`（既定）、横書きは `right`
- `--max-pages N`: 最大ページ数（0 = 無制限、既定）
- `--pdf-pages N`: PDF 1ファイルあたりのページ数（既定 50）
- `--no-md`: Markdown を作らない（漫画・コミックはこちら）
- `--keep-png`: 完了後も PNG を残す
- `--no-crop`: PDF の余白トリミングをしない

capture は一度に1つだけ動かせる（`$TMPDIR/kindlesnap.lock`）。`finish` は SIGUSR1、`stop` は SIGINT を
実行中のプロセスに送る。

## 出力

```
~/Documents/KindleBooks/<書名>/   # 同名のフォルダが既にあれば <書名>_2, _3 ...
├── <書名>_part1.pdf               # --pdf-pages ページごとに分割
├── <書名>_part2.pdf
├── <書名>.md                      # Markdown（--no-md なら無し）
├── run.jsonl                      # capture / md のイベントの記録（JSON Lines）
├── status.json                    # 最後の状態と結果
└── page_0001.png ...              # 完了したら削除（--keep-png・中断・Markdown 失敗のときは残る）
```

Markdown は `# 書名` の後に、ページごとに `<!-- p.N -->` の印を付けて本文を並べる。
柱（ページ上端の書名など）・ルビ・ウィンドウのタイトルは除き、ページをまたいで続く段落はつなげる。
図や横書きの囲みは本文の後ろに `> ` の引用としてまとめる。OCR が怪しいページは result の `review_pages` に出る。

## 取り込みの運用チェックリスト

取り込みが途中で止まる・画質が落ちる原因の多くは Kindle 側の状態にある。

**開始前**

- [ ] `kindlesnap doctor --launch` が `"ok":true` になる
- [ ] Kindle を**フルスクリーンにせず、通常のウィンドウ**で表示する（最小化もしない）
- [ ] 本を開き、**取り込みたい最初のページ**を表示する（撮影は今のページから始まる）
- [ ] **ページ本文を1回クリック**する（Kindle は起動直後にバックグラウンドのキーを受け付けないことがある）
- [ ] クリックしたら**マウスを Kindle のウィンドウの外へ出す**（ページ送りの矢印が写り込み、余白トリミングと OCR を汚す）
- [ ] Page Flip 表示やツールバー・スライダーが出ていない状態にする
- [ ] 画質を上げたいなら、Kindle を **Retina ディスプレイ**（MacBook 内蔵画面など）に置き、文字サイズを上げる
  - 画素数はディスプレイで決まり、コードでは増やせない。1x のディスプレイで撮るとログに「画質注意」が出る

**取り込み中**

- [ ] **マウスを Kindle のウィンドウに乗せない**（キーを受け付けなくなることがある）
- [ ] **Kindle のウィンドウを動かしたり、大きさを変えたりしない**
- [ ] Kindle にキーボードショートカットを送らない（⌘0 などで読書位置が変わる）。他のアプリでの作業はしてよい
- [ ] 一時停止（`"status":"paused"`）になったら:
  - 本の最後（最終ページ・「レビュー」「おすすめ」画面）なら `kindlesnap finish`
  - 途中なら **Kindle のページ本文を1回クリック**して、マウスを外へ出す。キー受付が戻ると自動で再開する
  - 一時停止のまま15分たつと、そこまでのページで撮影を終える。このときも完了と同じ扱い（`ok:true`・`status:"done"`・
    終了コード 0。Markdown を作り、PNG も消す）になるが、最後まで撮れたとは限らない。最後の状態のメッセージが
    「最終ページか確認できないまま終了しました（N ページ）」になっていれば、N ページ目で途中終了している

中断・エラーで止まった場合も、撮れたページが1枚以上あればそこまでの PDF を作る。
Markdown に失敗した・中断した場合は PNG が残るので、`kindlesnap md --dir <bookDir>` で作り直せる。

## テスト

```bash
env -u KINDLESNAP_SMOKE -u KINDLESNAP_SMOKE_KEYS go test ./...   # ユニットテスト（Kindle に触れない）
KINDLESNAP_SMOKE=1 go test ./internal/capture/ -v                # 実際に Kindle のウィンドウを撮るスモークテスト
KINDLESNAP_SMOKE_KEYS=1 go test ./internal/capture/ -run TurnPage -v  # 実際にページを送るスモークテスト（読書位置が動く）
```

`internal/ocr` と `internal/markdown` のテストの一部は、本物の Vision ヘルパーをビルドして動かす。
ベンチマーク画像（`.claude/tmp/bench/`、リポジトリには含めない）が無い、または OCR が使えない環境ではスキップする。

## 構成

```
cmd/kindlesnap/      # CLI（サブコマンド・JSON Lines の出力・ロック・シグナル）
internal/capture/    # screencapture / osascript のラッパー（ウィンドウ発見・ページ送り・クリック・権限確認・通知）
internal/frame/      # 画素差分による画面の変化・安定の判定（ページ送りが効いたか）
internal/crop/       # ウィンドウ画像の余白・タイトルバーを切り落とす矩形の検出
internal/output/     # 出力フォルダの連番作成（上書き防止）
internal/pdf/        # 連番 PNG → PDF 結合（自前の最小 PDF ライター。余白トリミング対応）
internal/session/    # キャプチャワーカー（撮影ループ・一時停止と再開・PDF 作成・状態とログ）
internal/ocr/        # Vision OCR の Swift ヘルパー（埋め込み・初回ビルド・実行）
internal/markdown/   # OCR 結果から Markdown を組み立てる（柱・ルビの除去、見出し、段落の結合）
.claude/skills/kindle-to-md/  # Claude Code 用の操作手順（プロジェクトスキル）
docs/                # 決定記録
```

> このリポジトリは、もともとの Python 版アプリのリポジトリに Go 実装を統合したもの。
> Python 版のコードと、ブラウザから操作していた以前の版は削除されたが、git 履歴には残っている。
