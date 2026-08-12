package session

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/imutaroh/kindle-screenshot-app/internal/dedupe"
)

// writeTestPNG はテスト用の PNG を生成する。internal/dedupe/dedupe_test.go の
// writeTestPNG と同じ作り方（pHash は「低周波成分（大まかな構図）」を比較するため、
// テスト画像は細かい模様ではなく構図レベルで変える）。
func writeTestPNG(t *testing.T, path string, pattern func(x, y int) color.Color) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, pattern(x, y))
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// leftHalf は左半分が黒いページ
func leftHalf(x, y int) color.Color {
	if x < 32 {
		return color.Black
	}
	return color.White
}

// diagonal は対角線で塗り分けたページ（leftHalf との pHash 距離は 29 で、
// DefaultDuplicateThreshold(5) を大きく超える構図の異なる別ページ）
func diagonal(x, y int) color.Color {
	if x > y {
		return color.Black
	}
	return color.White
}

// hashOf はテスト用パターンから PNG を書き出して pHash を計算するヘルパー。
// 同じ絵でも別ファイルとして書き出すことで、実際のポーリング（撮るたびに上書き）に近い形にする。
func hashOf(t *testing.T, dir, name string, pattern func(x, y int) color.Color) dedupe.Hash {
	t.Helper()
	path := filepath.Join(dir, name+".png")
	writeTestPNG(t, path, pattern)
	h, err := dedupe.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestShouldAccept(t *testing.T) {
	dir := t.TempDir()
	left := hashOf(t, dir, "left", leftHalf)
	leftAgain := hashOf(t, dir, "left-again", leftHalf) // leftと同一構図（距離0）
	diag := hashOf(t, dir, "diag", diagonal)
	diagAgain := hashOf(t, dir, "diag-again", diagonal) // diagと同一構図（距離0）
	var zero dedupe.Hash

	t.Run("前ページと同じままなら採用しない", func(t *testing.T) {
		// current は prevPageHash(left) と同一構図 → changed=false。
		// lastPoll も同一構図で「安定」はしているが、変化していない以上は不採用。
		if shouldAccept(leftAgain, left, leftAgain, DefaultDuplicateThreshold, DefaultStableThreshold) {
			t.Error("前ページと同じ画像は採用されるべきではない")
		}
	})

	t.Run("変化したが直前ポーリングと違う（まだ描画中）なら採用しない", func(t *testing.T) {
		// current(diag) は prevPageHash(left) と別構図 → changed=true。
		// だが lastPoll(left) とは別構図で「安定」していない → 不採用。
		if shouldAccept(diag, left, left, DefaultDuplicateThreshold, DefaultStableThreshold) {
			t.Error("描画途中（直前ポーリングと異なる）の画像は採用されるべきではない")
		}
	})

	t.Run("変化して直前ポーリングと同じなら採用する", func(t *testing.T) {
		// current(diag) は prevPageHash(left) と別構図 → changed=true。
		// lastPoll(diagAgain) とは同一構図 → stable=true → 採用。
		if !shouldAccept(diag, left, diagAgain, DefaultDuplicateThreshold, DefaultStableThreshold) {
			t.Error("変化して安定した画像は採用されるべき")
		}
	})

	t.Run("1ページ目（prev未設定）は安定していれば採用する", func(t *testing.T) {
		// prevPageHash が未計算（IsZero）→ changed は無条件 true。
		// lastPoll(diagAgain) と同一構図 → stable=true → 採用。
		if !shouldAccept(diag, zero, diagAgain, DefaultDuplicateThreshold, DefaultStableThreshold) {
			t.Error("1ページ目は安定していれば採用されるべき")
		}
	})

	t.Run("1ページ目でも最初のポーリング（lastPoll未設定）なら採用しない", func(t *testing.T) {
		// prevPageHash・lastPoll 両方未計算 → 比較対象がなく不採用（少なくとも2回のポーリングが必要）。
		if shouldAccept(diag, zero, zero, DefaultDuplicateThreshold, DefaultStableThreshold) {
			t.Error("直前ポーリングが未計算の1回目は採用されるべきではない")
		}
	})
}
