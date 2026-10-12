package frame

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// テスト画像はすべてコード内で合成する（書籍の実画像はリポジトリに入れない）。
// 実機の 1x ウィンドウ（1470×956、タイトルバー28px）の半分のサイズで、
// 縦書きページを模した「白地に黒い文字ブロックが縦に並ぶ列」を描く。
// 合成ページは 1x のウィンドウを撮ったものとして扱う（pagePt = pageW）。
const (
	pageW      = 735
	pageH      = 478
	pagePt     = pageW // ウィンドウのポイント幅（1x）
	titleBarH  = 28    // 1x のタイトルバー（ROI 上端 topChromePt=72px より内側）
	glyph      = 6     // 文字1つ分の正方形（半分サイズでの約12px相当）
	glyphGap   = 2
	colStep    = 12 // 列の間隔
	colTop     = 60
	colBottom  = 400
	firstColX  = 100
	lastColX   = 640
	titleBarGr = 0xd8
)

// newPage は合成ページを作る。skipCol に列番号を渡すとその列だけ文字を描かない
// （「本文1列だけの変化」を作るため。-1 なら全列を描く）。
func newPage(skipCol int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, pageW, pageH))
	fill(img, image.Rect(0, 0, pageW, pageH), color.NRGBA{0xff, 0xff, 0xff, 0xff})
	fill(img, image.Rect(0, 0, pageW, titleBarH), color.NRGBA{titleBarGr, titleBarGr, titleBarGr, 0xff})
	col := 0
	for x := lastColX; x >= firstColX; x -= colStep { // 縦書きなので右の列から
		if col != skipCol {
			for y := colTop; y+glyph <= colBottom; y += glyph + glyphGap {
				fill(img, image.Rect(x, y, x+glyph, y+glyph), color.NRGBA{0, 0, 0, 0xff})
			}
		}
		col++
	}
	return img
}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func mustDiff(t *testing.T, a, b Frame) float64 {
	t.Helper()
	frac, err := a.Diff(b)
	if err != nil {
		t.Fatal(err)
	}
	return frac
}

func TestIdentical(t *testing.T) {
	a := FromImage(newPage(-1), pagePt)
	b := FromImage(newPage(-1), pagePt)
	if frac := mustDiff(t, a, b); frac != 0 {
		t.Errorf("同一画像の frac = %g, want 0", frac)
	}
	if !a.Same(b) {
		t.Error("同一画像は Same() == true であるべき")
	}
}

func TestOneColumnChanged(t *testing.T) {
	a := FromImage(newPage(-1), pagePt)
	b := FromImage(newPage(10), pagePt) // 本文の1列だけ文字が消えた
	frac := mustDiff(t, a, b)
	if frac <= ChangedFrac {
		t.Errorf("1列だけの変化の frac = %g, want > ChangedFrac(%g)", frac, ChangedFrac)
	}
	if a.Same(b) {
		t.Error("1列変わった画像は Same() == false であるべき")
	}
}

func TestFewPixelNoise(t *testing.T) {
	src := newPage(-1)
	a := FromImage(src, pagePt)
	// 白地の数画素だけを黒にする（縮小で薄まりつつも、一部は pixelTol を超えうる）
	for _, p := range []image.Point{{50, 100}, {300, 200}, {301, 200}, {500, 350}, {700, 420}} {
		src.SetNRGBA(p.X, p.Y, color.NRGBA{0, 0, 0, 0xff})
	}
	b := FromImage(src, pagePt)
	if frac := mustDiff(t, a, b); frac > StableFrac {
		t.Errorf("数画素ノイズの frac = %g, want <= StableFrac(%g)", frac, StableFrac)
	}
	if a.Same(b) {
		t.Error("1画素でも違えば Same() == false であるべき（exact は縮小前で比較する）")
	}
}

func TestTitleBarOnlyChanged(t *testing.T) {
	a := FromImage(newPage(-1), pagePt)
	src := newPage(-1)
	// タイトルバー帯（ROI 外）だけを真っ黒にする（ウィンドウのアクティブ/非アクティブ切替を想定）
	fill(src, image.Rect(0, 0, pageW, titleBarH), color.NRGBA{0, 0, 0, 0xff})
	// 下端のスライダー/フッター帯（ROI 外）も変える
	fill(src, image.Rect(200, pageH-20, 500, pageH-5), color.NRGBA{0, 0, 0, 0xff})
	b := FromImage(src, pagePt)
	if frac := mustDiff(t, a, b); frac != 0 {
		t.Errorf("ROI 外だけの変化の frac = %g, want 0", frac)
	}
	if !a.Same(b) {
		t.Error("ROI 外だけの変化は Same() == true であるべき")
	}
}

func TestTransparentCorners(t *testing.T) {
	// alpha=0 の画素は RGB 値に関係なく不透明の黒として扱う
	a := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	b := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for i := range a.Pix {
		a.Pix[i] = 0x00
		b.Pix[i] = 0x00
	}
	for i := 0; i < len(b.Pix); i += 4 {
		b.Pix[i], b.Pix[i+1], b.Pix[i+2] = 0xff, 0x80, 0x10 // 見えない RGB だけが違う
	}
	fa, fb := FromImage(a, pagePt), FromImage(b, pagePt)
	if !fa.Same(fb) {
		t.Error("alpha=0 の画素は RGB 値が違っても Same() == true であるべき")
	}
	if frac := mustDiff(t, fa, fb); frac != 0 {
		t.Errorf("alpha=0 の画素同士の frac = %g, want 0", frac)
	}
	// 不透明の黒（*image.RGBA 経由）とも一致すること
	black := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 3; i < len(black.Pix); i += 4 {
		black.Pix[i] = 0xff
	}
	if !fa.Same(FromImage(black, pagePt)) {
		t.Error("alpha=0 の画素は不透明の黒と同一に扱われるべき")
	}
}

func TestSizeMismatch(t *testing.T) {
	a := FromImage(newPage(-1), pagePt)
	b := FromImage(image.NewNRGBA(image.Rect(0, 0, pageW*2, pageH)), pagePt)
	frac, err := a.Diff(b)
	if err != nil {
		t.Fatalf("サイズ違いはエラーにしない設計: err = %v", err)
	}
	if frac != 1 {
		t.Errorf("サイズ違いの frac = %g, want 1", frac)
	}
	if a.Same(b) {
		t.Error("サイズ違いは Same() == false であるべき")
	}
}

func TestDownscaleWidth(t *testing.T) {
	// 2x 相当（ROI 幅 > 512）は幅 512 に縮小、ROI 幅 < 512 は拡大しない
	big := FromImage(image.NewNRGBA(image.Rect(0, 0, 2940, 1846)), 1470)
	if big.w != targetWidth {
		t.Errorf("大きい画像の縮小後の幅 = %d, want %d", big.w, targetWidth)
	}
	small := FromImage(image.NewNRGBA(image.Rect(0, 0, 200, 100)), 200)
	if small.roiW != 188 || small.w != small.roiW { // ROI は x=6〜194 の 188px
		t.Errorf("小さい画像は拡大しない: w = %d, roiW = %d, want 188", small.w, small.roiW)
	}
}

func TestZeroFrame(t *testing.T) {
	var zero Frame
	if !zero.IsZero() {
		t.Error("ゼロ値の Frame は IsZero() == true であるべき")
	}
	if _, err := zero.Diff(zero); err == nil {
		t.Error("未計算フレームの Diff はエラーになるべき")
	}
	if zero.Same(zero) {
		t.Error("未計算フレーム同士の Same() は false であるべき")
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	src := newPage(-1)
	if err := png.Encode(f, src); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got, err := Load(path, pagePt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Same(FromImage(src, pagePt)) {
		t.Error("PNG を経由しても FromImage と同じ Frame になるべき")
	}
}

func TestLoadNotFound(t *testing.T) {
	if _, err := Load("/no/such/file.png", pagePt); err == nil {
		t.Error("存在しないファイルはエラーになるべき")
	}
}

// withToolbar は img の複製に、タイトルバー直下からマウスが乗ると出る上部ツールバー
// （高さ 40pt の濃い灰色の帯に、アイコンを模した明るい四角を並べたもの）を描いた画像を返す。
// scale はバッキングスケール（1x なら 1、2x なら 2）。
func withToolbar(img *image.NRGBA, scale int) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	copy(out.Pix, img.Pix)
	top, bottom := 28*scale, (28+40)*scale
	fill(out, image.Rect(0, top, out.Bounds().Dx(), bottom), color.NRGBA{0x20, 0x20, 0x20, 0xff})
	for x := 20 * scale; x+16*scale < out.Bounds().Dx(); x += 60 * scale {
		fill(out, image.Rect(x, top+12*scale, x+16*scale, top+28*scale), color.NRGBA{0xee, 0xee, 0xee, 0xff})
	}
	return out
}

// scaled は img を整数倍に拡大した画像を返す（2x のウィンドウを合成するため）。
func scaled(img *image.NRGBA, k int) *image.NRGBA {
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			out.SetNRGBA(x, y, img.NRGBAAt(x/k, y/k))
		}
	}
	return out
}

// TestToolbarOnlyChanged は、ウィンドウ表示（フルスクリーン以外）でマウスが乗って上部ツールバーが
// 出ただけでは「変化した」と判定しないことを確かめる。上端を比率（6%）だけで切っていたときは、
// ツールバーの下側が ROI に入り、実画像（2x・高さ1846px）で ChangedFrac の約30倍の変化になっていた
// （一時停止からの復旧時に、ページが進んでいない画面を次のページとして採用してしまう）。
func TestToolbarOnlyChanged(t *testing.T) {
	for _, tt := range []struct {
		name       string
		scale      int
		pointWidth float64
	}{
		{"1x", 1, pagePt},
		{"2x", 2, pagePt},
		{"2x・ポイント幅不明", 2, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			page := scaled(newPage(-1), tt.scale)
			a := FromImage(page, tt.pointWidth)
			b := FromImage(withToolbar(page, tt.scale), tt.pointWidth)
			if frac := mustDiff(t, a, b); frac != 0 {
				t.Errorf("ツールバーだけの変化の frac = %g, want 0（ChangedFrac=%g）", frac, ChangedFrac)
			}
			// 上端を除いても、本文の変化は従来どおり検出できること
			c := FromImage(scaled(newPage(10), tt.scale), tt.pointWidth)
			if frac := mustDiff(t, a, c); frac <= ChangedFrac {
				t.Errorf("1列だけの変化の frac = %g, want > ChangedFrac(%g)", frac, ChangedFrac)
			}
		})
	}
}

func TestTopCut(t *testing.T) {
	for _, tt := range []struct {
		name       string
		w, h       int
		pointWidth float64
		want       int
	}{
		{"1x の標準的なウィンドウ", 1470, 923, 1470, 72},
		{"2x の標準的なウィンドウ", 2940, 1846, 1470, 144},
		{"縦に大きいウィンドウは比率が勝つ", 1720, 1410 * 2, 1720, 169}, // 2820*0.06
		{"ポイント幅不明は 2x を仮定", 1470, 923, 0, 144},
		{"小さいウィンドウは maxTopFrac が上限", 400, 200, 400, 50},
		{"記録より縮んだウィンドウ（倍率1未満）は 1x 扱い", 700, 923, 1470, 72},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := topCut(image.Rect(0, 0, tt.w, tt.h), tt.pointWidth); got != tt.want {
				t.Errorf("topCut = %d, want %d", got, tt.want)
			}
		})
	}
}
