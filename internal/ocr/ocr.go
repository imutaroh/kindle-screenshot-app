// Package ocr は Apple Vision の OCR ヘルパー（Swift 製の小さな実行ファイル）を
// 管理し、PNG のページ画像から段落・行・リスト・表を読み取る。
//
// なぜ Swift ヘルパーか: 縦書き日本語を正しく（右→左の列順・段落構造つきで）読めるのは
// macOS 26 の Swift 専用 API RecognizeDocumentsRequest だけで、ObjC の
// VNRecognizeTextRequest は縦書きを読めない（ベンチマークで確認済み）。Go から
// Swift API は直接呼べず、CGo も使わない方針なので、Swift ソースを埋め込み、初回に
// swiftc でビルドしてキャッシュし、子プロセスとして起動する（build.go）。
//
// ヘルパーは複数の PNG パスを受け取り、1ページにつき1行の JSON を stdout に書く
// （JSON Lines）。ここで定義する構造体はその JSON をそのまま写したもの。
package ocr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Rect は正規化座標（0〜1）・左上原点の矩形 [x, y, w, h]。
// ヘルパーの JSON が配列なので、構造体ではなく固定長配列で受けている。
type Rect [4]float64

// X は左端、Y は上端、W は幅、H は高さを返す（すべて画像サイズに対する比率）。
func (r Rect) X() float64 { return r[0] }
func (r Rect) Y() float64 { return r[1] }
func (r Rect) W() float64 { return r[2] }
func (r Rect) H() float64 { return r[3] }

// Right は右端、Bottom は下端を返す。
func (r Rect) Right() float64  { return r[0] + r[2] }
func (r Rect) Bottom() float64 { return r[1] + r[3] }

// 文字の並ぶ向き。Vision の RecognizedTextObservation.Direction に対応する。
const (
	DirTTB = "ttb" // 縦書き（上→下）
	DirLTR = "ltr" // 横書き（左→右）
	DirRTL = "rtl" // 横書き（右→左）
)

// Line は認識された1行（縦書きなら1列）。
type Line struct {
	Text       string  `json:"text"`
	BBox       Rect    `json:"bbox"`
	Direction  string  `json:"direction"`
	Confidence float64 `json:"confidence"`
}

// Paragraph は Vision がまとめた段落。Lines は段落内の行を読み順で持つ。
//
// IsTitle は Vision がページの見出しと判断した段落（1ページに高々1つ）。
// 図のラベルを見出しと誤ることがあるため、見出しとして扱うかは呼び出し側
// （internal/markdown）が位置で判断する。
// Confidence は行の確信度の平均。Vision の確信度は正しく読めていても 0.3〜0.4 程度と
// 低めに出るので、絶対値ではなく相対的な目安として使う。
type Paragraph struct {
	Text       string  `json:"text"`
	BBox       Rect    `json:"bbox"`
	Direction  string  `json:"direction"`
	IsTitle    bool    `json:"isTitle"`
	Confidence float64 `json:"confidence"`
	Lines      []Line  `json:"lines"`
}

// List は Vision が箇条書きと判断した領域。項目の文字列は Paragraphs にも重複して入る。
type List struct {
	BBox  Rect     `json:"bbox"`
	Items []string `json:"items"`
}

// Table は Vision が表と判断した領域。Rows[行][列] にセルの文字列が入る。
type Table struct {
	BBox Rect       `json:"bbox"`
	Rows [][]string `json:"rows"`
}

// Page は1枚の画像の認識結果。Error が空でなければ、そのページは読み取りに失敗している
// （他のページの結果は有効）。Width/Height はピクセル。
type Page struct {
	Path       string      `json:"path"`
	Width      int         `json:"width"`
	Height     int         `json:"height"`
	Paragraphs []Paragraph `json:"paragraphs"`
	Lists      []List      `json:"lists"`
	Tables     []Table     `json:"tables"`
	Error      string      `json:"error,omitempty"`
}

// chunkSize は1回のヘルパー起動で処理するページ数。
// 起動ごとに Vision のモデル読み込み（約0.5秒）がかかるので、まとめて渡すほど速い。
// 一方で引数が長くなりすぎないよう、上限を設けている。
const chunkSize = 32

// maxHelperFailures は、ヘルパーが1ページも読まずに落ちることが何回続いたら
// 「ヘルパー自体が壊れている」とみなして全体を失敗にするか。
// 1ページで落ちるだけならそのページを要確認にして続けるが、毎回すぐ落ちるのに
// 残りのページ数だけ起動し直すと、全ページがエラーの Markdown ができるだけなので打ち切る。
const maxHelperFailures = 3

// Recognize は pngPaths の各画像を OCR し、入力と同じ順番で結果を返す。
// 内部では chunkSize ページずつヘルパーを起動する。ctx がキャンセルされると
// 実行中のヘルパーも終了させる。
func Recognize(ctx context.Context, pngPaths []string) ([]Page, error) {
	pages := make([]Page, 0, len(pngPaths))
	err := RecognizeEach(ctx, pngPaths, func(p Page) {
		pages = append(pages, p)
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

// RecognizeEach は Recognize と同じだが、1ページ読み終わるごとに fn を呼ぶ
// （入力順。進捗表示用）。fn は呼び出し元のゴルーチンで同期的に呼ばれる。
func RecognizeEach(ctx context.Context, pngPaths []string, fn func(Page)) error {
	if len(pngPaths) == 0 {
		return nil
	}
	bin, err := ensureBuilt(ctx)
	if err != nil {
		return err
	}
	for start := 0; start < len(pngPaths); start += chunkSize {
		end := min(start+chunkSize, len(pngPaths))
		if err := runChunk(ctx, bin, pngPaths[start:end], fn); err != nil {
			return err
		}
	}
	return nil
}

// runChunk は paths をヘルパーで読み、結果を1ページずつ入力順に fn に渡す。
//
// ヘルパーが途中で落ちた（Vision のクラッシュ・メモリ不足・出力の不正など）場合は、
// そこまでに読めたページは捨てずに渡し、落ちたページは Error 付きの Page として渡し
// （Markdown では要確認ページになる）、次のページからヘルパーを起動し直す。
// 1ページの失敗で本1冊分の OCR（数百ページなら10分以上）を無駄にしないため。
//
// 呼び出し側（markdown.ConvertDir）は「fn は paths の各要素につきちょうど1回、入力順に
// 呼ばれる」ことを前提にしているので、どの経路でもこれを守る。
func runChunk(ctx context.Context, bin string, paths []string, fn func(Page)) error {
	failures := 0 // 1ページも読めずに落ちた回数（連続）
	for len(paths) > 0 {
		n, err := runHelper(ctx, bin, paths, fn)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil // 全ページ読めた
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			return fatal.err
		}
		if n == 0 {
			failures++
			if failures >= maxHelperFailures {
				return fmt.Errorf("OCR ヘルパーが%d回続けて1ページ目で終了しました: %w", failures, err)
			}
		} else {
			failures = 0
		}
		// paths[n] で落ちた。そのページを失敗として渡し、次のページから再開する
		fn(Page{Path: paths[n], Error: err.Error()})
		paths = paths[n+1:]
	}
	return nil
}

// fatalError は、ページを飛ばして再開しても意味がない失敗（ヘルパーを起動できない、
// 出力が入力より多いなど、どのページの失敗か特定できないもの）を表す。
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }

// runHelper はヘルパーを1回起動し、paths の結果を1行ずつ読んで fn に渡す。
// 読めたページ数 n を返す。n < len(paths) なら err は paths[n] で止まった理由を表す
// （ページを飛ばして続けられないときは *fatalError）。
func runHelper(ctx context.Context, bin string, paths []string, fn func(Page)) (int, error) {
	cmd := exec.CommandContext(ctx, bin, paths...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, &fatalError{err}
	}
	if err := cmd.Start(); err != nil {
		return 0, &fatalError{fmt.Errorf("OCR ヘルパーを起動できません: %w", err)}
	}

	n, decodeErr := decodePages(stdout, paths, fn)
	// stdout を読み切る前に Wait すると子プロセスが書き込みで詰まる可能性があるので、
	// decodePages が EOF まで読んだ（またはエラーで抜けた）あとに Wait する。
	// 出力が壊れていたらその後のページの結果は使わないので、残りの処理を待たずに止める。
	if decodeErr != nil {
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()

	switch {
	case decodeErr != nil:
		if errors.Is(decodeErr, errTooManyLines) {
			return n, &fatalError{decodeErr}
		}
		return n, decodeErr
	case n == len(paths):
		// 全ページの結果を受け取れていれば、終了時に落ちていても結果は使える
		return n, nil
	default:
		return n, fmt.Errorf("OCR ヘルパーが途中で終了しました（%d/%d ページ）: %v: %s",
			n, len(paths), waitErr, tail(stderr.String(), 800))
	}
}

// errTooManyLines はヘルパーの出力行が入力のページ数より多いことを表す。
var errTooManyLines = errors.New("OCR ヘルパーの出力が入力より多い")

// decodePages はヘルパーの stdout（JSON Lines）を読み、各行を Page にして fn に渡す。
// 読めたページ数を返す。ヘルパーは引数の順に出力するので、行と paths を順番で突き合わせ、
// ずれていたらエラーにする（別ページの結果を取り違えないため）。
func decodePages(r io.Reader, paths []string, fn func(Page)) (int, error) {
	sc := bufio.NewScanner(r)
	// 1ページ分の JSON は文字の多いページで数十KBになる。既定の 64KB では足りないことがある。
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	n := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if n >= len(paths) {
			return n, errTooManyLines
		}
		var p Page
		if err := json.Unmarshal(line, &p); err != nil {
			return n, fmt.Errorf("OCR ヘルパーの出力を解析できません: %w", err)
		}
		if p.Path != paths[n] {
			return n, fmt.Errorf("OCR ヘルパーの出力順が不正: %q を期待したが %q", paths[n], p.Path)
		}
		fn(p)
		n++
	}
	return n, sc.Err()
}

// tail は s の末尾 max バイト程度を返す（エラーメッセージに stderr を添える用）。
func tail(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	// バイト位置で切ると UTF-8 の文字の途中になりうるので、壊れた先頭部分を落とす
	return "…" + strings.ToValidUTF8(s[len(s)-max:], "")
}
