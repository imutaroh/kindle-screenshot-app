// Package capture は macOS 標準コマンド（screencapture / osascript）を使って
// スクリーンショット取得と Kindle のページ送りを行う。
//
// robotgo などの CGo 依存ライブラリを使わず外部コマンドに逃がすことで、
// pure Go を保ち `go build` 一発の単一バイナリにしている。
package capture

import (
	"fmt"
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

// macOS の仮想キーコード（System Events の key code に渡す値）
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

// TurnPage は矢印キーを送ってページを1枚めくる。
// System Events を使うため、ターミナルに「アクセシビリティ」権限が必要。
func (k *Kindle) TurnPage(dir Direction) error {
	code := keyCodeLeftArrow
	if dir == Right {
		code = keyCodeRightArrow
	}
	script := fmt.Sprintf(`tell application "System Events" to key code %d`, code)
	if err := runOsascript(script); err != nil {
		return fmt.Errorf("ページ送りに失敗しました（システム設定 > プライバシーとセキュリティ > アクセシビリティ を確認）: %w", err)
	}
	return nil
}

// Screenshot はメインディスプレイ全体を PNG として path に保存する。
// ターミナルに「画面収録」権限が必要。
func Screenshot(path string) error {
	// -x: シャッター音を鳴らさない / -m: メインディスプレイのみ
	out, err := exec.Command("screencapture", "-x", "-m", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("screencaptureに失敗しました（画面収録権限を確認）: %w: %s",
			err, strings.TrimSpace(string(out)))
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
