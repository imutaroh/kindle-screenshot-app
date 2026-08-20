// Package capture は macOS 標準コマンド（screencapture / osascript）を使って
// スクリーンショット取得と Kindle のページ送りを行う。
//
// robotgo などの CGo 依存ライブラリを使わず外部コマンドに逃がすことで、
// pure Go を保ち `go build` 一発の単一バイナリにしている。
package capture

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
// 前面化に成功したアプリ名や、ウィンドウ発見（Discover）で得た情報をキャッシュし、
// 2回目以降の試行を減らす。
type Kindle struct {
	appName  string  // 前面化に成功した／発見できたアプリ名（未確定なら空文字）
	pid      int     // Kindle アプリのプロセスID（0 = 未発見）
	windowID int     // キャプチャ対象のウィンドウID（0 = 未発見）
	width    float64 // 発見時のウィンドウ幅（ログ・診断用）
	height   float64 // 発見時のウィンドウ高さ（ログ・診断用）

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

// Activate は Kindle アプリを前面に出す。
func (k *Kindle) Activate() error {
	candidates := kindleAppNames
	if k.appName != "" {
		candidates = []string{k.appName}
	}

	var lastErr error
	for _, name := range candidates {
		script := fmt.Sprintf("tell application %q to activate", name)
		if err := runOsascript(script); err != nil {
			lastErr = err
			continue
		}
		k.appName = name
		time.Sleep(500 * time.Millisecond) // 前面に切り替わるのを待つ
		return nil
	}
	return fmt.Errorf("Kindleアプリを前面に出せませんでした: %w", lastErr)
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
const keyEventScript = `
ObjC.import("CoreGraphics");
const pid = %d;
const keyCode = %d;
const down = $.CGEventCreateKeyboardEvent($(), keyCode, true);
const up = $.CGEventCreateKeyboardEvent($(), keyCode, false);
$.CGEventPostToPid(pid, down);
delay(0.03);
$.CGEventPostToPid(pid, up);
"ok";
`

// TurnPage は矢印キーイベントを Kindle のプロセスへ直送してページを1枚めくる。
// 前面化不要（Kindle がバックグラウンド・別ディスプレイ・別 Space でも動く）。
// ターミナルに「アクセシビリティ」権限が必要。
func (k *Kindle) TurnPage(dir Direction) error {
	if err := k.ensure(); err != nil {
		return err
	}
	code := keyCodeLeftArrow
	if dir == Right {
		code = keyCodeRightArrow
	}
	out, err := runJXA(fmt.Sprintf(keyEventScript, k.pid, code))
	if err != nil {
		return fmt.Errorf("ページ送りに失敗しました（システム設定 > プライバシーとセキュリティ > アクセシビリティ を確認）: %w", err)
	}
	if out != "ok" {
		return fmt.Errorf("ページ送りスクリプトの出力が想定外です: %q", out)
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
	// して古い PNG が残っていると、pHash が古い画像を読んで「描画が安定した」
	// と誤判定し、同一ページを誤って採用してしまう。事前削除 + 撮影後の存在
	// 検証が、適応待機の正しさ（＝古い画像を絶対に読ませない）の前提になる。
	os.Remove(path)

	// -l: ウィンドウ単体を指定（背面・別ディスプレイでも撮れる）
	// -o: ウィンドウの影を付けない / -x: シャッター音を鳴らさない
	out, err := exec.Command("screencapture", "-l", strconv.Itoa(k.windowID), "-o", "-x", path).CombinedOutput()
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

// runOsascript は AppleScript を1つ実行し、失敗時は stderr を含むエラーを返す。
func runOsascript(script string) error {
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runJXA は JXA（JavaScript for Automation）を1つ実行し、標準出力の文字列を返す。
// AppleScript ではなく JXA を使うのは、Cocoa（NSEvent / NSScreen）の値を
// JSON.stringify でそのまま取り出せて構文が単純になるため。
func runJXA(script string) (string, error) {
	out, err := exec.Command("osascript", "-l", "JavaScript", "-e", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("osascript(JXA): %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
