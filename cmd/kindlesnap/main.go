// Command kindlesnap は Kindle for Mac の本を撮影して PDF / Markdown にする CLI。
//
// 主な利用者は Claude Code（人間が直接叩くことは少ない）なので、出力はすべて JSON にする。
// 長く動くサブコマンド（capture / md）は1行1イベントの JSON Lines を流し、
// 最後の1行を {"type":"result",...} にする。
//
//	kindlesnap doctor [--launch]       事前チェック
//	kindlesnap capture --book NAME     撮影 → PDF → Markdown
//	kindlesnap status                  実行中の取り込みの状態
//	kindlesnap finish | stop           実行中の取り込みを終了（PDF作成）/ 中断
//	kindlesnap md --dir BOOKDIR        page_*.png から Markdown を作り直す
//	kindlesnap peek [--out FILE]       Kindle のウィンドウを PNG に撮る
//	kindlesnap click --x X --y Y       Kindle のウィンドウ内をクリックする
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// 終了コード。Claude Code が結果の JSON を読む前に、大まかな成否を判断できるようにする。
const (
	exitOK          = 0 // 成功
	exitError       = 1 // 失敗（実行時のエラー）
	exitUsage       = 2 // 引数の誤り
	exitPreflight   = 3 // 事前チェック（権限・Kindle の状態）に失敗
	exitInterrupted = 4 // 中断された（stop / Ctrl-C）
)

const usageText = `使い方: kindlesnap <サブコマンド> [オプション]

サブコマンド:
  doctor [--launch]           事前チェック（権限・Kindle・OCR）。--launch で Kindle を起動して待つ
  capture --book NAME [...]   撮影 → PDF → Markdown（JSON Lines を出力）
  status                      実行中の取り込みの状態を表示
  finish                      実行中の取り込みを終了し、そこまでのページで PDF を作る
  stop                        実行中の取り込みを中断する（撮れたページの PDF は作る）
  md --dir BOOKDIR            page_*.png から Markdown を作り直す
  peek [--out FILE]           Kindle のウィンドウを PNG に撮る
  click --x X --y Y [--double]  Kindle のウィンドウ内の座標（ポイント）をクリックする

各サブコマンドのオプションは kindlesnap <サブコマンド> -h で表示します。
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run はサブコマンドを振り分けて終了コードを返す。main から os.Exit を切り離して、
// テストから引数と出力先を差し替えて呼べるようにしている。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "doctor":
		return cmdDoctor(rest, stdout, stderr)
	case "capture":
		return cmdCapture(rest, stdout, stderr)
	case "status":
		return cmdStatus(rest, stdout, stderr)
	case "finish":
		return cmdSignal("finish", rest, stdout, stderr)
	case "stop":
		return cmdSignal("stop", rest, stdout, stderr)
	case "md":
		return cmdMD(rest, stdout, stderr)
	case "peek":
		return cmdPeek(rest, stdout, stderr)
	case "click":
		return cmdClick(rest, stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return exitOK
	default:
		fmt.Fprintf(stderr, "不明なサブコマンドです: %s\n\n%s", cmd, usageText)
		return exitUsage
	}
}

// newFlagSet はサブコマンド用の FlagSet を作る。ContinueOnError にして、
// 解析エラーで os.Exit せず終了コード 2（使い方の誤り）を返せるようにする。
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("kindlesnap "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseFlags は fs.Parse を呼び、続けてよければ ok=true を返す。
// -h は成功（exitOK）、それ以外の解析エラーと余分な位置引数は exitUsage。
func parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(fs.Output(), "余分な引数があります: %v\n", fs.Args())
		return exitUsage, false
	}
	return 0, true
}

// usageError は使い方の誤りを stderr に出して exitUsage を返す。
func usageError(fs *flag.FlagSet, format string, a ...any) int {
	fmt.Fprintf(fs.Output(), format+"\n", a...)
	fs.Usage()
	return exitUsage
}

// writeJSON は v を1行の JSON として w に書く。HTML 用のエスケープ（< > & を < 等に
// する）は人間にも Claude にも読みにくいだけなので切る。
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // 標準出力への書き込み失敗は報告先が無いので無視する
}

// errorOutput は単発のサブコマンド（status / finish / peek など）が失敗したときの出力。
type errorOutput struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// fail は {"ok":false,"error":...} を出力して exitError を返す。
func fail(stdout io.Writer, err error) int {
	writeJSON(stdout, errorOutput{OK: false, Error: err.Error()})
	return exitError
}
