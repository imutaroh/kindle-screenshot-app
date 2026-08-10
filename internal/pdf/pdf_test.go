package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// makeTestPNG は幅w・高さhのテスト用PNGを path に書き出す。
// 画素値は (x,y,imgIndex) から一意に決まるようにし、行/列の取り違えのような
// バグを検出しやすくする。
func makeTestPNG(t *testing.T, path string, w, h, imgIndex int) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8((x*37 + imgIndex) % 256),
				G: uint8((y*53 + imgIndex) % 256),
				B: uint8((imgIndex * 61) % 256),
				A: 255,
			})
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

// wantRGBBytes は元PNGを独立した経路（image.Image の汎用 At().RGBA()）でデコードし、
// 行優先・パディング無しの RGB バイト列を作る。writer.go の抽出ロジックと同じ手法を
// 使い回さず別実装で計算することで、両者が一致することを「本当の」無劣化検証にする。
func wantRGBBytes(t *testing.T, path string) []byte {
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
	b := img.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy()*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			out = append(out, byte(r>>8), byte(g>>8), byte(bl>>8))
		}
	}
	return out
}

// pdfImageStream は buildPDF が書き出す Image XObject のヘッダ情報とストリーム位置。
type pdfImageStream struct {
	width, height, bpc int
	colorSpace         string
	length             int
	streamStart        int
}

var imageXObjectRe = regexp.MustCompile(
	`/Type /XObject /Subtype /Image /Width (\d+) /Height (\d+) /ColorSpace /(DeviceRGB|DeviceGray) /BitsPerComponent (\d+) /Filter /FlateDecode /Length (\d+) >>\nstream\n`)

// findImageStreams は生成されたPDFバイト列から Image XObject のストリームを出現順に列挙する。
func findImageStreams(t *testing.T, pdfBytes []byte) []pdfImageStream {
	t.Helper()
	matches := imageXObjectRe.FindAllSubmatchIndex(pdfBytes, -1)
	var result []pdfImageStream
	for _, m := range matches {
		atoi := func(lo, hi int) int {
			n, err := strconv.Atoi(string(pdfBytes[lo:hi]))
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		result = append(result, pdfImageStream{
			width:       atoi(m[2], m[3]),
			height:      atoi(m[4], m[5]),
			colorSpace:  string(pdfBytes[m[6]:m[7]]),
			bpc:         atoi(m[8], m[9]),
			length:      atoi(m[10], m[11]),
			streamStart: m[1], // マッチ全体の終端 = "stream\n" の直後
		})
	}
	return result
}

func inflate(t *testing.T, compressed []byte) []byte {
	t.Helper()
	r, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCreateFromDir_LosslessAndPageCount(t *testing.T) {
	dir := t.TempDir()

	const w, h = 5, 4
	numPages := 5
	var srcPaths []string
	for i := 1; i <= numPages; i++ {
		p := filepath.Join(dir, fmt.Sprintf("page_%04d.png", i))
		makeTestPNG(t, p, w, h, i)
		srcPaths = append(srcPaths, p)
	}

	created, err := CreateFromDir(dir, "テスト/本", 2) // 2枚ごとに分割 → part1,2,3
	if err != nil {
		t.Fatalf("CreateFromDir: %v", err)
	}

	wantNames := []string{"テスト_本_part1.pdf", "テスト_本_part2.pdf", "テスト_本_part3.pdf"}
	if len(created) != len(wantNames) {
		t.Fatalf("生成PDF数 = %d, want %d (%v)", len(created), len(wantNames), created)
	}
	for i, p := range created {
		if got := filepath.Base(p); got != wantNames[i] {
			t.Errorf("created[%d] = %q, want %q", i, got, wantNames[i])
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("生成されたはずのPDFが存在しません: %v", err)
		}
	}

	// part1,2 は2ページ、part3 は1ページのはず
	wantPagesPerPart := []int{2, 2, 1}
	srcIdx := 0
	for partI, pdfPath := range created {
		pdfBytes, err := os.ReadFile(pdfPath)
		if err != nil {
			t.Fatal(err)
		}
		streams := findImageStreams(t, pdfBytes)
		if len(streams) != wantPagesPerPart[partI] {
			t.Fatalf("%s: 画像枚数 = %d, want %d", filepath.Base(pdfPath), len(streams), wantPagesPerPart[partI])
		}
		for _, s := range streams {
			// 1px = 1pt: MediaBox とページ画像サイズが元画像のピクセルサイズと一致するはず
			if s.width != w || s.height != h {
				t.Errorf("画像サイズ = %dx%d, want %dx%d", s.width, s.height, w, h)
			}
			if s.colorSpace != "DeviceRGB" || s.bpc != 8 {
				t.Errorf("colorSpace/bpc = %s/%d, want DeviceRGB/8", s.colorSpace, s.bpc)
			}

			compressed := pdfBytes[s.streamStart : s.streamStart+s.length]
			got := inflate(t, compressed)
			want := wantRGBBytes(t, srcPaths[srcIdx])
			if !bytes.Equal(got, want) {
				t.Errorf("page %d: 埋め込み画素データが元PNGと一致しません（劣化あり）", srcIdx+1)
			}
			srcIdx++
		}
	}
	if srcIdx != numPages {
		t.Fatalf("検証したページ数 = %d, want %d", srcIdx, numPages)
	}
}

func TestCreateFromDir_NoPNGs(t *testing.T) {
	dir := t.TempDir()
	created, err := CreateFromDir(dir, "空の本", 10)
	if err != nil {
		t.Fatalf("CreateFromDir: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("PNGが無いのにPDFが生成されました: %v", created)
	}
}

func TestCreateFromDir_InvalidPagesPerPDF(t *testing.T) {
	dir := t.TempDir()
	if _, err := CreateFromDir(dir, "本", 0); err == nil {
		t.Error("pagesPerPDF=0 はエラーになるべき")
	}
	if _, err := CreateFromDir(dir, "本", -1); err == nil {
		t.Error("pagesPerPDF=-1 はエラーになるべき")
	}
}

func TestDeletePNGs(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		makeTestPNG(t, filepath.Join(dir, fmt.Sprintf("page_%04d.png", i)), 2, 2, i)
	}
	// PNG以外のファイルは削除対象外
	other := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := DeletePNGs(dir)
	if err != nil {
		t.Fatalf("DeletePNGs: %v", err)
	}
	if n != 3 {
		t.Errorf("削除件数 = %d, want 3", n)
	}
	remaining, _ := listPagePNGs(dir)
	if len(remaining) != 0 {
		t.Errorf("PNGが残っています: %v", remaining)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("PNG以外のファイルまで消えました: %v", err)
	}
}

func TestGeneratePDFFilename(t *testing.T) {
	tests := []struct {
		book string
		part int
		want string
	}{
		{"普通の本", 1, "普通の本_part1.pdf"},
		{"a/b", 2, "a_b_part2.pdf"},
	}
	for _, tt := range tests {
		if got := generatePDFFilename(tt.book, tt.part); got != tt.want {
			t.Errorf("generatePDFFilename(%q, %d) = %q, want %q", tt.book, tt.part, got, tt.want)
		}
	}
}
