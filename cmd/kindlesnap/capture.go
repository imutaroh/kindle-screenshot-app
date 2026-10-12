package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/pdf"
	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

// captureOptions は capture のコマンドライン引数。
type captureOptions struct {
	book     string
	out      string
	maxPages int
	dir      capture.Direction
	pdfPages int
	noMD     bool
	keepPNG  bool
	noCrop   bool
}

// parseCaptureFlags は capture の引数を解析・検証する。ok=false なら code で終了する。
func parseCaptureFlags(args []string, stderr io.Writer) (opts captureOptions, code int, ok bool) {
	fs := newFlagSet("capture", stderr)
	fs.StringVar(&opts.book, "book", "", "本の名前（必須。出力フォルダ名と PDF / Markdown のファイル名に使う）")
	fs.StringVar(&opts.out, "out", "", "出力先のルートフォルダ（省略時は $KINDLESNAP_OUT、無ければ ~/Documents/KindleBooks）")
	fs.IntVar(&opts.maxPages, "max-pages", session.DefaultMaxPages, "撮影する最大ページ数（0 = 無制限。本の最後で一時停止したら kindlesnap finish）")
	direction := fs.String("direction", string(session.DefaultDirection), "ページをめくる方向: left（日本語の縦書き・右綴じ）| right（横書き・左綴じ）")
	fs.IntVar(&opts.pdfPages, "pdf-pages", session.DefaultPDFPagesPerFile, "PDF 1ファイルあたりのページ数")
	fs.BoolVar(&opts.noMD, "no-md", false, "Markdown を作らない（撮影と PDF だけ）")
	fs.BoolVar(&opts.keepPNG, "keep-png", false, "完了後も page_*.png を残す")
	fs.BoolVar(&opts.noCrop, "no-crop", false, "PDF の余白を自動トリミングしない")
	if code, ok := parseFlags(fs, args); !ok {
		return opts, code, false
	}

	opts.book = strings.TrimSpace(opts.book)
	switch {
	case opts.book == "":
		return opts, usageError(fs, "--book を指定してください"), false
	case *direction != string(capture.Left) && *direction != string(capture.Right):
		return opts, usageError(fs, "--direction は left か right を指定してください: %q", *direction), false
	case opts.maxPages < 0:
		return opts, usageError(fs, "--max-pages は0以上を指定してください: %d", opts.maxPages), false
	case opts.pdfPages < 1:
		return opts, usageError(fs, "--pdf-pages は1以上を指定してください: %d", opts.pdfPages), false
	}
	opts.dir = capture.Direction(*direction)
	return opts, 0, true
}

// preflightLite は撮影を始める前の最低限の確認（doctor の一部）。失敗した項目だけを返す。
// 撮影の途中で権限不足に気づくと、ページが進まない一時停止と区別がつかず時間を無駄にするため、
// アクセシビリティ・Kindle のウィンドウ・画面収録（実際に1枚撮る）だけは先に確かめる。
// OCR の確認は入れない（撮影と PDF は OCR が無くてもできる。Markdown の段階で失敗を報告する）。
var preflightLite = func() []check {
	var failed []check
	if err := capture.CheckAccessibility(); err != nil {
		failed = append(failed, check{Name: "accessibility", Detail: err.Error()})
	}
	if err := testCapture(); err != nil {
		failed = append(failed, check{Name: "kindle_capture", Detail: err.Error(),
			Fix: "kindlesnap doctor で原因を確認してください"})
	}
	return failed
}

// cmdCapture は kindlesnap capture。撮影 → PDF → Markdown → PNG 削除 までを行い、
// 進み具合を JSON Lines で流す（標準出力と <bookDir>/run.jsonl）。
//
// シグナル:
//   - SIGUSR1（kindlesnap finish）: 撮影を止め、そこまでのページで PDF・Markdown を作って完了する
//   - SIGINT / SIGTERM（kindlesnap stop・Ctrl-C）: 中断する。撮れたページの PDF は作るが、
//     Markdown は作らず PNG も残す（後で kindlesnap md --dir で作れるように）。
//     Markdown 変換中に受けた場合は変換を中断する
func cmdCapture(args []string, stdout, stderr io.Writer) int {
	opts, code, ok := parseCaptureFlags(args, stderr)
	if !ok {
		return code
	}

	sink := newEventSink(stdout, os.Getpid(), opts.book)
	defer sink.close()

	// 失敗で終わるときの result を出すヘルパー（book_dir が決まる前の失敗にも使う）
	failResult := func(status, msg string, exit int) int {
		sink.logNow("error", "❌ "+msg)
		sink.result(resultEvent{Status: status, BookDir: sink.bookDir(), Error: strPtr(msg)})
		return exit
	}

	outRoot, err := resolveOutRoot(opts.out, os.Getenv("KINDLESNAP_OUT"), os.UserHomeDir)
	if err != nil {
		return failResult(statusError, err.Error(), exitError)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return failResult(statusError, "出力先を作れません: "+err.Error(), exitError)
	}

	mgr := session.NewManager(outRoot)

	// シグナルはロックを取る前から受ける。ロックに pid を書いた瞬間から kindlesnap finish が
	// SIGUSR1 を送ってくる可能性があり、ハンドラが無いと SIGUSR1 の既定動作（終了）で落ちるため。
	// 事前チェック中の Ctrl-C も「中断」として扱う。
	// stopRequested は「中断を頼まれた」ことを覚えておくフラグ（撮影が終わった後の
	// Markdown 段階で、続けるかどうかの判断に使う）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stopRequested atomic.Bool
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			if sig == syscall.SIGUSR1 {
				if err := mgr.Finish(); err != nil {
					sink.logNow("info", "finish を受け取りましたが、撮影中ではないため無視しました")
				} else {
					// 開始前のカウントダウン中（1ページ目を撮る前）に受けた場合、session は
					// 「中断」として終える（PDF にするページが無い）ので、PDF を作るとは言い切らない
					sink.logNow("info", "finish を受け取りました。撮影を止め、撮れたページで PDF を作ります（1ページ目を撮る前なら中断として終わります）")
				}
				continue
			}
			stopRequested.Store(true)
			if err := mgr.Stop(); err != nil {
				// 撮影中でなければ（事前チェック中・Markdown 変換中）その処理を止める
				cancel()
			}
			sink.logNow("info", fmt.Sprintf("%v を受け取りました。中断します", sig))
		}
	}()

	// 同時に2つ撮影すると、どちらのキーで Kindle のページが進んだか分からなくなる。1つに限る
	lock, err := acquireLock(lockPath(), os.Getpid())
	if err != nil {
		return failResult(statusError, err.Error(), exitError)
	}
	defer lock.release()

	if failed := preflightLite(); len(failed) > 0 {
		msgs := make([]string, 0, len(failed))
		for _, c := range failed {
			msgs = append(msgs, c.Name+": "+c.Detail)
		}
		return failResult(statusError, "事前チェックに失敗しました（kindlesnap doctor で詳細を確認）: "+strings.Join(msgs, " / "), exitPreflight)
	}
	if stopRequested.Load() {
		return failResult(statusInterrupted, "撮影を始める前に中断されました", exitInterrupted)
	}

	// session のフック。ワーカー goroutine から同期的に呼ばれる
	mgr.OnLog = func(e session.LogEntry) {
		sink.log(e.Time, e.Level, e.Text)
	}
	mgr.OnState = func(st session.State) {
		if st.OutputDir != "" && sink.bookDir() == "" {
			if err := sink.setBookDir(st.OutputDir); err != nil {
				sink.logNow("error", "⚠️ "+err.Error())
			}
			if err := lock.setBookDir(st.OutputDir); err != nil {
				sink.logNow("error", "⚠️ "+err.Error())
			}
		}
		// 終わった後の状態は、Markdown まで済ませてから CLI が自分で出す
		if st.IsRunning {
			sink.state(cliStatus(st), st.CurrentPage, st.Message)
		}
	}

	settings := session.Settings{
		BookName:        opts.book,
		MaxPages:        opts.maxPages,
		PDFPagesPerFile: opts.pdfPages,
		AutoDeletePNG:   !opts.keepPNG,
		AutoCrop:        !opts.noCrop,
		Direction:       opts.dir,
		// PNG は Markdown を作った後に消す（finalize の中で消されると OCR できない）
		DeferPNGDelete: true,
	}
	// mgr.Run（Start + Wait）ではなく、Start と Wait の間で中断要求を確かめ直す。
	// 上の stopRequested の確認から Start が isRunning を立てるまでの間に Ctrl-C / stop が
	// 来ると、シグナル側の mgr.Stop() は「撮影中ではない」で失敗し、ctx を止めるだけになる。
	// そのままだと session は中断を知らずに本1冊を撮り切ってしまう。
	// シグナル側は Stop を試す前に stopRequested を立てるので、Start の後にここで
	// stopRequested を見れば、取りこぼした中断要求を必ず拾える（Stop の重複は無害）。
	if err := mgr.Start(settings); err != nil {
		return failResult(statusError, err.Error(), exitError)
	}
	if stopRequested.Load() {
		_ = mgr.Stop()
	}
	mgr.Wait()

	st := mgr.Snapshot()
	res, exit := finishCapture(ctx, sink, st, opts, &stopRequested)
	sink.state(res.Status, res.Pages, finalMessage(st, res))
	sink.result(res)
	return exit
}

// finishCapture は撮影と PDF 作成が終わった後の処理（Markdown 変換・PNG 削除）を行い、
// 結果と終了コードを返す。
//
// PNG は「Markdown を作れた（または --no-md）完了」のときだけ消す。中断・エラー・
// Markdown の失敗では残し、kindlesnap md --dir でやり直せるようにする。
func finishCapture(ctx context.Context, sink *eventSink, st session.State, opts captureOptions, stopRequested *atomic.Bool) (resultEvent, int) {
	res := resultEvent{
		Status:  endStatus(st.EndReason),
		BookDir: st.OutputDir,
		PDFs:    st.PDFFiles,
		Pages:   st.TotalPages,
	}
	keepNote := func() {
		if st.OutputDir != "" && st.TotalPages > 0 {
			sink.logNow("info", "page_*.png は残しています（kindlesnap md --dir \""+st.OutputDir+"\" で Markdown を作れます）")
		}
	}

	switch st.EndReason {
	case session.EndInterrupted:
		res.Error = strPtr("中断されました")
		keepNote()
		return res, exitInterrupted
	case session.EndDone, session.EndPauseTimeout:
		// 下で続ける
	default:
		msg := st.Error
		if msg == "" {
			msg = "取り込みに失敗しました"
		}
		res.Error = strPtr(msg)
		keepNote()
		return res, exitError
	}

	// ここからは撮影・PDF 作成が完了している
	if !opts.noMD {
		if stopRequested.Load() {
			// PDF 作成中に中断を頼まれた。Markdown は作らずに終える
			res.Status = statusInterrupted
			res.Error = strPtr("中断されたため Markdown は作成していません")
			keepNote()
			return res, exitInterrupted
		}
		sink.state(statusRunning, 0, "Markdown変換中...")
		mdPath, _, review, err := convertMarkdown(ctx, st.OutputDir, opts.book, markdownProgress(sink, statusRunning))
		res.ReviewPages = review
		switch {
		case ctx.Err() != nil:
			res.Status = statusInterrupted
			res.Error = strPtr("Markdown変換を中断しました")
			keepNote()
			return res, exitInterrupted
		case err != nil:
			msg := "Markdown変換に失敗しました: " + err.Error()
			sink.logNow("error", "❌ "+msg)
			res.Error = strPtr(msg)
			keepNote()
			return res, exitError
		}
		res.Markdown = strPtr(mdPath)
		sink.logNow("success", "✅ Markdown を作成しました: "+mdPath)
	}

	if !opts.keepPNG {
		n, err := pdf.DeletePNGs(st.OutputDir)
		if err != nil {
			// 成果物（PDF / Markdown）はできているので失敗にはしない
			sink.logNow("error", fmt.Sprintf("⚠️ PNG の削除に一部失敗しました（%d個削除）: %v", n, err))
		} else {
			sink.logNow("info", fmt.Sprintf("%d個のPNGを削除しました", n))
		}
	}
	res.OK = true
	return res, exitOK
}

// finalMessage は最後の state イベントのメッセージを作る。
//
//   - 撮影自体が中断・エラーで終わった: session のメッセージ（「中断されました（12ページまで保存）。…」など、
//     PDF を作れたかまで書いてある）をそのまま使う
//   - 撮影は完了したが Markdown で失敗・中断した: その理由
//   - すべて成功: session の完了メッセージに Markdown の作成を添える
func finalMessage(st session.State, res resultEvent) string {
	captureDone := st.EndReason == session.EndDone || st.EndReason == session.EndPauseTimeout
	switch {
	case !captureDone:
		return st.Message
	case res.Error != nil:
		return *res.Error
	case res.Markdown != nil:
		return st.Message + " Markdown を作成しました"
	default:
		return st.Message
	}
}
