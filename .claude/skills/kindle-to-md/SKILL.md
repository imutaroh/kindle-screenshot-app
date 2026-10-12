---
name: kindle-to-md
description: Kindle for Mac の本を（ライブラリから選んで開始ページを開くところから）1ページずつ撮影し、PDF と Markdown（縦書き日本語の OCR）にする。このリポジトリの kindlesnap CLI を Claude が操作する手順。「Kindleの本をPDFとMarkdownにして」「この本取り込んで」「Kindle取り込み」「Kindleの本をテキスト化して」「Kindle の続きから撮って」のような依頼で使う。漫画・コミックは PDF のみ（--no-md）。
---

# Kindle の本を PDF / Markdown にする

`kindlesnap` は Kindle for Mac のウィンドウを1ページずつ撮影し（キー送信でページをめくる）、
PDF にまとめ、Apple Vision の OCR で Markdown を作る CLI。出力はすべて JSON。
Claude はこの手順に沿って、事前チェック → 本を選んで開始ページを開く → 撮影 → 結果の報告 までを進める。

**著作権**: 出力（PNG・PDF・Markdown）は利用者本人の私的利用に限る。共有・公開・
アップロード・リポジトリへのコミットはしない。本文の要約を公開用の文章に書くこともしない。

## 絶対に守ること

- **Kindle にキーボードショートカットを送らない**（osascript の keystroke、⌘0 など）。
  テストで ⌘0 が読書位置をリセットした。ページ送りは kindlesnap だけが行う
- `kindlesnap click` / `park` は**本物のマウスカーソルを動かす**。使う前に利用者に一言断る
- 撮影は**今 Kindle に表示されているページから**始まる。本の選定や開始ページへの移動は、
  「3. 本を選んで開始ページを開く」の通り **peek で画面を見て click で操作する**（キーボードは使わない）。
  どこから撮るか（表紙から・序章から・続きから）が依頼から読めなければ利用者に聞く
- 同時に動かせる capture は1つだけ（ロックで拒否される）

## 1. ビルド

リポジトリのルートで:

```bash
go build -o kindlesnap ./cmd/kindlesnap
```

以降のコマンドはルートで `./kindlesnap ...` として実行する。

## 2. 事前チェック（doctor）

```bash
./kindlesnap doctor --launch
```

`--launch` は Kindle が起動していなければバックグラウンドで起動し、ウィンドウを最大30秒待つ。
初回は OCR ヘルパー（Swift）のビルドで数秒かかる（`~/Library/Caches/kindlesnap/` にキャッシュ）。
出力は `{"ok":bool,"checks":[{"name","ok","detail","fix"}]}`、終了コードは全部 OK なら 0、そうでなければ 3。

失敗した項目は `fix` をそのまま利用者に伝える。項目ごとの補足:

| name | 失敗したときに利用者へ伝えること |
| --- | --- |
| `macos` | macOS 26 以降が必要（OCR が使う Vision の機能） |
| `kindle_running` / `kindle_window` | Kindle for Mac を起動して本を開いてもらう |
| `window_onscreen` | **フルスクリーンをやめて通常のウィンドウ表示**にしてもらう（最小化も解除） |
| `screen_recording` | システム設定 > プライバシーとセキュリティ > 画面収録 で、**Claude Code を動かしているターミナルアプリ**を許可し、ターミナルを再起動してもらう |
| `accessibility` | 同じくアクセシビリティで許可（ページ送りのキーとクリックに必要） |
| `ocr` | `fix` の通り（多くは `xcode-select --install`）。Markdown が要らなければ `--no-md` で撮影だけできる |

doctor が OK でも、撮影の前に次を確かめ、必要なら利用者に頼む（doctor では検出できない）:

- Kindle を**フルスクリーンにせず、通常のウィンドウ**で表示する（最小化もしない）。
  フルスクリーンで別の Space にあっても撮影はできるため、doctor の `window_onscreen` は
  OK（「画面外（別 Space）ですが撮影できます」）になる。doctor の結果では判断せず、利用者に直接頼む。
  フルスクリーンのままだと、一時停止したときに本文をクリックするのに Space を切り替える必要があり、
  `kindlesnap click` も使えない
- 画質を上げたいなら、Kindle を **Retina（MacBook 内蔵）ディスプレイ**に置く。外部の 1x ディスプレイだと
  画素数が半分になり、OCR の精度が落ちる（ログに「画質注意」が出る）。peek の `width / window.w` が 1 なら等倍。
  内蔵ディスプレイが使えない（蓋を閉じている）ときは等倍のまま撮ってよい（等倍でも本文の OCR は実測で誤りほぼ 0）
- 撮影中は Kindle のウィンドウを動かさない・大きさを変えない

## 3. 本を選んで開始ページを開く（peek と click）

本の選定と開始ページへの移動は、Claude が画面を見て操作する。判断が要るのはここだけで、
ページ送りと撮影の繰り返しは capture（kindlesnap）に任せる。

### 道具

```bash
./kindlesnap peek                     # → {"path","width","height","window":{x,y,w,h}}
./kindlesnap click --x 120 --y 200 [--double]
./kindlesnap park                     # マウスを Kindle のウィンドウの外（画面上）へ動かす
```

- peek の `path` の PNG を Read で見て、押す位置を決める。`click` の座標は**ウィンドウ左上からのポイント**なので、
  PNG の画素座標を `width / window.w`（Retina なら 2）で割って渡す
- click の後は、毎回 peek して結果を確かめてから次を押す（画面の切り替わりに1〜2秒待つ）
- click と park は**本物のマウスカーソルを動かす**。始める前に利用者へ「Kindle を前面に出してマウスを動かすので、
  少し触らないでほしい」と一言断る

### クリックの前に Kindle を前面に出す

peek は Kindle のウィンドウだけを撮るので、ターミナルやブラウザが上に重なっていても画像には写らない。
一方 click は画面上のその位置に本物のマウスイベントを送るので、重なっていると上にあるウィンドウ
（ターミナル・ブラウザのリンクなど）をクリックしてしまう。最初の click の前に、Kindle を前面に出す:

```bash
open -b com.amazon.Lassen             # Kindle を前面に出す（キー操作ではないので読書位置は変わらない）
```

click の後に peek しても変化が無いときは、ダブルクリックを繰り返す前に、まず重なりを疑う。

### 手順

1. **今の画面を見る**: peek。読書画面なら左上の「<」（戻る）でライブラリへ、ライブラリなら次へ
2. **本を開く**: ライブラリで目的の本の表紙を `--double` でダブルクリックする。見つからなければ
   ライブラリの検索欄をクリックしてもらうなど、利用者に頼む（Claude は文字入力のキーを送らない）。
   未ダウンロードの本は、最初のダブルクリックでダウンロードだけ始まることがある。peek で確かめ、
   必要ならもう一度ダブルクリックする
3. **開始ページへ移動する**: 本を開くと前回の読書位置が出る。別の場所から撮るなら、上のツールバーの
   目次ボタン（左上の「≡」に似たアイコン。ツールバーが隠れていれば本文を1回クリックすると出る）を click し、
   目次で章名を click する。表紙から撮るなら目次の「初めに戻る」。画面下の表示（「135ページ中の10ページ目」など）と
   本文を peek で見て、開始ページに来たことを確かめる
4. **本文を1回クリックする**: ツールバーとスライダーを消し、キー受付を戻す（Kindle は起動直後や
   前面の切り替え後に、バックグラウンドのキーを受け付けないことがある）。本文の中央あたりを click
5. **マウスを外へ出す**: `./kindlesnap park`。カーソルが乗っているとページ送りの矢印が表示されて写り込み、
   余白トリミングと OCR を汚す（キー受付が止まる原因にもなる）
6. **最終確認**: もう一度 peek し、ツールバー・矢印・目次の一覧が写っていないこと、開始ページであることを確かめる

利用者が自分で開始ページを開いた場合も、4〜6（本文クリック → park → peek）はやってよい。

## 4. 撮影（capture）

本1冊は Bash のフォアグラウンド上限（10分）より長くかかるので、**必ず `run_in_background` で実行する**。

```bash
./kindlesnap capture --book "書名"
```

主なオプション:

- `--book NAME`（必須）: 出力フォルダ名と PDF / Markdown のファイル名
- `--direction left|right`: ページをめくる方向。日本語の縦書き（右綴じ）は `left`（既定）、横書きの本は `right`
- `--no-md`: Markdown を作らない。**漫画・コミック・写真集は `--no-md`**（OCR が吹き出しを拾いきれない）
- `--max-pages N`: 撮る最大ページ数（0 = 無制限、既定）。試し撮りなら `--max-pages 5`
- `--keep-png`: 完了後も page_*.png を残す
- `--out DIR`: 出力先のルート（既定は `$KINDLESNAP_OUT`、無ければ `~/Documents/KindleBooks`）
- `--pdf-pages N`: PDF 1ファイルあたりのページ数（既定 50）
- `--no-crop`: PDF の余白トリミングをしない

開始すると5秒のカウントダウンの後に撮影が始まる。開始直後に事前チェック（アクセシビリティと1枚の試し撮り）で
失敗すると終了コード 3 で終わる。その場合は doctor を実行して原因を伝える。

### 進み具合の見方

stdout と `<bookDir>/run.jsonl` に同じ JSON Lines が流れる。1行1イベント:

- `{"type":"log","time","level","text"}`
- `{"type":"state","status":"running|paused|done|error|interrupted","page":N,"message"}`
- `{"type":"result","ok","status","book_dir","pdfs":[...],"markdown":path|null,"pages","review_pages":[...],"error"}`（最後に1回）

`<bookDir>` は `kindlesnap status` の `book_dir` で分かる。Monitor で `run.jsonl` の state / result 行を見張るか、
定期的に `./kindlesnap status` を実行する（実行中なら `{"running":true,"book_dir",...,"status":{...}}`）。
終わった取り込みの結果は `<bookDir>/status.json` の `result` にある。
全ページの log を逐一読む必要はない（1ページごとに出る）。

### 一時停止（status: paused）になったら

ページが進まなくなると、再送の後に一時停止する。原因は2つで、画面だけでは区別できない:

1. `./kindlesnap peek` で撮った画像を Read で見る
2. **本の最後**（最終ページ・「レビュー」「おすすめ」画面など）なら `./kindlesnap finish`
   → そこまでのページで PDF・Markdown を作って完了する
3. 本文の途中なら、Kindle がキーを受け付けなくなっている。**ページ本文を1回クリックして、マウスを外へ出す**。
   キー受付が戻ると自動で再開する。利用者に一言断ってから Claude が `open -b com.amazon.Lassen` →
   本文の中央を `click` → `park` で行ってよい（利用者に頼んでもよい）。本文以外（端の矢印・ツールバー）を
   押すとページが動くので、押す位置は peek の画像で本文の中央を選ぶ

一時停止のまま15分たつと、そこまでのページで撮影を終える。**このときも完了と同じ扱い**になり、
result は `ok:true`・`status:"done"`、終了コード 0 で、Markdown も作られ、page_*.png は削除される。
最終ページまで撮れたとは限らないので、見分け方を「5. 結果の報告」に書いてある。

### 止めたいとき

- `./kindlesnap finish`: 撮影を止め、そこまでのページで PDF と Markdown を作って完了。
  ただし開始前の5秒のカウントダウン中（1ページ目を撮る前）に送ると、撮れたページが無いので
  中断（`status:"interrupted"`、終了コード 4）として終わる
- `./kindlesnap stop`: 中断。撮れたページの PDF は作るが、Markdown は作らず PNG を残す

## 5. 結果の報告

`result` 行（または status.json の `result`）を読んで利用者に伝える:

- `pdfs`: PDF のパス
- `markdown`: Markdown のパス（`--no-md` なら null）
- `review_pages`: OCR が怪しいページ番号（文字が取れなかった・確信度が極端に低い・ヘルパーのエラー）
- `ok:false` のときは `error` を伝える。終了コード: 1 = 失敗、3 = 事前チェック失敗、4 = 中断

**`ok:true` でも途中で終わっていることがある（一時停止の時間切れ）。** 結果を伝える前に、最後の
`state` 行の `message`（`kindlesnap status` や status.json の `message` でも同じ）か、`run.jsonl` の log を見る。
`message` が「最終ページか確認できないまま終了しました（N ページ）」で始まっている、または log に
「⚠️ 一時停止が…続いたため、Nページまでで終了します」がある場合は、本の最後まで撮れていない可能性がある。
利用者には「N ページ目で止まっていて、本が途中で切れているかもしれない」と伝え、続きが必要なら
Kindle で続きのページ（N+1 ページ目に当たるページ）を開いてもらい、別の `--book`（例: 「書名_続き」）で
新しく capture すればよいと案内する。

**Markdown だけ失敗した**場合（`status:"done"` なのに `ok:false`、`markdown:null`）、PDF はできていて
page_*.png も残っている。原因（多くは OCR の準備不足）を直してから作り直す:

```bash
./kindlesnap md --dir "<bookDir>" [--title "書名"]
```

中断した場合も PNG は残っているので、同じコマンドで撮れた分の Markdown を作れる。

### （任意）要確認ページを直す

利用者が望めば、`review_pages` のページを PDF から Read で見て（PDF は `--pdf-pages`（既定 50）ページごとに
分かれている。既定ならページ N は `part⌈N/50⌉` の `(N-1)%50+1` ページ目。`--keep-png` なら page_N.png を直接見てもよい）、
Markdown の該当箇所（`<!-- p.N -->` の後ろ）を直す。表・図・手書き・漫画のコマは OCR が苦手なので、
そこも同じ方法で補える。直した内容も私的利用の範囲に留める。

ページ N が前のページの段落の続きで始まっている場合、その書き出し部分は前のページの段落の末尾に
つながれていて、`<!-- p.N -->` の**前**にある（印はつないだ段落の後ろに来る）。ページ N を直すときは、
印の直前の段落の末尾も確かめる。

## 出力の場所

```
<out>/<書名>/            # 同名フォルダが既にあれば <書名>_2 など
  <書名>_part1.pdf ...   # --pdf-pages ページごと
  <書名>.md              # Markdown（--no-md なら無し）
  run.jsonl              # イベントの記録
  status.json            # 最後の状態と結果
  page_0001.png ...      # 完了時に削除（--keep-png・中断・Markdown 失敗なら残る）
```
