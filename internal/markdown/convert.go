package markdown

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/imutaroh/kindle-screenshot-app/internal/crop"
	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
	"github.com/imutaroh/kindle-screenshot-app/internal/output"
)

// Options は ConvertDir の設定。
type Options struct {
	// BookTitle は書名。Markdown 先頭の "# 書名" と出力ファイル名に使う。
	// 空なら pngDir のフォルダ名を使う（kindlesnap md で書名を渡さなくても動くように）。
	BookTitle string
	// Progress は1ページ OCR するごとに呼ばれる（done は済んだページ数）。nil なら呼ばない。
	Progress func(done, total int)
}

// Result は ConvertDir の結果。
type Result struct {
	MarkdownPath string // 書き出した .md の絶対パス
	Pages        int    // 処理したページ数
	ReviewPages  []int  // 人（または Claude）が画像を見て確認すべきページ番号。Build を参照
}

// pageFileRe はキャプチャが保存するページ画像の名前（page_0001.png）。
var pageFileRe = regexp.MustCompile(`^page_(\d+)\.png$`)

// ConvertDir は pngDir の page_*.png をページ番号順に OCR し、
// <pngDir>/<書名>.md に Markdown を書き出す。
func ConvertDir(ctx context.Context, pngDir string, opts Options) (Result, error) {
	dir, err := filepath.Abs(pngDir)
	if err != nil {
		return Result{}, err
	}
	paths, nums, err := listPages(dir)
	if err != nil {
		return Result{}, err
	}

	title := strings.TrimSpace(opts.BookTitle)
	if title == "" {
		title = filepath.Base(dir)
	}

	pages := make([]BookPage, 0, len(paths))
	err = ocr.RecognizeEach(ctx, paths, func(p ocr.Page) {
		i := len(pages)
		bp := BookPage{Number: nums[i], OCR: p}
		// 白紙判定は画像の読み込みが要るので、文字が取れなかったページだけ行う
		if p.Error == "" && !layoutPage(p).hasText() {
			bp.Blank = isBlankImage(paths[i])
		}
		pages = append(pages, bp)
		if opts.Progress != nil {
			opts.Progress(len(pages), len(paths))
		}
	})
	if err != nil {
		return Result{}, fmt.Errorf("OCR に失敗しました: %w", err)
	}

	md, review := Build(title, pages)
	outPath := filepath.Join(dir, output.SanitizeBookName(title)+".md")
	if err := writeFileAtomic(outPath, []byte(md)); err != nil {
		return Result{}, fmt.Errorf("Markdown を書き出せません: %w", err)
	}
	return Result{MarkdownPath: outPath, Pages: len(pages), ReviewPages: review}, nil
}

// listPages は dir の page_*.png をページ番号順に返す。
// 文字列順ではなく番号順に並べるのは、page_10000.png（5桁）が page_9999.png より
// 前に来てしまうのを防ぐため。
func listPages(dir string) (paths []string, nums []int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	type page struct {
		path string
		num  int
	}
	var ps []page
	for _, e := range entries {
		m := pageFileRe.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		ps = append(ps, page{filepath.Join(dir, e.Name()), n})
	}
	if len(ps) == 0 {
		return nil, nil, errors.New("page_*.png が見つかりません: " + dir)
	}
	slices.SortFunc(ps, func(a, b page) int { return a.num - b.num })
	for _, p := range ps {
		paths = append(paths, p.path)
		nums = append(nums, p.num)
	}
	return paths, nums, nil
}

// isBlankImage は画像に背景以外の内容が無い（白紙ページ）かを返す。
// 余白トリミングと同じ crop.Detect を使い、内容の矩形が見つからなければ白紙とする。
// 読めない画像は「白紙ではない」扱いにして、要確認ページに回す。
func isBlankImage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return false
	}
	_, _, ok := crop.Detect(img)
	return !ok
}

// writeFileAtomic は同じフォルダの一時ファイルに書いてから rename する。
// 書き込み途中で落ちても、中途半端な .md が残らない。
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".md-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 成功後は存在しないので何もしない
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
