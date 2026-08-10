// Package capture は macOS 標準コマンド（screencapture / osascript）を使って
// スクリーンショット取得と Kindle のページ送りを行う。
//
// robotgo などの CGo 依存ライブラリを使わず外部コマンドに逃がすことで、
// pure Go を保ち `go build` 一発の単一バイナリにしている。
package capture

import (
	"encoding/json"
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
// 前面化に成功したアプリ名をキャッシュし、2回目以降の試行を減らす。
type Kindle struct {
	appName string // 前面化に成功したアプリ名（未確定なら空文字）
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

// mousePositionScript は現在のマウス座標を取得する JXA。
// NSEvent.mouseLocation は「主画面の左下」を原点とする Cocoa 座標系（Y軸が上向き）を返すため、
// 主画面の高さから引いて左上原点（Y軸が下向き）に変換する。
// これは Python 版が使っていた pyautogui の座標系（左上原点）に合わせるため。
// ホットコーナー判定（左上角・右上角）はこの座標系を前提にしており、変換を誤ると誤判定に直結する。
const mousePositionScript = `
ObjC.import("Cocoa");
const p = $.NSEvent.mouseLocation;
const screenHeight = $.NSScreen.mainScreen.frame.size.height;
JSON.stringify({x: p.x, y: screenHeight - p.y});
`

// screenSizeScript は主画面のサイズ（ポイント単位）を取得する JXA。
const screenSizeScript = `
ObjC.import("Cocoa");
const f = $.NSScreen.mainScreen.frame;
JSON.stringify({w: f.size.width, h: f.size.height});
`

// jxaPoint は mousePositionScript の JSON 出力を受けるための型。
type jxaPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// jxaSize は screenSizeScript の JSON 出力を受けるための型。
type jxaSize struct {
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// MousePosition は現在のマウスカーソル座標を左上原点で返す（単位: ポイント）。
// ホットコーナー（画面の角にマウスを置いてページ送りを止める等）の判定に使う想定で、
// ページ送りごとにポーリングされてもよいよう1回あたり数十〜100ms程度で完了する。
// アクセシビリティ/画面収録権限は不要（マウス位置の取得は特別な許可を要求しない）。
func MousePosition() (x, y float64, err error) {
	out, err := runJXA(mousePositionScript)
	if err != nil {
		return 0, 0, fmt.Errorf("マウス座標の取得に失敗しました: %w", err)
	}
	var p jxaPoint
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return 0, 0, fmt.Errorf("マウス座標のJSON解析に失敗しました: %w (出力: %s)", err, out)
	}
	return p.X, p.Y, nil
}

// ScreenSize は主画面のサイズ（幅・高さ、単位: ポイント）を返す。
func ScreenSize() (w, h float64, err error) {
	out, err := runJXA(screenSizeScript)
	if err != nil {
		return 0, 0, fmt.Errorf("画面サイズの取得に失敗しました: %w", err)
	}
	var s jxaSize
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return 0, 0, fmt.Errorf("画面サイズのJSON解析に失敗しました: %w (出力: %s)", err, out)
	}
	return s.W, s.H, nil
}
