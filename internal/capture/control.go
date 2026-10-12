package capture

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// KindleBundleID は Kindle for Mac（Mac Catalyst 版）のバンドルID。
// アプリ名は世代によって "Kindle" / "Amazon Kindle" と揺れるが、バンドルIDは変わらないので
// 起動にはこちらを使う。
const KindleBundleID = "com.amazon.Lassen"

// screencaptureArgs は Kindle ウィンドウ単体を撮る screencapture の引数を作る。
//
//	-l: ウィンドウ単体を指定（背面・別ディスプレイでも撮れる）
//	-o: ウィンドウの影を付けない / -x: シャッター音を鳴らさない
//
// 撮影ループ（captureWindow）と Peek で同じ引数を使うための共通部品。
// 実行せずに引数だけをユニットテストで確かめられるよう、純粋関数にしている。
func screencaptureArgs(windowID int, path string) []string {
	return []string{"-l", strconv.Itoa(windowID), "-o", "-x", path}
}

// Peek は Kindle のウィンドウを探し直し、ウィンドウ単体を PNG として path に保存する。
// 撮ったウィンドウの情報（位置・大きさ）も返す。
//
// CLI（kindlesnap peek）から、Claude が今の Kindle の画面（ライブラリ・本の表紙など）を
// 目で見て次の操作（Click）を決めるために使う。撮影ループとは独立した1回きりの撮影なので、
// 毎回 Discover し直して最新のウィンドウ位置を返す（Click の座標変換に使えるように）。
// ターミナルに「画面収録」権限が必要。
func Peek(path string) (WindowInfo, error) {
	k := &Kindle{}
	if err := k.Discover(); err != nil {
		return WindowInfo{}, err
	}
	if err := k.captureWindow(path); err != nil {
		return WindowInfo{}, err
	}
	return k.Info(), nil
}

// Launch は Kindle をバックグラウンドで起動する（open -g: 前面に出さない）。
// 既に起動していれば何もしない（open の仕様）。起動しただけではウィンドウがすぐには
// 現れないので、呼び出し側が Discover をポーリングして待つこと。
func Launch() error {
	out, err := exec.Command("open", "-g", "-b", KindleBundleID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("Kindle の起動に失敗しました（%s）: %w: %s", KindleBundleID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// IsKindleRunning は Kindle のプロセスが動いているかを pgrep で調べる。
// ウィンドウの有無（Discover）とは別に見るのは、「起動していない」と「起動しているが
// ウィンドウが無い（閉じた・まだ出ていない）」で案内を変えたいため。
func IsKindleRunning() (bool, error) {
	for _, name := range kindleAppNames {
		err := exec.Command("pgrep", "-x", name).Run()
		if err == nil {
			return true, nil
		}
		// pgrep は「見つからない」を exit 1 で返す。それ以外は pgrep 自体の失敗
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return false, fmt.Errorf("Kindle のプロセス確認に失敗しました: %w", err)
		}
	}
	return false, nil
}

// clickScript はグローバル座標 (x, y)（ポイント単位）を左クリックする JXA を作る。
// double なら同じ位置でダブルクリックする。
//
// TurnPage と違い CGEventPostToPid ではなく CGEventPost(kCGHIDEventTap) を使う。
// プロセス直送のマウスイベントを Kindle（Mac Catalyst）が通常のクリックとして扱うかは
// 未検証なので、実際のマウス操作と同じ経路（HID イベントタップ）に流す確実な方を選んでいる。
// その代わり本物のマウスカーソルが動き、クリック先が最前面の別ウィンドウなら
// そちらがクリックされる（呼び出し側の責任で Kindle が見えている位置を指定する）。
//
// 定数は JXA の ObjC.import で列挙値が取れないことがあるので数値で埋め込む:
//
//	kCGEventLeftMouseDown = 1, kCGEventLeftMouseUp = 2, kCGEventMouseMoved = 5
//	kCGMouseButtonLeft = 0, kCGHIDEventTap = 0, kCGMouseEventClickState = 1
//
// 修飾キーのフラグは必ず 0 で上書きする。CGEventCreateMouseEvent に送信元(NULL)を渡すと、
// その瞬間に物理的に押されている修飾キー（⌘/⌥/Shift）がイベントに乗る（keyEventScript と
// 同じ罠）。⌘クリックなどになると別の動作（複数選択・新規ウィンドウ）になってしまう。
//
// ダブルクリックは down/up を2組送り、2組目のクリック回数（kCGMouseEventClickState）を 2 にする。
// macOS はこの値でシングル/ダブルクリックを区別する（時間間隔では判定しない）。
func clickScript(x, y float64, double bool) string {
	clicks := 1
	if double {
		clicks = 2
	}
	return fmt.Sprintf(`
ObjC.import("CoreGraphics");
function run() {
	const p = {x: %s, y: %s};
	const clicks = %d;
	const post = (type, clickState) => {
		const ev = $.CGEventCreateMouseEvent($(), type, p, 0);
		if (!ev) {
			throw new Error("CGEventCreateMouseEvent が失敗しました");
		}
		$.CGEventSetFlags(ev, 0);
		if (clickState > 0) {
			$.CGEventSetIntegerValueField(ev, 1, clickState);
		}
		$.CGEventPost(0, ev);
	};
	try {
		post(5, 0);
		delay(0.05);
		for (let i = 1; i <= clicks; i++) {
			post(1, i);
			delay(0.03);
			post(2, i);
			delay(0.08);
		}
		return "ok";
	} catch (e) {
		return "error: " + e;
	}
}
`, formatCoord(x), formatCoord(y), clicks)
}

// formatCoord は座標を JavaScript の数値リテラルにする（指数表記にしない）。
func formatCoord(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Click はグローバル座標 (x, y)（ポイント単位。CGWindowBounds と同じ座標系）を左クリックする。
// double なら同じ位置でダブルクリックする。本物のマウスカーソルが動く。
// ターミナルに「アクセシビリティ」権限が必要（無いとイベントが黙って捨てられる）。
//
// CLI（kindlesnap click）が、Peek で見たライブラリ画面から本を開くために使う。
// ウィンドウ内の相対座標からの変換は呼び出し側（WindowInfo の X/Y を足す）が行う。
func Click(x, y float64, double bool) error {
	if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		return fmt.Errorf("クリック座標が不正です: (%v, %v)", x, y)
	}
	out, err := runJXA(clickScript(x, y, double))
	if err != nil {
		return fmt.Errorf("クリックに失敗しました（システム設定 > プライバシーとセキュリティ > アクセシビリティ を確認）: %w", err)
	}
	if out != "ok" {
		return fmt.Errorf("クリックに失敗しました: %s", out)
	}
	return nil
}

// Rect はグローバル座標（ポイント単位。左上が原点で y は下向き。CGWindowBounds と同じ座標系）の矩形。
type Rect struct {
	X, Y, W, H float64
}

// contains は点 (x, y) が矩形の内側（端を含む）にあるか。
func (r Rect) contains(x, y float64) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}

// parkMargin はマウスを逃がすとき、Kindle のウィンドウの縁から離す距離（ポイント）。
const parkMargin = 40

// parkPoint は、Kindle のウィンドウ win の外で、どれかの画面 screens の上にある点を選ぶ。
// ウィンドウの右・左・下・上の順に、縁から parkMargin 離れた点を試す。
// どれも画面の外（ウィンドウが画面いっぱい）なら、ウィンドウに掛からない画面の隅を探す。
// 見つからなければ ok=false（全画面がウィンドウに覆われている）。
//
// 画面の上にない点へ動かすと、macOS はカーソルを最寄りの画面の縁へ寄せてしまい、
// ウィンドウの縁の上に乗って矢印が出ることがあるので、必ず画面内の点を選ぶ。
func parkPoint(win Rect, screens []Rect) (x, y float64, ok bool) {
	midX, midY := win.X+win.W/2, win.Y+win.H/2
	candidates := [][2]float64{
		{win.X + win.W + parkMargin, midY}, // 右
		{win.X - parkMargin, midY},         // 左
		{midX, win.Y + win.H + parkMargin}, // 下
		{midX, win.Y - parkMargin},         // 上
	}
	for _, s := range screens {
		const inset = 5
		candidates = append(candidates,
			[2]float64{s.X + s.W - inset, s.Y + s.H - inset}, // 右下の隅
			[2]float64{s.X + inset, s.Y + s.H - inset},       // 左下の隅
		)
	}
	for _, c := range candidates {
		if win.contains(c[0], c[1]) {
			continue
		}
		for _, s := range screens {
			if s.contains(c[0], c[1]) {
				return c[0], c[1], true
			}
		}
	}
	return 0, 0, false
}

// screensScript は全画面の矩形を JSON（[{x,y,w,h},...]）で返す JXA。
// NSScreen の frame は左下原点（y は上向き）なので、主画面（screens[0]）の高さを使って
// CG のグローバル座標（左上原点・y は下向き）に変換する。
const screensScript = `
ObjC.import("AppKit");
function run() {
	const ss = $.NSScreen.screens;
	const n = ss.count;
	if (n === 0) {
		return "[]";
	}
	const mainH = ss.objectAtIndex(0).frame.size.height;
	const out = [];
	for (let i = 0; i < n; i++) {
		const f = ss.objectAtIndex(i).frame;
		out.push({x: f.origin.x, y: mainH - (f.origin.y + f.size.height), w: f.size.width, h: f.size.height});
	}
	return JSON.stringify(out);
}
`

// Screens は接続されている全画面の矩形（グローバル座標・ポイント単位）を返す。
func Screens() ([]Rect, error) {
	out, err := runJXA(screensScript)
	if err != nil {
		return nil, fmt.Errorf("画面の一覧を取得できません: %w", err)
	}
	var raw []struct{ X, Y, W, H float64 }
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("画面の一覧を解釈できません（%q）: %w", out, err)
	}
	rs := make([]Rect, len(raw))
	for i, r := range raw {
		rs[i] = Rect{X: r.X, Y: r.Y, W: r.W, H: r.H}
	}
	return rs, nil
}

// moveScript はマウスカーソルをグローバル座標 (x, y) へ動かす JXA（クリックはしない）。
// 修飾キーのフラグを 0 にする理由と数値定数は clickScript と同じ。
func moveScript(x, y float64) string {
	return fmt.Sprintf(`
ObjC.import("CoreGraphics");
function run() {
	const ev = $.CGEventCreateMouseEvent($(), 5, {x: %s, y: %s}, 0);
	if (!ev) {
		return "error: CGEventCreateMouseEvent が失敗しました";
	}
	$.CGEventSetFlags(ev, 0);
	$.CGEventPost(0, ev);
	return "ok";
}
`, formatCoord(x), formatCoord(y))
}

// ParkMouse はマウスカーソルを Kindle のウィンドウ win の外（画面上）へ動かし、動かした先を返す。
// カーソルがウィンドウに乗っているとページ送りの矢印が表示されて写り込み、キー受付が止まる
// 原因にもなるので、click で本を開いた後・capture の前に使う。本物のマウスカーソルが動く。
func ParkMouse(win Rect) (x, y float64, err error) {
	screens, err := Screens()
	if err != nil {
		return 0, 0, err
	}
	x, y, ok := parkPoint(win, screens)
	if !ok {
		return 0, 0, errors.New("Kindle のウィンドウの外に、マウスを置ける画面上の場所がありません（ウィンドウを小さくしてください）")
	}
	out, err := runJXA(moveScript(x, y))
	if err != nil {
		return 0, 0, fmt.Errorf("マウスを動かせませんでした（システム設定 > プライバシーとセキュリティ > アクセシビリティ を確認）: %w", err)
	}
	if out != "ok" {
		return 0, 0, fmt.Errorf("マウスを動かせませんでした: %s", out)
	}
	return x, y, nil
}
