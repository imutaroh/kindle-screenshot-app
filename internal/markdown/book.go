package markdown

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// ReviewConfidence は「読み取りが怪しい」とみなす行の確信度の平均の下限。
// Vision の確信度は正しく読めたページでも平均 0.3〜0.4 程度（ベンチの実測:
// 本文ページ 0.38、図入りページ 0.31、漫画 0.25〜0.30）なので、それより明確に低い値に置く。
const ReviewConfidence = 0.2

// BookPage は本1冊分の組み立てに渡す1ページ。
type BookPage struct {
	Number int      // ページ番号（page_0001.png なら 1）。<!-- p.N --> に使う
	OCR    ocr.Page // OCR 結果
	// Blank は画像がほぼ一様（白紙ページ）かどうか。白紙でないのに文字が取れなかった
	// ページを要確認にするために使う。判定は呼び出し側（ConvertDir）が画像から行う。
	Blank bool
}

// sentenceEnds は「ここで文が終わっている」とみなすページ末尾の文字。
// ページの最後の段落がこれ以外で終わっていれば、文の途中でページが切れている。
const sentenceEnds = "。．.！？!?」』）)】〕〉》”’…‥"

// endsSentence は s が文末記号で終わっているかを返す。
func endsSentence(s string) bool {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	r, _ := utf8.DecodeLastRuneInString(s)
	return r == utf8.RuneError || strings.ContainsRune(sentenceEnds, r)
}

// joinText は前のページの段落の末尾と次のページの段落の先頭をつなぐ。
// 日本語は空白なしでつなぐが、英単語同士が切れている場合だけ空白を入れる。
func joinText(a, b string) string {
	ra, _ := utf8.DecodeLastRuneInString(a)
	rb, _ := utf8.DecodeRuneInString(b)
	if ra < utf8.RuneSelf && rb < utf8.RuneSelf && (unicode.IsLetter(ra) || unicode.IsDigit(ra)) && unicode.IsLetter(rb) {
		return a + " " + b
	}
	return a + b
}

// Build は本1冊分の Markdown を組み立て、要確認ページの番号を返す。
//
// 出力の形:
//
//	# 書名
//
//	<!-- p.1 -->
//
//	## 見出し
//
//	本文……
//
// ページをまたいで続く段落は1つにつなぐ（前のページの最後の本文段落が文末記号で
// 終わらず列の終わりまで埋まっていて、次のページの最初のブロックが字下げなしの本文の
// とき）。つないだ部分は前のページの段落に入り、<!-- p.N --> はその後ろに来る。
//
// 要確認ページ（reviewPages）: OCR がエラーになったページ、白紙でないのに本文が
// 1文字も取れなかったページ（図だけのページを含む）、確信度の平均が ReviewConfidence
// 未満のページ。
func Build(title string, pages []BookPage) (md string, reviewPages []int) {
	var sb strings.Builder
	if t := strings.TrimSpace(title); t != "" {
		sb.WriteString("# " + t + "\n\n")
	}

	// 直前のページのブロックは、次のページの段落とつながる可能性があるので、
	// つながるかが決まるまで書き出さずに保留しておく。
	var pending []string // 書き出し待ちの Markdown ブロック
	pendingBody := -1    // pending の中で、次ページとつながりうる本文ブロックの位置（無ければ -1）
	flush := func() {
		for _, b := range pending {
			sb.WriteString(b + "\n\n")
		}
		pending = pending[:0]
		pendingBody = -1
	}

	for _, bp := range pages {
		if bp.OCR.Error != "" {
			reviewPages = append(reviewPages, bp.Number)
		}
		layout := layoutPage(bp.OCR)
		if bp.OCR.Error == "" && !layout.hasText() && !bp.Blank {
			reviewPages = append(reviewPages, bp.Number)
		} else if layout.hasText() && layout.meanConfidence < ReviewConfidence {
			reviewPages = append(reviewPages, bp.Number)
		}

		blocks := layout.blocks
		// 前のページの段落の続きなら、最初の本文ブロックを前の段落につなげる。
		// 保留中のブロックのうち、つないだ段落より後ろにあるのはページ区切りと
		// 引用（図の文字）だけなので、本文の順序は崩れない。
		merged := -1
		if pendingBody >= 0 && layout.startsMidParagraph && len(blocks) > 0 {
			pending[pendingBody] = joinText(pending[pendingBody], blocks[0].text)
			blocks = blocks[1:]
			merged = pendingBody
		} else {
			flush()
		}
		pendingBody = -1

		pending = append(pending, fmt.Sprintf("<!-- p.%d -->", bp.Number))
		last := lastBodyIndex(blocks)
		for i, b := range blocks {
			pending = append(pending, render(b))
			if i == last && layout.endsMidParagraph && !endsSentence(b.text) {
				pendingBody = len(pending) - 1
			}
		}
		// ページ全体が1つの段落の途中だった（つないだ段落のほかに本文も見出しも無い）なら、
		// その段落がさらに次のページへ続く可能性がある
		if merged >= 0 && last < 0 && !hasBreak(blocks) && layout.endsMidParagraph && !endsSentence(pending[merged]) {
			pendingBody = merged
		}
	}
	flush()
	return strings.TrimRight(sb.String(), "\n") + "\n", reviewPages
}

// lastBodyIndex は blocks の中で最後の本文ブロックの位置を返す（無ければ -1）。
// ただし、その後ろに見出し・箇条書き・表があれば段落は終わっているので -1。
// 引用（縦書きページの図の文字）は本文の流れの外なので飛ばして探す。
func lastBodyIndex(blocks []block) int {
	for i := len(blocks) - 1; i >= 0; i-- {
		switch blocks[i].kind {
		case kindBody:
			return i
		case kindHeading, kindStruct:
			return -1
		}
	}
	return -1
}

// hasBreak は blocks に段落の流れを切るもの（見出し・箇条書き・表）があるかを返す。
func hasBreak(blocks []block) bool {
	for _, b := range blocks {
		if b.kind == kindHeading || b.kind == kindStruct {
			return true
		}
	}
	return false
}

// render はブロックを Markdown にする。
func render(b block) string {
	switch b.kind {
	case kindHeading:
		return "## " + b.text
	case kindAside:
		// 規則3: 図・表・箇条書きの文字は引用にする（本文と区別できるように）
		lines := strings.Split(b.text, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRight("> "+l, " ")
		}
		return strings.Join(lines, "\n")
	default:
		return b.text
	}
}
