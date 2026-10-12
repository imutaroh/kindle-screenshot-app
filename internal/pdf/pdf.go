// Package pdf は連番PNG画像をページ単位で結合し、PDFファイルとして出力する。
// Python 版の src/pdf_generator.py（img2pdf 利用）の Go 移植。
//
// img2pdf 相当の外部ライブラリ（pdfcpu 等）を追加する代わりに、標準ライブラリの
// image/png・compress/zlib だけで完結する最小の PDF ライターを自前で持つ。
// img2pdf がやっていること自体は「画像をデコードし、劣化しないフィルタ
// （FlateDecode）でページに埋め込む」という小さな処理であり、依存を増やさず
// かつ無劣化性を自前のテストで直接検証できるメリットを優先した。
package pdf

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"

	"github.com/imutaroh/kindle-screenshot-app/internal/output"
)

// Options は CreateFromDir の動作オプション。
type Options struct {
	// Crop が true なら、全ページ共通の余白トリミング（internal/crop）を適用してから埋め込む。
	// PNG ファイル自体は書き換えない（PDF に埋め込む範囲を変えるだけ）。
	Crop bool

	// Progress は余白検出（全ページを1回ずつデコードするので時間がかかる）の進捗通知。
	// nil なら通知しない。複数の goroutine から呼ばれることはない。
	Progress func(done, total int)
}

// Result は CreateFromDir の結果。
type Result struct {
	// Files は作成した PDF のパス（作成順）。
	Files []string

	// Cropped はトリミングを適用したか。
	Cropped bool
	// CropRect は全ページ共通の切り出し矩形（Cropped=false なら空）。
	CropRect image.Rectangle
	// Bounds は CropRect の基準にしたページの画像サイズ。
	Bounds image.Rectangle
	// CropNote は Crop を指定したのに適用しなかった理由、または適用時の補足
	// （サイズ違いのページ・個別に広げたページの数など）。空なら補足なし。
	CropNote string
}

// CreateFromDir は dir 内の page_*.png を番号順に取得し、pagesPerPDF 枚ごとに
// <bookName>_part1.pdf, _part2.pdf ... として dir 直下に作成する。
//
// 各ページのサイズは埋め込む画像のピクセルサイズと同じ（1px=1pt）にし、PNGは再エンコード
// せず Flate（可逆圧縮）でそのまま埋め込むため画質の劣化は発生しない。opts.Crop のときは
// 切り出した範囲の画素だけを埋め込み、MediaBox も切り出し後のサイズになる。
func CreateFromDir(dir, bookName string, pagesPerPDF int, opts Options) (Result, error) {
	var res Result
	if pagesPerPDF <= 0 {
		return res, fmt.Errorf("pagesPerPDFは1以上を指定してください（指定値: %d）", pagesPerPDF)
	}

	pngs, err := listPagePNGs(dir)
	if err != nil {
		return res, err
	}
	if len(pngs) == 0 {
		return res, nil
	}

	// rects[i] はページ i を埋め込むときの切り出し矩形（空ならページ全体）
	rects := make([]image.Rectangle, len(pngs))
	if opts.Crop {
		plan, err := planCrop(pngs, opts.Progress)
		if err != nil {
			return res, err
		}
		rects = plan.rects
		res.Cropped = plan.applied
		res.CropRect = plan.union
		res.Bounds = plan.bounds
		res.CropNote = plan.note
	}

	part := 1
	for i := 0; i < len(pngs); i += pagesPerPDF {
		end := min(i+pagesPerPDF, len(pngs))

		images := make([]imageData, 0, end-i)
		for j := i; j < end; j++ {
			img, err := decodePNGForPDF(pngs[j], rects[j])
			if err != nil {
				return res, fmt.Errorf("part %d: %w", part, err)
			}
			images = append(images, img)
		}

		pdfBytes, err := buildPDF(images)
		if err != nil {
			return res, fmt.Errorf("part %d のPDF生成に失敗しました: %w", part, err)
		}

		outPath := filepath.Join(dir, generatePDFFilename(bookName, part))
		if err := os.WriteFile(outPath, pdfBytes, 0o644); err != nil {
			return res, fmt.Errorf("part %d の書き込みに失敗しました: %w", part, err)
		}

		res.Files = append(res.Files, outPath)
		part++
	}

	return res, nil
}

// DeletePNGs は dir 内の page_*.png をすべて削除し、削除できた件数を返す。
// 途中でエラーが発生しても他のファイルの削除は続け、発生したエラーはまとめて返す。
func DeletePNGs(dir string) (int, error) {
	pngs, err := listPagePNGs(dir)
	if err != nil {
		return 0, err
	}

	var errs []error
	count := 0
	for _, p := range pngs {
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
			continue
		}
		count++
	}
	return count, errors.Join(errs...)
}

// generatePDFFilename は "<本の名前>_part<番号>.pdf" 形式のファイル名を作る。
// フォルダ名に使えない文字の置換ルールは internal/output と揃える。
func generatePDFFilename(bookName string, part int) string {
	return fmt.Sprintf("%s_part%d.pdf", output.SanitizeBookName(bookName), part)
}

// listPagePNGs は dir 内の page_*.png を番号順（ファイル名の辞書順）で返す。
// ファイル名はゼロ埋め4桁（page_0001.png 等）である前提のため、辞書順ソートが
// そのまま番号順になる。
func listPagePNGs(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "page_*.png"))
	if err != nil {
		return nil, fmt.Errorf("PNG一覧の取得に失敗しました: %w", err)
	}
	sort.Strings(matches)
	return matches, nil
}
