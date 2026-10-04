package capture

import (
	"encoding/json"
	"fmt"
)

// windowListScript は CGWindowListCopyWindowInfo を JXA 経由で叩き、
// 全ウィンドウの owner / pid / windowID / layer / bounds を JSON で返す。
//
// kCGWindowListOptionAll を使う理由: Kindle が別 Space（操作スペース）に
// あると OnScreenOnly オプションでは一覧に現れないが、screencapture -l は
// その状態でも撮れることを実機確認済み。OnScreenOnly にすると別 Space
// 運用時に発見そのものが失敗する。
//
// ObjC.castRefToObject を挟んでいるのは、挟まないと
// ObjC.deepUnwrap が undefined になる罠を実機で踏んだため。削除しない。
const windowListScript = `
ObjC.import("CoreGraphics");
const ref = $.CGWindowListCopyWindowInfo($.kCGWindowListOptionAll, $.kCGNullWindowID);
const arr = ObjC.deepUnwrap(ObjC.castRefToObject(ref));
JSON.stringify(arr.map(w => ({
  owner: w.kCGWindowOwnerName,
  pid: w.kCGWindowOwnerPID,
  windowID: w.kCGWindowNumber,
  layer: w.kCGWindowLayer,
  x: w.kCGWindowBounds.X,
  y: w.kCGWindowBounds.Y,
  width: w.kCGWindowBounds.Width,
  height: w.kCGWindowBounds.Height
})));
`

// WindowInfo は CGWindowListCopyWindowInfo から得た1ウィンドウ分の情報。
type WindowInfo struct {
	Owner    string  `json:"owner"`
	PID      int     `json:"pid"`
	WindowID int     `json:"windowID"`
	Layer    int     `json:"layer"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
}

// parseWindowList は windowListScript の標準出力（JSON文字列）をパースする。
func parseWindowList(jsonStr string) ([]WindowInfo, error) {
	var wins []WindowInfo
	if err := json.Unmarshal([]byte(jsonStr), &wins); err != nil {
		return nil, fmt.Errorf("ウィンドウ一覧のJSON解析に失敗しました: %w", err)
	}
	return wins, nil
}

// selectKindleWindow は Kindle 本体のウィンドウを選ぶ。
//
// Kindle for Mac は layer 0 に空タイトルのバー状ウィンドウ（例: 3440x30、
// 1470x33）を複数持つため、単純な「先頭 or 最初に見つかったもの」では
// バーを誤って選んでしまう。実機観測に基づき、面積最大のウィンドウを
// 本体とみなす。
//
// kCGWindowName（ウィンドウタイトル）は使わない。取得できるかどうかが
// Screen Recording 権限の状態に左右され、無いことがあるため。
func selectKindleWindow(wins []WindowInfo) (WindowInfo, bool) {
	var best WindowInfo
	found := false

	for _, w := range wins {
		if !isKindleOwner(w.Owner) {
			continue
		}
		if w.Layer != 0 {
			continue
		}

		if !found {
			best = w
			found = true
			continue
		}

		area := w.Width * w.Height
		bestArea := best.Width * best.Height
		if area > bestArea || (area == bestArea && w.WindowID < best.WindowID) {
			best = w
		}
	}

	return best, found
}

// isKindleOwner は owner 名が kindleAppNames のいずれかと完全一致するかを見る。
// 部分一致にしないのは、"Kindle Helper" のような別プロセスを誤って
// 拾わないため。
func isKindleOwner(owner string) bool {
	for _, name := range kindleAppNames {
		if owner == name {
			return true
		}
	}
	return false
}

// Discover は現在起動中の Kindle ウィンドウを探し、Kindle に記録する（常に強制実行）。
// スロットルは設けない: 発見失敗はエラーとして呼び出し側に伝播しセッションが
// 止まる設計なので、無制限な連打はそもそも起きない。「未発見なのに nil を返す」
// 早期リターンを入れると、呼び出し側が windowID=0 のまま成功と誤認するバグになる。
func (k *Kindle) Discover() error {
	out, err := runJXA(windowListScript)
	if err != nil {
		return fmt.Errorf("ウィンドウ一覧の取得に失敗しました: %w", err)
	}

	wins, err := parseWindowList(out)
	if err != nil {
		return err
	}

	w, ok := selectKindleWindow(wins)
	if !ok {
		return fmt.Errorf("Kindleアプリのウィンドウが見つかりません。Kindle for Mac を起動して本を開いてください")
	}

	k.appName = w.Owner
	k.pid = w.PID
	k.windowID = w.WindowID
	k.width = w.Width
	k.height = w.Height
	return nil
}

// ensure は windowID が未確定の場合にのみ Discover を実行する。
// 発見済みなら何もしない（後続ステップで TurnPage / Screenshot の入口に置く想定）。
func (k *Kindle) ensure() error {
	if k.windowID != 0 {
		return nil
	}
	return k.Discover()
}

// WindowWidth は発見時のウィンドウ幅をポイント単位で返す（未発見なら0）。
// screencapture -l の PNG はバッキングスケール倍の画素数で保存されるので、
// 「PNG の幅 ÷ この値」で Retina 倍率（1x / 2x）を求められる。
func (k *Kindle) WindowWidth() float64 {
	return k.width
}

// Describe は現在保持しているウィンドウ情報を人間可読な1行にする。
func (k *Kindle) Describe() string {
	if k.windowID == 0 {
		return "Kindle (未検出)"
	}
	return fmt.Sprintf("Kindle (pid=%d, window=%d, %gx%g)", k.pid, k.windowID, k.width, k.height)
}
