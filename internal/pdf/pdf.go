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
	"os"
	"path/filepath"
	"sort"

	"github.com/imutaroh/kindle-screenshot-app/internal/output"
)

// CreateFromDir は dir 内の page_*.png を番号順に取得し、pagesPerPDF 枚ごとに
// <bookName>_part1.pdf, _part2.pdf ... として dir 直下に作成する。
// 作成に成功した PDF のパス一覧を作成順に返す。
//
// 各ページのサイズは元画像のピクセルサイズと同じ（1px=1pt）にし、PNGは再エンコード
// せず Flate（可逆圧縮）でそのまま埋め込むため画質の劣化は発生しない。
func CreateFromDir(dir, bookName string, pagesPerPDF int) ([]string, error) {
	if pagesPerPDF <= 0 {
		return nil, fmt.Errorf("pagesPerPDFは1以上を指定してください（指定値: %d）", pagesPerPDF)
	}

	pngs, err := listPagePNGs(dir)
	if err != nil {
		return nil, err
	}
	if len(pngs) == 0 {
		return nil, nil
	}

	var created []string
	part := 1
	for i := 0; i < len(pngs); i += pagesPerPDF {
		end := i + pagesPerPDF
		if end > len(pngs) {
			end = len(pngs)
		}
		batch := pngs[i:end]

		images := make([]imageData, 0, len(batch))
		for _, p := range batch {
			img, err := decodePNGForPDF(p)
			if err != nil {
				return created, fmt.Errorf("part %d: %w", part, err)
			}
			images = append(images, img)
		}

		pdfBytes, err := buildPDF(images)
		if err != nil {
			return created, fmt.Errorf("part %d のPDF生成に失敗しました: %w", part, err)
		}

		outPath := filepath.Join(dir, generatePDFFilename(bookName, part))
		if err := os.WriteFile(outPath, pdfBytes, 0o644); err != nil {
			return created, fmt.Errorf("part %d の書き込みに失敗しました: %w", part, err)
		}

		created = append(created, outPath)
		part++
	}

	return created, nil
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
