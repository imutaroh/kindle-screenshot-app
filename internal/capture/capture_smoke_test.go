package capture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/imutaroh/kindle-screenshot-app/internal/frame"
)

// TestScreenshotSmoke は実際に screencapture を叩くスモークテスト。
// 画面収録権限が必要で環境に依存するため、通常の go test では実行せず、
// KINDLESNAP_SMOKE=1 を付けたときだけ動かす。
//
//	KINDLESNAP_SMOKE=1 go test ./internal/capture/ -run Screenshot -v
func TestScreenshotSmoke(t *testing.T) {
	if os.Getenv("KINDLESNAP_SMOKE") != "1" {
		t.Skip("KINDLESNAP_SMOKE=1 のときだけ実行（実際に画面をキャプチャするため）")
	}

	k := &Kindle{}
	if err := k.Discover(); err != nil {
		t.Skipf("Kindleウィンドウが見つかりませんでした（Kindle for Mac を起動して本を開いてください）: %v", err)
	}
	t.Logf("検出: %s", k.Describe())

	dir := t.TempDir()
	p1 := filepath.Join(dir, "shot1.png")
	p2 := filepath.Join(dir, "shot2.png")

	for _, p := range []string{p1, p2} {
		if err := k.Screenshot(p); err != nil {
			t.Fatalf("Screenshot(%s): %v", p, err)
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Fatalf("スクショが空です: %s (err=%v)", p, err)
		}
		t.Logf("saved %s (%d bytes)", p, st.Size())
	}

	// 実スクショで変化検出（internal/frame）のパイプラインが通ることを確認
	f1, err := frame.Load(p1, k.WindowWidth())
	if err != nil {
		t.Fatal(err)
	}
	f2, err := frame.Load(p2, k.WindowWidth())
	if err != nil {
		t.Fatal(err)
	}
	d, err := f1.Diff(f2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("連続スクショの変化画素率 = %.4f%%、完全一致 = %v（画面が静止していれば 0%%・true のはず）", d*100, f1.Same(f2))
}
