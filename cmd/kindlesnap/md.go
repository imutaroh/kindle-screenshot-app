package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/imutaroh/kindle-screenshot-app/internal/markdown"
	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// convertMarkdown は dir の page_*.png を OCR して Markdown を作る。
// 戻り値は作った Markdown のパス・処理したページ数・目視確認が必要なページ番号。
//
// 関数変数にしているのは、capture / md の流れのテストで偽物に差し替えるため
// （本物は Swift ヘルパーのビルドと Vision の OCR を伴い、ユニットテストには重い）。
var convertMarkdown = func(ctx context.Context, dir, title string, progress func(done, total int)) (mdPath string, pages int, review []int, err error) {
	res, err := markdown.ConvertDir(ctx, dir, markdown.Options{BookTitle: title, Progress: progress})
	if err != nil {
		return "", 0, nil, err
	}
	return res.MarkdownPath, res.Pages, res.ReviewPages, nil
}

// checkOCR は doctor の「OCR ヘルパーを使えるか」の確認。
// macOS のバージョン・swiftc・ヘルパーのビルドまで確かめる（未ビルドなら初回だけ数秒かかる）。
// 関数変数なのは doctor のテストで差し替えるため。
var checkOCR = ocr.Check

// markdownProgress は Markdown 変換の進捗を state イベントにする関数を返す。
// 毎ページ出すと数百行になるので、10ページごとと最後だけ出す（finalize の余白検出と同じ間隔）。
func markdownProgress(sink *eventSink, status string) func(done, total int) {
	return func(done, total int) {
		if done%10 == 0 || done == total {
			sink.state(status, done, fmt.Sprintf("Markdown変換中...（%d / %d ページ）", done, total))
		}
	}
}

// cmdMD は kindlesnap md --dir BOOKDIR。既存の本のフォルダの page_*.png から Markdown を作り直す
// （capture で Markdown 変換に失敗した、OCR を改善したので作り直したい、など）。
// PNG は消さない。Ctrl-C で変換を中断できる。
func cmdMD(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("md", stderr)
	dir := fs.String("dir", "", "page_*.png がある本のフォルダ（必須）")
	title := fs.String("title", "", "Markdown の書名（省略時はフォルダ名）")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if *dir == "" {
		return usageError(fs, "--dir を指定してください")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return usageError(fs, "--dir を解決できません: %v", err)
	}
	if *title == "" {
		*title = filepath.Base(absDir)
	}

	sink := newEventSink(stdout, os.Getpid(), *title)
	defer sink.close()
	res := resultEvent{BookDir: absDir}

	// md は短時間で終わるとは限らない（数百ページの OCR）ので、Ctrl-C で止められるようにする
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if st, err := os.Stat(absDir); err != nil || !st.IsDir() {
		res.Status = statusError
		res.Error = strPtr("フォルダが見つかりません: " + absDir)
		sink.result(res)
		return exitError
	}
	// run.jsonl / status.json は本のフォルダに追記する（capture のときの記録と並べて読めるように）
	if err := sink.setBookDir(absDir); err != nil {
		sink.logNow("error", "⚠️ "+err.Error())
	}

	sink.state(statusRunning, 0, "Markdown変換中...")
	mdPath, pages, review, err := convertMarkdown(ctx, absDir, *title, markdownProgress(sink, statusRunning))
	res.Pages = pages
	res.ReviewPages = review
	switch {
	case ctx.Err() != nil:
		res.Status = statusInterrupted
		res.Error = strPtr("中断されました")
		sink.state(res.Status, pages, "Markdown変換を中断しました")
		sink.result(res)
		return exitInterrupted
	case err != nil:
		res.Status = statusError
		res.Error = strPtr("Markdown変換に失敗しました: " + err.Error())
		sink.logNow("error", "❌ "+*res.Error)
		sink.state(res.Status, pages, *res.Error)
		sink.result(res)
		return exitError
	}
	res.OK = true
	res.Status = statusDone
	res.Markdown = strPtr(mdPath)
	sink.logNow("success", "✅ Markdown を作成しました: "+mdPath)
	sink.state(res.Status, pages, "Markdown を作成しました")
	sink.result(res)
	return exitOK
}
