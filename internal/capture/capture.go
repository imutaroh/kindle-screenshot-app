// Package capture は macOS 標準コマンド（screencapture / osascript）を使って
// スクリーンショット取得と Kindle のページ送りを行う。
//
// robotgo などの CGo 依存ライブラリを使わず外部コマンドに逃がすことで、
// pure Go を保ち `go build` 一発の単一バイナリにしている。
package capture

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Direction はページめくり方向を表す。
type Direction string

const (
	// Left は日本語の本（縦書き・右綴じ）。左矢印キーで次ページへ進む。
	Left Direction = "left"
	// Right は英語の本（横書き・左綴じ）。右矢印キーで次ページへ進む。
	Right Direction = "right"
)

// macOS の仮想キーコード（CGEventCreateKeyboardEvent に渡す値）
const (
	keyCodeLeftArrow  = 123
	keyCodeRightArrow = 124
)

// kindleAppNames は Kindle for Mac のアプリ名候補。世代によって名前が違う。
var kindleAppNames = []string{"Kindle", "Amazon Kindle"}

// Kindle は Kindle アプリへの操作をまとめた型。
// ウィンドウ発見（Discover）で得た情報をキャッシュし、2回目以降の試行を減らす。
type Kindle struct {
	appName  string     // 発見できたアプリ名（"Kindle" / "Amazon Kindle"。未発見なら空文字）
	pid      int        // Kindle アプリのプロセスID（0 = 未発見）
	windowID int        // キャプチャ対象のウィンドウID（0 = 未発見）
	width    float64    // 発見時のウィンドウ幅（ポイント単位。ログ・診断・解像度の倍率計算用）
	height   float64    // 発見時のウィンドウ高さ（ログ・診断用）
	info     WindowInfo // 発見時のウィンドウ情報の全体（位置・OnScreen。Info で返す）

	// onRediscover はウィンドウ再発見（本の開き直し等でwindowIDが変わった）時に
	// 呼ばれるコールバック。capture パッケージ自体はロガーを持たない設計にして
	// おり、警告ログの配線は呼び出し側（session パッケージ）に委ねる。
	onRediscover func(before, after string)
}

// SetOnRediscover は Discover による再発見でウィンドウ情報が変化したときに
// 呼ばれるコールバックを登録する。
func (k *Kindle) SetOnRediscover(f func(before, after string)) {
	k.onRediscover = f
}

// keyEventScript は矢印キーの down/up を Kindle のプロセスへ直送する JXA。
// fmt.Sprintf で pid とキーコード（いずれも int）を埋め込んで使う。
//
// System Events の `key code`（最前面アプリへの送信）ではなく
// CGEventPostToPid を使うのは、キーボードフォーカスを奪わないため。
// イベントは対象プロセスのイベントキューに直接入り、システム全体の
// 入力ストリームを経由しないので、メインディスプレイで作業中のアプリに
// キーが漏れない（実機検証済み: フォーカスが別アプリのままページがめくれる）。
// down/up を1回の osascript 呼び出しにまとめるのは起動コスト
// （実測0.11〜0.12秒/回）を半減させるため。
//
// 結果は標準出力の1行で返す（TurnPage がリトライしてよいかの判断に使う）:
//   - "ok"            : down/up の両方を送った
//   - "error: ..."    : キーを1つも送る前に失敗した（リトライしても重複しない）
//   - "posted: ..."   : 送信を始めた後に失敗した（キーが届いた可能性があるのでリトライ禁止）
//
// 例外はすべて run() の中で捕まえるので、osascript が非0で終わるのは
// osascript 自体の起動失敗や ObjC.import の失敗（どちらもキー送信前）に限られる。
//
// 修飾キーのフラグは必ず arrowKeyFlags で上書きする。CGEventCreateKeyboardEvent に
// 送信元(NULL)を渡すと、その瞬間に物理的に押されている修飾キー（⌘/⌥/Shift）がイベントに
// 乗る。取り込み中にユーザーがメインで ⌘ を押すと Kindle に「⌘+←」が届き、次の章の
// 先頭（や本の先頭）へジャンプしてページが大量に抜ける（実機で確認: フラグ 0x20b00110 に
// ⌘=0x100000 が乗っていた。漫画で第1話の途中から第2話の扉へ何度も飛んだ原因）。
// 0xA00000 は本物の矢印キーが持つ NumericPad(0x200000) | SecondaryFn(0x800000)。
//
// key-up は少し間をあけて2回送る。Kindle（Mac Catalyst）は矢印キーを押しっぱなしにすると
// アプリ側でページ送りを自動で繰り返す（実機確認: down だけ送ると0.5秒ごとにページが進み続けた）。
// まれに key-up が処理されないことがあり、そのときは次のキーが来るまでページが高速で
// 送られ続けて数十〜百数十ページが抜けた（実機で3回観測）。押していないキーの key-up は
// 無視されるだけなので、2回目は副作用なく「押しっぱなし」を確実に解除する保険になる。
// それでも暴走した場合は、呼び出し側（session の rewindRunaway）が検知して巻き戻す。
const keyEventScript = `
ObjC.import("CoreGraphics");
const arrowKeyFlags = 0x00A00000;
function run() {
	const pid = %d;
	const keyCode = %d;
	let posted = false;
	try {
		const down = $.CGEventCreateKeyboardEvent($(), keyCode, true);
		const up = $.CGEventCreateKeyboardEvent($(), keyCode, false);
		if (!down || !up) {
			throw new Error("CGEventCreateKeyboardEvent が失敗しました");
		}
		$.CGEventSetFlags(down, arrowKeyFlags);
		$.CGEventSetFlags(up, arrowKeyFlags);
		posted = true;
		$.CGEventPostToPid(pid, down);
		delay(0.03);
		$.CGEventPostToPid(pid, up);
		delay(0.12);
		$.CGEventPostToPid(pid, up);
		return "ok";
	} catch (e) {
		return (posted ? "posted: " : "error: ") + e;
	}
}
`

// turnPageRetries は TurnPage の osascript がキー送信前に失敗したときのリトライ回数（初回とは別に数える）。
// osascript は起動直後の一時的な失敗（システム負荷・スリープ復帰直後など）がありうるため、
// 1回の失敗で取り込み全体をエラー終了させず、短い間隔で数回やり直す。
const turnPageRetries = 3

// turnPageRetryInterval は TurnPage のリトライ間隔。
const turnPageRetryInterval = 300 * time.Millisecond

// jxaRunner は JXA の実行関数。ユニットテストで osascript を起動せずに
// TurnPage のリトライ判断を確かめるため、差し替え可能にしている。
var jxaRunner = runJXA

// TurnPage は矢印キーイベントを Kindle のプロセスへ直送してページを1枚めくる。
// 前面化不要（Kindle がバックグラウンド・別ディスプレイ・別 Space でも動く）。
// ターミナルに「アクセシビリティ」権限が必要。
//
// リトライするのは「キーを1つも送っていないと分かる失敗」だけ（osascript の起動失敗・
// スクリプトがキー送信前に報告した失敗）。送信後の失敗や想定外の出力でリトライすると、
// キーが2回以上届いてページを飛ばし、しかも呼び出し側の画面の変化の確認では
// その欠落に気づけないため、即エラーにする。
//
// 注意: キーが「送れたのに Kindle が反応しない」状態（キー不感）はここでは検知できない
// （CGEventPostToPid は届いたかどうかを返さない）。それは呼び出し側が画面の変化で判断する。
func (k *Kindle) TurnPage(dir Direction) error {
	if err := k.ensure(); err != nil {
		return err
	}
	code := keyCodeLeftArrow
	if dir == Right {
		code = keyCodeRightArrow
	}

	var lastErr error
	for attempt := 0; attempt <= turnPageRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(turnPageRetryInterval)
		}
		out, err := jxaRunner(fmt.Sprintf(keyEventScript, k.pid, code))
		switch {
		case err != nil:
			// osascript が非0で終わった＝run() に入る前の失敗。キーは送っていない
			lastErr = fmt.Errorf("ページ送りに失敗しました（%d回試行。システム設定 > プライバシーとセキュリティ > アクセシビリティ を確認）: %w", attempt+1, err)
		case out == "ok":
			return nil
		case strings.HasPrefix(out, "error: "):
			// キー送信前の失敗をスクリプト自身が報告した。キーは送っていない
			lastErr = fmt.Errorf("ページ送りに失敗しました（%d回試行）: %s", attempt+1, out)
		default:
			// 送信を始めた後の失敗、または想定外の出力。キーが届いた可能性があるので再送しない
			return fmt.Errorf("ページ送りスクリプトの結果が想定外です（キーが送られた可能性があるため再送しません）: %q", out)
		}
	}
	return lastErr
}

// axTrustedScript は、このプロセス（を起動したターミナルアプリ）にアクセシビリティ権限が
// あるかを AXIsProcessTrusted で調べる JXA。権限ダイアログは出さない（読み取りのみ）。
// AXIsProcessTrusted は ApplicationServices（HIServices）にあり、CoreGraphics の import
// だけでは undefined になる（実機確認済み）。
const axTrustedScript = `
ObjC.import("ApplicationServices");
String($.AXIsProcessTrusted());
`

// CheckAccessibility はアクセシビリティ権限の有無を確認し、無ければ案内付きのエラーを返す。
//
// CGEventPostToPid は権限が無くてもエラーを返さず、イベントが黙って捨てられる。
// そのまま取り込みを始めると「ページが進まない」状態になり、キー不感（本文クリックで
// 復旧する Kindle 側の問題）と見分けがつかないため、開始時に明示的に確認する。
func CheckAccessibility() error {
	out, err := runJXA(axTrustedScript)
	if err != nil {
		return fmt.Errorf("アクセシビリティ権限の確認に失敗しました: %w", err)
	}
	if out != "true" {
		return fmt.Errorf("アクセシビリティ権限がありません。システム設定 > プライバシーとセキュリティ > アクセシビリティ で、" +
			"kindlesnap を起動したターミナルアプリ（Terminal / iTerm / Ghostty など）を許可してから、ターミナルを再起動してやり直してください")
	}
	return nil
}

// Notify は macOS の通知センターに通知を出す（display notification）。
// 文字列は AppleScript のソースに埋め込まず argv で渡すので、引用符などを含んでいても壊れない。
func Notify(title, message string) error {
	out, err := exec.Command("osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, message).CombinedOutput()
	if err != nil {
		return fmt.Errorf("通知に失敗しました: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Screenshot は Kindle ウィンドウ単体を PNG として path に保存する。
// 失敗時はウィンドウIDが変わった可能性（本の開き直し等）があるため、
// 1回だけ再発見してリトライする。
// ターミナルに「画面収録」権限が必要。
func (k *Kindle) Screenshot(path string) error {
	if err := k.ensure(); err != nil {
		return err
	}
	if err := k.captureWindow(path); err == nil {
		return nil
	}
	before := k.Describe()
	if derr := k.Discover(); derr != nil {
		return derr
	}
	if k.Describe() != before && k.onRediscover != nil {
		k.onRediscover(before, k.Describe())
	}
	return k.captureWindow(path)
}

// captureWindow は k.windowID 指定でウィンドウ単体を PNG として path に保存する。
func (k *Kindle) captureWindow(path string) error {
	// captureSettled は同じ path に上書きし続けるため、screencapture が失敗
	// して古い PNG が残っていると、古い画像を読んで「描画が安定した」
	// と誤判定し、同一ページを誤って採用してしまう。事前削除 + 撮影後の存在
	// 検証が、適応待機の正しさ（＝古い画像を絶対に読ませない）の前提になる。
	os.Remove(path)

	// -l: ウィンドウ単体を指定（背面・別ディスプレイでも撮れる）
	// -o: ウィンドウの影を付けない / -x: シャッター音を鳴らさない
	out, err := exec.Command("screencapture", screencaptureArgs(k.windowID, path)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screencaptureに失敗しました（画面収録権限を確認）: %w: %s",
			err, strings.TrimSpace(string(out)))
	}

	// ページめくりアニメーション中は「could not create image from window」で
	// exit 1 になることを実機で観測済み（上の err で捕捉される）。ここではさらに
	// 防御として、exit 0 なのにファイルが無い・空という未観測の失敗モードにも
	// 備えて実際にファイルが書かれたかを検証する（古い PNG は事前削除済みなので、
	// この検証を抜けた path には必ず今回撮った画像だけが存在する）。
	st, serr := os.Stat(path)
	if serr != nil || st.Size() == 0 {
		return fmt.Errorf("screencaptureは成功しましたが画像が保存されませんでした: %s", path)
	}
	return nil
}

// runJXA は JXA（JavaScript for Automation）を1つ実行し、標準出力の文字列を返す。
// AppleScript ではなく JXA を使うのは、Cocoa（NSEvent / NSScreen）の値を
// JSON.stringify でそのまま取り出せて構文が単純になるため。
//
// 標準エラーは結果に混ぜない（エラー時のメッセージにだけ使う）。osascript は成功時にも
// フレームワークの警告などを標準エラーに出すことがあり、混ぜると結果の文字列比較や
// JSON の解析が壊れるため。
func runJXA(script string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", script)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("osascript(JXA): %w: %s", err,
			strings.TrimSpace(stderr.String()+" "+stdout.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
