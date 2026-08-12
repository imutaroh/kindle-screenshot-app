package capture

import (
	"os"
	"testing"
)

// TestMouseAndScreenSmoke は実際に osascript(JXA) を叩いて
// マウス座標・画面サイズを取得するスモークテスト。
// 実行環境（ディスプレイ構成）に依存するため、通常の go test では実行せず、
// KINDLESNAP_SMOKE=1 を付けたときだけ動かす（capture_smoke_test.go と同じ流儀）。
//
//	KINDLESNAP_SMOKE=1 go test ./internal/capture/ -run MouseAndScreenSmoke -v
func TestMouseAndScreenSmoke(t *testing.T) {
	if os.Getenv("KINDLESNAP_SMOKE") != "1" {
		t.Skip("KINDLESNAP_SMOKE=1 のときだけ実行（実際に osascript を叩くため）")
	}

	x, y, w, h, err := CornerState()
	if err != nil {
		t.Fatalf("CornerState: %v", err)
	}
	if w <= 0 || h <= 0 {
		t.Fatalf("画面サイズが不正です: w=%v h=%v", w, h)
	}
	t.Logf("screen size = %vx%v", w, h)
	t.Logf("mouse position (top-left origin) = (%v, %v)", x, y)

	// シングルディスプレイなら主画面の範囲内に収まるはず。
	// マルチディスプレイでマウスが NSScreen.mainScreen 以外の画面にある場合は
	// 座標が主画面の範囲外（負値や幅超え）になりうるため、そのケースは失敗にせず
	// 警告ログに留める（値そのものが取得できていることの確認が本テストの主眼）。
	if x < 0 || x > w || y < 0 || y > h {
		t.Logf("警告: マウス座標が主画面のサイズ内に収まっていません（マルチディスプレイでマウスが副画面にある可能性）: pos=(%v,%v) screen=%vx%v", x, y, w, h)
	}
}
