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

// cornerStateScript は現在のマウス座標と主画面サイズをまとめて取得する JXA。
// NSEvent.mouseLocation は「主画面の左下」を原点とする Cocoa 座標系（Y軸が上向き）を返すため、
// 主画面の高さから引いて左上原点（Y軸が下向き）に変換する。
// これは Python 版が使っていた pyautogui の座標系（左上原点）に合わせるため。
// ホットコーナー判定（左上角・右上角）はこの座標系を前提にしており、変換を誤ると誤判定に直結する。
//
// マウス座標と画面サイズを別々の osascript 呼び出し（旧 mousePositionScript / screenSizeScript）
// に分けていたが、osascript の起動コストが1回あたり実測0.11〜0.12秒と大きく、適応待機の
// ポーリングごとに checkCornerAction が呼ばれる構成では2回呼ぶと無視できないオーバーヘッドに
// なる（0.22〜0.24秒/回）。1回の JXA 呼び出しで両方読むことでコストを半減させる。
const cornerStateScript = `
ObjC.import("Cocoa");
const p = $.NSEvent.mouseLocation;
const f = $.NSScreen.mainScreen.frame;
JSON.stringify({x: p.x, y: f.size.height - p.y, w: f.size.width, h: f.size.height});
`

// jxaCornerState は cornerStateScript の JSON 出力を受けるための型。
type jxaCornerState struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// CornerState は現在のマウスカーソル座標（左上原点、単位: ポイント）と主画面サイズ
// （幅・高さ、単位: ポイント）を1回の osascript 呼び出しでまとめて返す。
// ホットコーナー（画面の角にマウスを置いてページ送りを止める等）の判定に使う想定で、
// 適応待機のポーリングごとに呼ばれてもよいよう1回あたり実測0.11〜0.12秒程度で完了する。
// アクセシビリティ/画面収録権限は不要（マウス位置・画面サイズの取得は特別な許可を要求しない）。
func CornerState() (x, y, w, h float64, err error) {
	out, err := runJXA(cornerStateScript)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("マウス座標・画面サイズの取得に失敗しました: %w", err)
	}
	var s jxaCornerState
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("マウス座標・画面サイズのJSON解析に失敗しました: %w (出力: %s)", err, out)
	}
	return s.X, s.Y, s.W, s.H, nil
}
