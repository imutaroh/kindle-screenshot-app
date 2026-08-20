package capture

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/dedupe"
)

// TestTurnPageSmoke は CGEventPostToPid によるバックグラウンドページ送りの
// スモークテスト。実際に Kindle の読書位置を動かすため、通常のスモーク
// （KINDLESNAP_SMOKE）とは別の環境変数 KINDLESNAP_SMOKE_KEYS=1 で二重ガードする。
//
//	KINDLESNAP_SMOKE_KEYS=1 go test ./internal/capture/ -run TurnPage -v
//
// Kindle for Mac が起動していて本が開いている状態で実行すること。
// 前後のウィンドウ単体スクショの pHash 距離で「フォーカスなしでページが
// 進んだ」ことを自動判定し、最後に逆方向へ1ページ戻して読書位置を復元する。
func TestTurnPageSmoke(t *testing.T) {
	if os.Getenv("KINDLESNAP_SMOKE_KEYS") != "1" {
		t.Skip("KINDLESNAP_SMOKE_KEYS=1 のときだけ実行（Kindleの読書位置を実際に動かすため）")
	}

	k := &Kindle{}
	if err := k.Discover(); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	t.Logf("検出: %s", k.Describe())

	dir := t.TempDir()
	before := filepath.Join(dir, "before.png")
	after := filepath.Join(dir, "after.png")

	if err := k.Screenshot(before); err != nil {
		t.Fatalf("ページ送り前のキャプチャ: %v", err)
	}

	if err := k.TurnPage(Left); err != nil {
		t.Fatalf("TurnPage(Left): %v", err)
	}

	// ページめくりアニメーション中はウィンドウが一時的にキャプチャ不能になる
	// ことがある（実機観測）ため、少し待ってからリトライ付きで撮る。
	time.Sleep(1 * time.Second)
	var capErr error
	for i := 0; i < 5; i++ {
		if capErr = k.Screenshot(after); capErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if capErr != nil {
		t.Fatalf("ページ送り後のキャプチャ: %v", capErr)
	}

	// 読書位置を元に戻す（失敗しても検証には影響しないのでログのみ）
	defer func() {
		if err := k.TurnPage(Right); err != nil {
			t.Logf("読書位置の復元に失敗（手動で戻してください）: %v", err)
		}
	}()

	h1, err := dedupe.HashFile(before)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := dedupe.HashFile(after)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h1.Distance(h2)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ページ送り前後の pHash 距離 = %d", d)
	if d <= 5 {
		t.Errorf("ページが進んでいない可能性があります（pHash 距離 %d <= 5）", d)
	}
}
