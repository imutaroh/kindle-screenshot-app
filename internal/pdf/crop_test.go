package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// テスト画像はすべて合成（書籍の実画像はリポジトリに入れない）。
// ダークテーマのウィンドウを模して、黒い背景の中央に「文字の行」の横縞を描く。

// writeBookPage は w×h の黒い画像の content 内に白い横縞を描いて path に保存する。
// 画素値は位置から一意に決まるようにし（文字色を x,y で少しずつ変える）、
// 切り出し位置のずれを検出できるようにする。
func writeBookPage(t *testing.T, path string, w, h int, content image.Rectangle) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{0, 0, 0, 255}
			if (image.Point{x, y}).In(content) && ((y-content.Min.Y)%8 < 5 || y == content.Max.Y-1) {
				c = color.NRGBA{uint8(200 + x%50), uint8(200 + y%50), 230, 255}
			}
			img.SetNRGBA(x, y, c)
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

// wantRGBBytesRect は wantRGBBytes の切り出し版（汎用 At() 経由の独立した実装）。
func wantRGBBytesRect(t *testing.T, path string, r image.Rectangle) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 0, r.Dx()*r.Dy()*3)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			out = append(out, byte(cr>>8), byte(cg>>8), byte(cb>>8))
		}
	}
	return out
}

var mediaBoxRe = regexp.MustCompile(`/MediaBox \[0 0 (\d+) (\d+)\]`)

// mediaBoxes は PDF 内の各ページの MediaBox の幅・高さを出現順に返す。
func mediaBoxes(t *testing.T, pdfBytes []byte) []image.Point {
	t.Helper()
	var out []image.Point
	for _, m := range mediaBoxRe.FindAllSubmatch(pdfBytes, -1) {
		w, _ := strconv.Atoi(string(m[1]))
		h, _ := strconv.Atoi(string(m[2]))
		out = append(out, image.Pt(w, h))
	}
	return out
}

func TestCreateFromDir_Crop(t *testing.T) {
	const w, h = 300, 200
	pad := 2 // 短辺 200 の1%
	body := image.Rect(100, 40, 200, 160)

	dir := t.TempDir()
	var paths []string
	for i := 1; i <= 4; i++ {
		p := filepath.Join(dir, fmt.Sprintf("page_%04d.png", i))
		writeBookPage(t, p, w, h, body)
		paths = append(paths, p)
	}
	// 表紙だけ紙面がウィンドウの高さいっぱいまで広がるケース（Union では外れ値として切られる）
	cover := filepath.Join(dir, "page_0005.png")
	writeBookPage(t, cover, w, h, image.Rect(120, 0, 180, 200))
	paths = append(paths, cover)

	before, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}

	var progressCalls int
	res, err := CreateFromDir(dir, "本", 10, Options{Crop: true, Progress: func(done, total int) {
		progressCalls++
		if total != 5 || done < 1 || done > total {
			t.Errorf("progress(%d, %d) が範囲外", done, total)
		}
	}})
	if err != nil {
		t.Fatalf("CreateFromDir: %v", err)
	}
	if progressCalls != 5 {
		t.Errorf("progress の呼び出し回数 = %d, want 5", progressCalls)
	}
	if !res.Cropped {
		t.Fatalf("トリミングが適用されていない: note=%q", res.CropNote)
	}
	wantUnion := body.Inset(-pad)
	if res.CropRect != wantUnion {
		t.Errorf("CropRect = %v, want %v", res.CropRect, wantUnion)
	}
	if !strings.Contains(res.CropNote, "1 ページ") {
		t.Errorf("表紙を個別に広げた旨の補足が無い: %q", res.CropNote)
	}

	pdfBytes, err := os.ReadFile(res.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	// 表紙は Union と表紙自身の矩形（パディング付き・画像内に切り詰め）の外接矩形で切る
	wantCover := wantUnion.Union(image.Rect(118, 0, 182, 200))
	wantRects := []image.Rectangle{wantUnion, wantUnion, wantUnion, wantUnion, wantCover}

	boxes := mediaBoxes(t, pdfBytes)
	streams := findImageStreams(t, pdfBytes)
	if len(boxes) != 5 || len(streams) != 5 {
		t.Fatalf("ページ数 MediaBox=%d 画像=%d, want 5", len(boxes), len(streams))
	}
	for i, r := range wantRects {
		if boxes[i] != r.Size() {
			t.Errorf("page %d: MediaBox = %v, want %v", i+1, boxes[i], r.Size())
		}
		s := streams[i]
		if s.width != r.Dx() || s.height != r.Dy() {
			t.Errorf("page %d: 画像サイズ = %dx%d, want %v", i+1, s.width, s.height, r.Size())
		}
		got := inflate(t, pdfBytes[s.streamStart:s.streamStart+s.length])
		if !bytes.Equal(got, wantRGBBytesRect(t, paths[i], r)) {
			t.Errorf("page %d: 切り出した画素が元PNGの該当範囲と一致しません", i+1)
		}
	}

	// PNG 自体は書き換えない
	after, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("PNG ファイルが書き換えられた")
	}
}

func TestCreateFromDir_CropSkipped(t *testing.T) {
	t.Run("余白がほとんど無ければ切らずに警告", func(t *testing.T) {
		dir := t.TempDir()
		for i := 1; i <= 3; i++ {
			writeBookPage(t, filepath.Join(dir, fmt.Sprintf("page_%04d.png", i)), 300, 200, image.Rect(2, 2, 298, 198))
		}
		res, err := CreateFromDir(dir, "本", 10, Options{Crop: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Cropped || res.CropNote == "" {
			t.Fatalf("Cropped=%v note=%q, want 未適用＋警告", res.Cropped, res.CropNote)
		}
		pdfBytes, _ := os.ReadFile(res.Files[0])
		for i, b := range mediaBoxes(t, pdfBytes) {
			if b != image.Pt(300, 200) {
				t.Errorf("page %d: MediaBox = %v, want 元サイズ", i+1, b)
			}
		}
	})

	t.Run("全ページ白紙なら切らずに警告", func(t *testing.T) {
		dir := t.TempDir()
		for i := 1; i <= 2; i++ {
			writeBookPage(t, filepath.Join(dir, fmt.Sprintf("page_%04d.png", i)), 300, 200, image.Rectangle{})
		}
		res, err := CreateFromDir(dir, "本", 10, Options{Crop: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Cropped || res.CropNote == "" {
			t.Fatalf("Cropped=%v note=%q, want 未適用＋警告", res.Cropped, res.CropNote)
		}
	})

	t.Run("サイズの違うページは切らずに埋め込む", func(t *testing.T) {
		dir := t.TempDir()
		for i := 1; i <= 3; i++ {
			writeBookPage(t, filepath.Join(dir, fmt.Sprintf("page_%04d.png", i)), 300, 200, image.Rect(100, 40, 200, 160))
		}
		writeBookPage(t, filepath.Join(dir, "page_0004.png"), 400, 250, image.Rect(100, 40, 200, 160))
		res, err := CreateFromDir(dir, "本", 10, Options{Crop: true})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Cropped {
			t.Fatalf("多数派のサイズのページはトリミングされるべき: %q", res.CropNote)
		}
		pdfBytes, _ := os.ReadFile(res.Files[0])
		boxes := mediaBoxes(t, pdfBytes)
		if got := boxes[3]; got != image.Pt(400, 250) {
			t.Errorf("サイズ違いのページの MediaBox = %v, want 400x250", got)
		}
		if !strings.Contains(res.CropNote, "画像サイズ") {
			t.Errorf("サイズ違いの補足が無い: %q", res.CropNote)
		}
	})
}

// writeTitledPage は writeBookPage の画像の上端 title 行を、macOS のダークアピアランスの
// タイトルバーを模した一様な灰色の帯にして保存する。
func writeTitledPage(t *testing.T, path string, w, h, title int, content image.Rectangle) {
	t.Helper()
	writeBookPage(t, path, w, h, content)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	m := img.(interface {
		image.Image
		Set(x, y int, c color.Color)
	})
	for y := 0; y < title; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.NRGBA{37, 38, 38, 255})
		}
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, m); err != nil {
		t.Fatal(err)
	}
}

// TestCreateFromDir_CropKeepsTitleBarOut は、表紙の本文がタイトルバー直下から始まる本でも、
// 切り出し矩形の上側のパディングがタイトルバーの中まで広がらないことを確かめる
// （実画像では全ページの上端 14 行にタイトルバーの帯が写っていた）。
// タイトルバーが厚く検出されるページ（Page Flip のツールバー付き）が1枚あっても、
// 代表値には中央値を使うので本文ページの上端は削られない。
func TestCreateFromDir_CropKeepsTitleBarOut(t *testing.T) {
	const w, h, title = 600, 400, 28
	dir := t.TempDir()
	pages := []struct {
		title int
		body  image.Rectangle
	}{
		{title, image.Rect(150, title, 450, 380)}, // 表紙: タイトルバー直下から
		{title, image.Rect(150, 36, 450, 380)},
		{title, image.Rect(150, 36, 450, 380)},
		{title, image.Rect(150, 36, 450, 380)},
		{30, image.Rect(150, 36, 450, 380)}, // 帯がやや厚く検出されるページ
		{title, image.Rect(150, 36, 450, 380)},
	}
	for i, p := range pages {
		writeTitledPage(t, filepath.Join(dir, fmt.Sprintf("page_%04d.png", i+1)), w, h, p.title, p.body)
	}

	res, err := CreateFromDir(dir, "本", 10, Options{Crop: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Cropped {
		t.Fatalf("トリミングが適用されていない: %q", res.CropNote)
	}
	if res.CropRect.Min.Y != title {
		t.Errorf("CropRect = %v, 上端 = %d, want %d（タイトルバーの中まで広げない）", res.CropRect, res.CropRect.Min.Y, title)
	}
}
