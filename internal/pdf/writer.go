package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/png"
	"os"
)

// imageData は1ページ分の画像を、PDFに埋め込める形（非圧縮のコンポーネントバイト列）に
// デコードした結果を保持する。
type imageData struct {
	width, height int
	bitsPerComp   int    // 8 or 16
	colorSpace    string // "DeviceGray" or "DeviceRGB"
	raw           []byte // 行優先（先頭行が画像の一番上）、パディング無しの生データ
}

// decodePNGForPDF は PNG ファイルをデコードし、PDF の Image XObject に
// そのまま（画素値を変換せず）埋め込める生データへ変換する。
//
// Go の image/png デコーダは、PNG のカラータイプに応じて具体的な画像型
// （*image.NRGBA / *image.RGBA / *image.Gray など）を返す。それぞれの Pix を
// 直接読むことで、色変換による誤差を一切挟まずに元のバイト値を取り出せる。
// screencapture が出力する画面全体のスクリーンショットは不透明（alpha=255固定）
// であることを前提に、アルファチャンネルは保持せず RGB のみを埋め込む
// （透過画像を想定した用途ではない）。
func decodePNGForPDF(path string) (imageData, error) {
	f, err := os.Open(path)
	if err != nil {
		return imageData{}, err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return imageData{}, fmt.Errorf("PNGデコード失敗 %s: %w", path, err)
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	switch px := img.(type) {
	case *image.NRGBA:
		return imageData{w, h, 8, "DeviceRGB", extractRGB8(w, h, px.Pix, px.Stride, 4)}, nil
	case *image.RGBA:
		// screencapture の出力は不透明なので premultiplied でも非 premultiplied でも
		// RGB値は一致する（alpha=255のとき premultiply は恒等変換）。
		return imageData{w, h, 8, "DeviceRGB", extractRGB8(w, h, px.Pix, px.Stride, 4)}, nil
	case *image.Gray:
		return imageData{w, h, 8, "DeviceGray", extractPacked(w, h, px.Pix, px.Stride, 1)}, nil
	case *image.Gray16:
		return imageData{w, h, 16, "DeviceGray", extractPacked(w, h, px.Pix, px.Stride, 2)}, nil
	case *image.NRGBA64:
		return imageData{w, h, 16, "DeviceRGB", extractRGB16(w, h, px.Pix, px.Stride, 8)}, nil
	case *image.RGBA64:
		return imageData{w, h, 16, "DeviceRGB", extractRGB16(w, h, px.Pix, px.Stride, 8)}, nil
	case *image.Paletted:
		return imageData{w, h, 8, "DeviceRGB", extractPaletted(px)}, nil
	default:
		// 上記以外の稀な型のフォールバック。8bit画像由来であれば
		// color.Color.RGBA() の16bit値を8bit分右シフトして正確に復元できる
		// （8bitの値cはGoの内部表現でc<<8|cに複製されるため、>>8で元に戻る）。
		return imageData{w, h, 8, "DeviceRGB", extractRGBFallback(img)}, nil
	}
}

// extractRGB8 は 8bit/channel で R,G,B,A の順に並ぶ Pix から、A を除いた RGB のみを
// 行優先・パディング無しで取り出す。
func extractRGB8(w, h int, pix []byte, stride, channels int) []byte {
	out := make([]byte, 0, w*h*3)
	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*channels]
		for x := 0; x < w; x++ {
			i := x * channels
			out = append(out, row[i], row[i+1], row[i+2])
		}
	}
	return out
}

// extractRGB16 は 16bit/channel（ビッグエンディアン、PNG準拠）で R,G,B,A の順に並ぶ Pix から、
// A を除いた RGB のみを行優先・パディング無しで取り出す。
func extractRGB16(w, h int, pix []byte, stride, channels int) []byte {
	out := make([]byte, 0, w*h*6)
	for y := 0; y < h; y++ {
		row := pix[y*stride : y*stride+w*channels]
		for x := 0; x < w; x++ {
			i := x * channels
			out = append(out, row[i:i+6]...)
		}
	}
	return out
}

// extractPacked は1画素あたり bytesPerPixel バイトで詰まっている Pix（Gray/Gray16 など）を
// 行優先・パディング無しで取り出す。
func extractPacked(w, h int, pix []byte, stride, bytesPerPixel int) []byte {
	out := make([]byte, 0, w*h*bytesPerPixel)
	rowBytes := w * bytesPerPixel
	for y := 0; y < h; y++ {
		out = append(out, pix[y*stride:y*stride+rowBytes]...)
	}
	return out
}

// extractPaletted はパレット画像を、パレット参照で RGB に変換して取り出す。
// PNG の PLTE は8bit固定のため、color.Color.RGBA() の16bit値を8bit分右シフトすれば
// 元の8bit値に一致する（極稀な非8bitパレット実装があっても丸めは発生しない）。
func extractPaletted(px *image.Paletted) []byte {
	w, h := px.Bounds().Dx(), px.Bounds().Dy()
	out := make([]byte, 0, w*h*3)
	for y := 0; y < h; y++ {
		row := px.Pix[y*px.Stride : y*px.Stride+w]
		for _, idx := range row {
			r, g, b, _ := px.Palette[idx].RGBA()
			out = append(out, byte(r>>8), byte(g>>8), byte(b>>8))
		}
	}
	return out
}

// extractRGBFallback は image.Image の一般インタフェースだけを使う最終手段の経路。
func extractRGBFallback(img image.Image) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, w*h*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			out = append(out, byte(r>>8), byte(g>>8), byte(bl>>8))
		}
	}
	return out
}

// flateCompress は PDF の /FilterFlateDecode（zlib形式, RFC1950）でデータを圧縮する。
// 可逆圧縮であり画素値の劣化は発生しない。
func flateCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildPDF は images の各要素を1ページずつに割り当てた PDF のバイト列を組み立てる。
// 各ページのサイズは画像のピクセルサイズと同じにする（1px = 1pt）ため、
// 表示側で拡大縮小されず無劣化のまま埋め込める。
//
// 生成する PDF は単純な構成（Catalog → Pages → Page/Contents/Image の3つ組の繰り返し）
// で、外部ライブラリを使わず標準ライブラリの image/png・compress/zlib のみで完結する。
func buildPDF(images []imageData) ([]byte, error) {
	if len(images) == 0 {
		return nil, fmt.Errorf("画像が1枚もありません")
	}

	var buf bytes.Buffer
	var offsets []int // offsets[i] = オブジェクト番号 i+1 の "N 0 obj" 開始位置

	buf.WriteString("%PDF-1.4\n")

	startObj := func() int {
		offsets = append(offsets, buf.Len())
		n := len(offsets)
		fmt.Fprintf(&buf, "%d 0 obj\n", n)
		return n
	}
	endObj := func() {
		buf.WriteString("\nendobj\n")
	}

	// obj 1: Catalog
	catalogNum := startObj()
	buf.WriteString("<< /Type /Catalog /Pages 2 0 R >>")
	endObj()

	// obj 2: Pages（各ページのオブジェクト番号は 3 + i*3 で静的に決まる）
	kids := make([]int, len(images))
	for i := range images {
		kids[i] = 3 + i*3
	}
	pagesNum := startObj()
	buf.WriteString("<< /Type /Pages /Kids [")
	for i, k := range kids {
		if i > 0 {
			buf.WriteString(" ")
		}
		fmt.Fprintf(&buf, "%d 0 R", k)
	}
	fmt.Fprintf(&buf, "] /Count %d >>", len(images))
	endObj()
	if pagesNum != 2 {
		return nil, fmt.Errorf("内部エラー: Pagesオブジェクト番号が想定外です: %d", pagesNum)
	}

	for i, img := range images {
		pageObj := 3 + i*3
		contentObj := pageObj + 1
		imageObj := pageObj + 2

		// Page
		gotPageObj := startObj()
		fmt.Fprintf(&buf,
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			img.width, img.height, imageObj, contentObj)
		endObj()
		if gotPageObj != pageObj {
			return nil, fmt.Errorf("内部エラー: Pageオブジェクト番号が想定外です: got=%d want=%d", gotPageObj, pageObj)
		}

		// Contents（ページ全面に画像を1枚描画するだけの内容ストリーム）
		content := []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", img.width, img.height))
		gotContentObj := startObj()
		fmt.Fprintf(&buf, "<< /Length %d >>\nstream\n", len(content))
		buf.Write(content)
		buf.WriteString("\nendstream")
		endObj()
		if gotContentObj != contentObj {
			return nil, fmt.Errorf("内部エラー: Contentsオブジェクト番号が想定外です: got=%d want=%d", gotContentObj, contentObj)
		}

		// Image XObject（Flate圧縮のみで再エンコードは行わない＝無劣化）
		compressed, err := flateCompress(img.raw)
		if err != nil {
			return nil, fmt.Errorf("画像ストリームの圧縮に失敗しました: %w", err)
		}
		gotImageObj := startObj()
		fmt.Fprintf(&buf,
			"<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /%s /BitsPerComponent %d /Filter /FlateDecode /Length %d >>\nstream\n",
			img.width, img.height, img.colorSpace, img.bitsPerComp, len(compressed))
		buf.Write(compressed)
		buf.WriteString("\nendstream")
		endObj()
		if gotImageObj != imageObj {
			return nil, fmt.Errorf("内部エラー: Imageオブジェクト番号が想定外です: got=%d want=%d", gotImageObj, imageObj)
		}
	}

	// xref テーブル（各エントリはPDF仕様どおり20バイト固定長）
	xrefOffset := buf.Len()
	total := len(offsets) + 1 // オブジェクト0（フリーリストの先頭）を含む
	fmt.Fprintf(&buf, "xref\n0 %d\n", total)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF", total, catalogNum, xrefOffset)

	return buf.Bytes(), nil
}
