package capture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/imutaroh/kindle-screenshot-app/internal/dedupe"
)

// TestScreenshotSmoke は実際に screencapture を叩くスモークテスト。
// 画面収録権限が必要で環境に依存するため、通常の go test では実行せず、
// KINDLESNAP_SMOKE=1 を付けたときだけ動かす。
//
//	KINDLESNAP_SMOKE=1 go test ./internal/capture/ -run Smoke -v
func TestScreenshotSmoke(t *testing.T) {
	if os.Getenv("KINDLESNAP_SMOKE") != "1" {
		t.Skip("KINDLESNAP_SMOKE=1 のときだけ実行（実際に画面をキャプチャするため）")
	}

	dir := t.TempDir()
	p1 := filepath.Join(dir, "shot1.png")
	p2 := filepath.Join(dir, "shot2.png")

	for _, p := range []string{p1, p2} {
		if err := Screenshot(p); err != nil {
			t.Fatalf("Screenshot(%s): %v", p, err)
		}
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Fatalf("スクショが空です: %s (err=%v)", p, err)
		}
		t.Logf("saved %s (%d bytes)", p, st.Size())
	}

	// 実スクショで pHash パイプラインが通ることを確認
	h1, err := dedupe.HashFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := dedupe.HashFile(p2)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h1.Distance(h2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("連続スクショの pHash 距離 = %d（画面が変わっていなければ閾値5以下のはず）", d)
}
