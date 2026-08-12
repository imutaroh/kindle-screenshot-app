// kindlesnap は Kindle for Mac の全ページを自動でスクリーンショットする CLI。
// Python 版 kindle-screenshot-app のフェーズ①（スクショ〜pHash自動停止）の Go 移植。
//
// 使い方:
//
//	kindlesnap -book "本の名前" [-dir left|right] [-max 0] [-out output]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/capture"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/dedupe"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/output"
)

// options はコマンドラインフラグの値をまとめた構造体。
type options struct {
	book         string
	outRoot      string
	direction    capture.Direction
	maxPages     int
	startDelay   time.Duration
	pageWait     time.Duration
	initialWait  time.Duration
	initialPages int
	dupThreshold int
	dupCount     int
}

func parseFlags() (options, error) {
	var o options
	var dir string

	flag.StringVar(&o.book, "book", "", "本の名前（必須。出力フォルダ名になる）")
	flag.StringVar(&o.outRoot, "out", "output", "出力先のルートフォルダ")
	flag.StringVar(&dir, "dir", "left", "ページめくり方向: left=日本語の本 / right=英語の本")
	flag.IntVar(&o.maxPages, "max", 0, "最大ページ数（0=無制限。自動停止に任せる）")
	flag.DurationVar(&o.startDelay, "delay", 5*time.Second, "開始前の待機時間")
	flag.DurationVar(&o.pageWait, "wait", 500*time.Millisecond, "ページ送り後の待機時間")
	flag.DurationVar(&o.initialWait, "initial-wait", 2*time.Second, "最初の数ページの待機時間（読み込みが遅いため長め）")
	flag.IntVar(&o.initialPages, "initial-pages", 5, "長めに待機する最初のページ数")
	flag.IntVar(&o.dupThreshold, "dup-threshold", 5, "pHashハミング距離の閾値（これ以下なら同一ページ扱い）")
	flag.IntVar(&o.dupCount, "dup-count", 2, "同一ページが何回連続したら最終ページと判断するか")
	flag.Parse()

	if o.book == "" {
		return o, errors.New("-book で本の名前を指定してください")
	}
	switch dir {
	case "left":
		o.direction = capture.Left
	case "right":
		o.direction = capture.Right
	default:
		return o, fmt.Errorf("-dir は left か right を指定してください（指定値: %s）", dir)
	}
	return o, nil
}

func main() {
	opts, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, "❌", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	// Ctrl+C（SIGINT）で ctx がキャンセルされ、ループが安全に抜ける
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("==================================================")
	fmt.Println("📚 kindlesnap - Kindle 自動スクショ (Go版)")
	fmt.Println("==================================================")

	outDir, err := output.CreateUniqueDir(opts.outRoot, opts.book)
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(outDir)
	fmt.Printf("📁 保存先: %s\n", abs)

	kindle := &capture.Kindle{}
	if err := kindle.Activate(); err != nil {
		fmt.Printf("⚠️  %v\n    %v秒以内に手動で Kindle を前面に出してください。\n",
			err, opts.startDelay.Seconds())
	} else {
		fmt.Println("🖥️  Kindle を前面に表示しました（フルスクリーン推奨）")
	}

	if err := countdown(ctx, opts.startDelay); err != nil {
		return err
	}

	fmt.Println("🚀 開始！ 停止するには Ctrl+C")
	fmt.Println("--------------------------------------------------")

	captured, err := captureAllPages(ctx, kindle, outDir, opts)
	if err != nil {
		return err
	}

	fmt.Println("--------------------------------------------------")
	fmt.Printf("✅ %d ページを保存しました: %s\n", captured, abs)
	return nil
}

// countdown は開始前カウントダウンを表示する。Ctrl+C で即中断できる。
func countdown(ctx context.Context, d time.Duration) error {
	remaining := int(d.Seconds())
	for i := remaining; i > 0; i-- {
		fmt.Printf("⏱️  %d秒後に開始...\r", i)
		if err := sleepCtx(ctx, time.Second); err != nil {
			fmt.Println()
			return err
		}
	}
	fmt.Println()
	return nil
}

// captureAllPages はスクショ→重複判定→ページ送りを繰り返し、保存したページ数を返す。
func captureAllPages(ctx context.Context, kindle *capture.Kindle, outDir string, opts options) (int, error) {
	var prev dedupe.Hash
	dupRun := 0 // 直前ページと同一と判定された連続回数

	for page := 1; ; page++ {
		// Ctrl+C チェック（ブロックせずに ctx の状態だけ見る）
		select {
		case <-ctx.Done():
			fmt.Printf("\n⚠️  中断されました（%dページまで保存済み）\n", page-1)
			return page - 1, nil
		default:
		}

		if opts.maxPages > 0 && page > opts.maxPages {
			fmt.Printf("\n最大ページ数 %d に到達しました。\n", opts.maxPages)
			return page - 1, nil
		}

		path := pagePath(outDir, page)
		if err := capture.Screenshot(path); err != nil {
			return page - 1, err
		}

		h, err := dedupe.HashFile(path)
		if err != nil {
			return page - 1, err
		}

		// 直前のページとほぼ同じなら「最終ページでページ送りが効いていない」可能性
		if !prev.IsZero() {
			dist, err := h.Distance(prev)
			if err != nil {
				return page - 1, err
			}
			if dist <= opts.dupThreshold {
				dupRun++
				if dupRun >= opts.dupCount {
					// 余分に撮った重複ページを削除して終了
					for i := 0; i < dupRun; i++ {
						os.Remove(pagePath(outDir, page-i))
					}
					saved := page - dupRun
					fmt.Printf("\n同じページが%d回連続（pHash距離≦%d）→ 最終ページと判断して停止。%dページ保存。\n",
						dupRun, opts.dupThreshold, saved)
					return saved, nil
				}
			} else {
				dupRun = 0
			}
		}
		prev = h

		fmt.Printf("[%d] 保存: %s\n", page, path)

		if err := kindle.TurnPage(opts.direction); err != nil {
			return page, err
		}

		// 最初の数ページは Kindle の読み込みが遅いので長めに待つ
		wait := opts.pageWait
		if page <= opts.initialPages {
			wait = opts.initialWait
		}
		if err := sleepCtx(ctx, wait); err != nil {
			fmt.Printf("\n⚠️  中断されました（%dページまで保存済み）\n", page)
			return page, nil
		}
	}
}

// pagePath はページ番号から連番ファイルパスを作る（例: output/本/page_0001.png）。
func pagePath(outDir string, page int) string {
	return filepath.Join(outDir, fmt.Sprintf("page_%04d.png", page))
}

// sleepCtx は d だけ待つが、ctx がキャンセルされたら即座に ctx.Err() を返す。
// time.Sleep と違い Ctrl+C にすぐ反応できる。
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
