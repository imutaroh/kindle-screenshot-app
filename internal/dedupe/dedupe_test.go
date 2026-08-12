package dedupe

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeTestPNG はテスト用の PNG を生成する。
// pattern に応じてピクセルを塗り分け、「同じ絵」「違う絵」を作る。
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

// 注意: pHash は画像の「低周波成分（大まかな構図）」を比較するため、
// 細かい模様の違いは同一と判定される。テスト画像は構図レベルで変える。

// leftHalf は左半分が黒いページ
func leftHalf(x, y int) color.Color {
	if x < 32 {
		return color.Black
	}
	return color.White
}

// diagonal は対角線で塗り分けたページ（構図が大きく異なる別ページ。
// leftHalf との pHash 距離は 29 で、閾値 5 を大きく超える）
func diagonal(x, y int) color.Color {
	if x > y {
		return color.Black
	}
	return color.White
}

func TestHashFileAndDistance(t *testing.T) {
	dir := t.TempDir()
	same1 := filepath.Join(dir, "same1.png")
	same2 := filepath.Join(dir, "same2.png")
	diff := filepath.Join(dir, "diff.png")

	writeTestPNG(t, same1, leftHalf)
	writeTestPNG(t, same2, leftHalf)
	writeTestPNG(t, diff, diagonal)

	h1, err := HashFile(same1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashFile(same2)
	if err != nil {
		t.Fatal(err)
	}
	h3, err := HashFile(diff)
	if err != nil {
		t.Fatal(err)
	}

	// 同じ絵 → 距離 0
	if d, err := h1.Distance(h2); err != nil || d != 0 {
		t.Errorf("同一画像の距離 = %d (err=%v), want 0", d, err)
	}

	// 違う絵 → 閾値5より十分大きい距離になるはず
	if d, err := h1.Distance(h3); err != nil || d <= 5 {
		t.Errorf("異なる画像の距離 = %d (err=%v), want > 5", d, err)
	}
}

func TestZeroHash(t *testing.T) {
	var zero Hash
	if !zero.IsZero() {
		t.Error("ゼロ値の Hash は IsZero() == true であるべき")
	}
	if _, err := zero.Distance(zero); err == nil {
		t.Error("未計算ハッシュの比較はエラーになるべき")
	}
}

func TestHashFileNotFound(t *testing.T) {
	if _, err := HashFile("/no/such/file.png"); err == nil {
		t.Error("存在しないファイルはエラーになるべき")
	}
}
