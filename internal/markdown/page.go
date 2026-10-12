// Package markdown は OCR 結果（internal/ocr の Page）を本1冊分の Markdown に組み立てる。
//
// Vision の段落をそのまま並べると、柱（ページ上下の書名・ページ番号）やルビ、
// 図中のラベルが本文に混ざる。ここではベンチマーク（縦書きの実ページで CER 0%）で
// 効果を確かめた4つの後処理を、1ページごとに適用する:
//
//  1. 上端・下端の帯に収まる柱（書名・ウィンドウタイトル「Kindle」・ページ番号）を捨てる。
//     縦書きページでは帯の中の横書き段落、横書きページでは帯の中の短い段落が対象。
//     縦書きの本の図だけのページ（横書きページと判定される）で長い書名の柱が残らないよう、
//     本の縦書きページで見つけた柱と似た段落も捨てる（book.go の bookRunningHeads）。
//  2. ルビを捨てる: 本文より細いひらがなだけの縦書き段落で、すぐ左に漢字を含む
//     縦書き段落が縦方向に重なって並んでいるもの。
//  3. 縦書きページの横書き段落・箇条書き・表（図や表の中の文字）は、本文の流れを
//     切らないよう、本文の後ろに引用（"> "）としてまとめる。
//  4. 縦書きの「……」は「・」の連続として読まれるので、2個以上の連続を「……」に戻す。
//
// このファイルは1ページ分の処理（純粋関数）、book.go が本1冊分の組み立て、
// convert.go がファイルの読み書きを担当する。
package markdown

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// 後処理のしきい値。座標はすべて画像サイズに対する比率（0〜1、左上原点）。
const (
	// edgeBand は柱とみなす上端・下端の帯の幅。実画像では書名・ページ番号・
	// ウィンドウタイトルはいずれも端から 6% 以内に収まり、本文は 7% より内側から始まる。
	edgeBand = 0.08
	// runningHeadMaxChars は横書きページで柱とみなす段落の最大文字数（空白を除く）。
	// 横書きページでは本文も柱も同じ向きなので、向きでは区別できない。帯に収まる
	// 短い段落（書名・章名・ページ番号）だけを柱とし、長い段落は念のため本文に残す。
	runningHeadMaxChars = 30
	// runningHeadSimilarity は、帯の中の段落を本の柱（縦書きページで見つけた柱）と同じとみなす
	// 類似度（2×最長共通部分列の長さ ÷ 2つの文字数の和）の下限。柱は毎ページ同じ文字列だが、
	// 小さい文字なので OCR が数文字を読み違える（「組織」→「組版」など）ため、完全一致では拾えない。
	runningHeadSimilarity = 0.8

	// rubyWidthRatio: ルビの列幅は本文の列幅（縦書き行の幅の中央値）のおよそ半分。
	// 0.7 未満を「本文より明らかに細い」とみなす。
	rubyWidthRatio = 0.7
	// rubyGapMin/rubyGapExtra: ルビの左端と親文字の段落の右端の位置関係の許容幅。
	// 親文字の段落はルビのすぐ左にあり、右端がルビの左端付近（少し重なるか、ほぼ接する）に来る。
	rubyGapMin   = -0.01
	rubyGapExtra = 0.005

	// titleRightTol は「ページで一番右の縦書き段落」とみなす右端の誤差。
	titleRightTol = 0.005

	// continueLineMin: ページをまたいで続く段落の最後の列は、列の高さいっぱいまで
	// 文字が詰まっている。本文の列（高さ 0.85 前後）と漫画の吹き出し（0.15 以下）を分ける値。
	continueLineMin = 0.5
	// continueEndTol は「列の終わり（縦書きなら下端）まで届いている」とみなす誤差。
	continueEndTol = 0.03
	// indentTol は「字下げなし」とみなす、列の頭からのずれ（文字数）。startsAtLineHead を参照。
	indentTol = 0.3
)

// blockKind はページ内のブロックの種類。
type blockKind int

const (
	kindHeading blockKind = iota // 見出し（"## "）
	kindBody                     // 本文の段落
	kindAside                    // 縦書きページの図・表・箇条書きの文字（引用にして本文の後ろへ）
	kindStruct                   // 横書きページの箇条書き・表（そのまま出す。段落連結の対象外）
)

// block は Markdown の1ブロック（空行で区切られる単位）。
type block struct {
	kind blockKind
	text string
}

// pageLayout は1ページに後処理を適用した結果。
type pageLayout struct {
	vertical bool
	blocks   []block // 出力順（縦書きページでは aside は本文の後ろ）

	// 前後のページと段落をつなぐための情報（book.go の mergeAcross で使う）。
	// startsMidParagraph: 最初のブロックが本文で、字下げなしで列の頭から始まっている。
	// endsMidParagraph: 最後の本文段落の最後の列が、列の終わりまで文字で埋まっている。
	startsMidParagraph bool
	endsMidParagraph   bool

	// meanConfidence は残した行（柱・ルビを除く）の確信度の平均。行が無ければ 0。
	meanConfidence float64
}

// hasText は本文か見出しが1つでもあるかを返す（図の中の文字だけのページは false）。
func (l pageLayout) hasText() bool {
	for _, b := range l.blocks {
		if b.kind != kindAside && strings.TrimSpace(b.text) != "" {
			return true
		}
	}
	return false
}

var (
	// 「・」（全角・半角）が2個以上続く部分。縦書きの三点リーダの誤読。
	dotsRe = regexp.MustCompile(`[・･]{2,}`)
	// ひらがな（と長音・空白）だけの文字列。ルビの判定に使う。
	hiraganaOnlyRe = regexp.MustCompile(`^[\p{Hiragana}ー\s]+$`)
	hanRe          = regexp.MustCompile(`\p{Han}`)
)

// fixDots は規則4（「・」の連続 → 「……」）を適用する。
func fixDots(s string) string {
	return dotsRe.ReplaceAllString(s, "……")
}

func isTTB(p ocr.Paragraph) bool { return p.Direction == ocr.DirTTB }

// isVerticalPage は、認識した文字の過半数が縦書きの段落にあれば縦書きページとみなす。
// 段落の数ではなく文字数で比べるのは、図中の短い横書きラベルが多いページでも
// 本文の向きで判定するため。
//
// 上端・下端の帯（柱の位置）にある段落は数えない。柱の書名は縦書きの本でも横書きなので、
// 章扉のように本文が短いページ（柱の書名 5〜6文字 + 章題「はじめに」4文字）で
// 柱の文字数が勝ち、横書きページと誤判定されて柱も章題も本文扱いになってしまうため。
// 帯の外に段落が1つも無いページだけは、全段落で判定する。
func isVerticalPage(p ocr.Page) bool {
	count := func(skipBand bool) (ttb, all int) {
		for _, para := range p.Paragraphs {
			if skipBand && inEdgeBand(para) {
				continue
			}
			n := countChars(para.Text)
			all += n
			if isTTB(para) {
				ttb += n
			}
		}
		return ttb, all
	}
	ttb, all := count(true)
	if all == 0 {
		ttb, all = count(false)
	}
	return ttb*2 > all
}

// inEdgeBand は段落が上端か下端の帯（柱の位置）に完全に収まっているかを返す。
func inEdgeBand(para ocr.Paragraph) bool {
	return para.BBox.Bottom() < edgeBand || para.BBox.Y() > 1-edgeBand
}

// countChars は空白を除いた文字数を返す。
func countChars(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// hasLetter は s に文字か数字が1つでもあるかを返す。
// 「.」だけのような記号だけの段落は、図の線や汚れを誤認識したものなので捨てる。
func hasLetter(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// medianTTBLineWidth は縦書きの行（列）の幅の中央値。本文の列幅の目安に使う。
func medianTTBLineWidth(p ocr.Page) float64 {
	var ws []float64
	for _, para := range p.Paragraphs {
		for _, l := range para.Lines {
			if l.Direction == ocr.DirTTB {
				ws = append(ws, l.BBox.W())
			}
		}
	}
	if len(ws) == 0 {
		return 0
	}
	slices.Sort(ws)
	return ws[len(ws)/2]
}

// isRunningHead は規則1（柱）に当たるかを返す。上端か下端の帯に完全に収まる段落のうち:
//   - ウィンドウタイトルの「Kindle」: ページの向きに関係なく本の内容ではないので、常に捨てる
//   - 縦書きページ: 横書きの段落（書名・ページ番号）
//   - 横書きページ: runningHeadMaxChars 文字以下の短い段落（書名・章名・ページ番号）。
//     これを捨てないと、横書きの本では毎ページ柱が本文に混ざり、しかもページの最初と
//     最後のブロックが柱になるので、ページをまたぐ段落の連結も一切効かなくなる
//   - 本の柱 knownHeads（縦書きページで見つけた柱）と似た段落: ページの向きに関係なく捨てる。
//     縦書きの本でも図だけのページは横書きページと判定されるので、長い書名の柱が
//     runningHeadMaxChars を超えて本文（しかも見出し）に残ってしまうのを防ぐ
func isRunningHead(para ocr.Paragraph, vertical bool, knownHeads []string) bool {
	if !inEdgeBand(para) {
		return false
	}
	if strings.TrimSpace(para.Text) == "Kindle" {
		return true
	}
	if vertical {
		return !isTTB(para)
	}
	if countChars(para.Text) <= runningHeadMaxChars {
		return true
	}
	return matchesKnownHead(para.Text, knownHeads)
}

// matchesKnownHead は s が knownHeads のどれかと runningHeadSimilarity 以上に似ているかを返す。
func matchesKnownHead(s string, knownHeads []string) bool {
	a := []rune(compactText(s))
	if len(a) == 0 {
		return false
	}
	for _, h := range knownHeads {
		b := []rune(h)
		if len(b) == 0 {
			continue
		}
		if float64(2*lcsLen(a, b))/float64(len(a)+len(b)) >= runningHeadSimilarity {
			return true
		}
	}
	return false
}

// compactText は空白を除いた文字列（柱の比較用）。
func compactText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// lcsLen は a と b の最長共通部分列の長さ。柱は数十文字なので O(len(a)×len(b)) で足りる。
func lcsLen(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			switch {
			case a[i-1] == b[j-1]:
				cur[j] = prev[j-1] + 1
			case prev[j] >= cur[j-1]:
				cur[j] = prev[j]
			default:
				cur[j] = cur[j-1]
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// isRuby は規則2（ルビ）に当たるかを返す。
// 縦書きのルビは親文字の列の右側に細く並ぶ。Vision はそれを別の段落として返すので、
// 「細い」「ひらがなだけ」「すぐ左に漢字を含む縦書き段落がある」の3条件で見分ける。
func isRuby(para ocr.Paragraph, all []ocr.Paragraph, medW float64) bool {
	if !isTTB(para) || medW <= 0 || para.BBox.W() >= medW*rubyWidthRatio {
		return false
	}
	if !hiraganaOnlyRe.MatchString(para.Text) {
		return false
	}
	b := para.BBox
	for _, c := range all {
		if !isTTB(c) || c.BBox.W() <= b.W() || !hanRe.MatchString(c.Text) {
			continue
		}
		// c の右端が b の左端のすぐそば（b の幅程度まで）にある = c は b のすぐ左
		gap := c.BBox.Right() - b.X()
		if gap <= rubyGapMin || gap >= b.W()+rubyGapExtra {
			continue
		}
		// 縦方向に、ルビの高さの半分以上重なっている
		overlap := min(c.BBox.Bottom(), b.Bottom()) - max(c.BBox.Y(), b.Y())
		if overlap > b.H()*0.5 {
			return true
		}
	}
	return false
}

// inside は a の中心が c の内側にあるかを返す（段落が箇条書き・表の一部かの判定）。
func inside(a, c ocr.Rect) bool {
	cx, cy := a.X()+a.W()/2, a.Y()+a.H()/2
	return cx >= c.X() && cx <= c.Right() && cy >= c.Y() && cy <= c.Bottom()
}

// renderList は箇条書きを Markdown のリストにする。
func renderList(l ocr.List) string {
	items := make([]string, 0, len(l.Items))
	for _, it := range l.Items {
		items = append(items, "- "+fixDots(strings.TrimSpace(it)))
	}
	return strings.Join(items, "\n")
}

// renderTable は表を Markdown の表にする（1行目を見出し行として扱う）。
func renderTable(t ocr.Table) string {
	if len(t.Rows) == 0 {
		return ""
	}
	row := func(cells []string) string {
		esc := make([]string, len(cells))
		for i, c := range cells {
			esc[i] = strings.ReplaceAll(fixDots(c), "|", `\|`)
		}
		return "| " + strings.Join(esc, " | ") + " |"
	}
	lines := []string{row(t.Rows[0]), "|" + strings.Repeat("---|", len(t.Rows[0]))}
	for _, r := range t.Rows[1:] {
		lines = append(lines, row(r))
	}
	return strings.Join(lines, "\n")
}

// layoutPage は1ページに4つの後処理を適用し、Markdown のブロック列にする。
func layoutPage(p ocr.Page) pageLayout {
	return layoutPageWithHeads(p, nil)
}

// layoutPageWithHeads は layoutPage に、本の柱 knownHeads（bookRunningHeads）を渡して柱の判定に使う版。
func layoutPageWithHeads(p ocr.Page, knownHeads []string) pageLayout {
	vertical := isVerticalPage(p)
	medW := medianTTBLineWidth(p)

	// 柱とルビを先に除いておく（見出しの「一番右」判定や段落連結の判定に混ぜないため）
	var kept []ocr.Paragraph
	for _, para := range p.Paragraphs {
		if !hasLetter(para.Text) || isRunningHead(para, vertical, knownHeads) || isRuby(para, p.Paragraphs, medW) {
			continue
		}
		kept = append(kept, para)
	}

	out := pageLayout{vertical: vertical}
	var main, asides []block
	listDone := make([]bool, len(p.Lists))
	tableDone := make([]bool, len(p.Tables))
	var bodyParas []ocr.Paragraph // 本文になった段落（段落連結の判定用）
	var confSum float64
	var confN int

	// 構造ブロック（箇条書き・表）を置く先。縦書きページでは本文の後ろの引用へ回す（規則3）。
	addStructured := func(text string) {
		if text == "" {
			return
		}
		if vertical {
			asides = append(asides, block{kindAside, text})
		} else {
			main = append(main, block{kindStruct, text})
		}
	}

	for _, para := range kept {
		for _, l := range para.Lines {
			confSum += l.Confidence
			confN++
		}

		// 箇条書き・表の一部の段落は、その構造の最初の段落の位置で構造ごと1回だけ出す。
		// （Vision は箇条書きの項目を段落としても重複して返すため）
		if i := slices.IndexFunc(p.Lists, func(l ocr.List) bool { return inside(para.BBox, l.BBox) }); i >= 0 {
			if !listDone[i] {
				listDone[i] = true
				addStructured(renderList(p.Lists[i]))
			}
			continue
		}
		if i := slices.IndexFunc(p.Tables, func(t ocr.Table) bool { return inside(para.BBox, t.BBox) }); i >= 0 {
			if !tableDone[i] {
				tableDone[i] = true
				addStructured(renderTable(p.Tables[i]))
			}
			continue
		}

		text := fixDots(strings.TrimSpace(para.Text))
		switch {
		case isHeading(para, kept, vertical):
			main = append(main, block{kindHeading, text})
		case vertical && !isTTB(para):
			asides = append(asides, block{kindAside, text}) // 規則3
		default:
			main = append(main, block{kindBody, text})
			bodyParas = append(bodyParas, para)
		}
	}
	// 段落としては返ってこなかった箇条書き・表も落とさない
	for i, l := range p.Lists {
		if !listDone[i] {
			addStructured(renderList(l))
		}
	}
	for i, t := range p.Tables {
		if !tableDone[i] {
			addStructured(renderTable(t))
		}
	}

	out.blocks = main
	if len(asides) > 0 {
		// 図の文字はまとめて1つの引用にする（空行で区切った引用が並ぶと読みにくいため）
		parts := make([]string, len(asides))
		for i, a := range asides {
			parts[i] = a.text
		}
		out.blocks = append(out.blocks, block{kindAside, strings.Join(parts, "\n\n")})
	}
	if confN > 0 {
		out.meanConfidence = confSum / float64(confN)
	}
	if len(bodyParas) > 0 {
		out.startsMidParagraph = len(main) > 0 && main[0].kind == kindBody &&
			startsAtLineHead(bodyParas[0], kept, vertical, p)
		out.endsMidParagraph = len(main) > 0 && main[len(main)-1].kind == kindBody &&
			endsAtLineEnd(bodyParas[len(bodyParas)-1], kept, vertical)
	}
	return out
}

// isHeading は Vision が見出しとした段落を、本当に見出しとして扱ってよいかを判定する。
// Vision は図のラベルや漫画のセリフを見出しに選ぶことがあるので保守的にする:
//   - 縦書きページ: 縦書きで、かつページで一番右の縦書き段落（章題は本文より右から始まる）
//   - 横書きページ: 横書きで、かつページで一番上の段落
func isHeading(para ocr.Paragraph, kept []ocr.Paragraph, vertical bool) bool {
	if !para.IsTitle {
		return false
	}
	if vertical {
		if !isTTB(para) {
			return false
		}
		for _, o := range kept {
			if isTTB(o) && o.BBox.Right() > para.BBox.Right()+titleRightTol {
				return false
			}
		}
		return true
	}
	if isTTB(para) {
		return false
	}
	for _, o := range kept {
		if o.BBox.Y() < para.BBox.Y()-titleRightTol {
			return false
		}
	}
	return true
}

// endsAtLineEnd は段落の最後の行（縦書きなら最後の列）が、ページの本文の終わり
// （縦書きなら列の下端、横書きなら行の右端）まで文字で埋まっているかを返す。
// 埋まっていれば、文の途中でページが切れている可能性がある。
// 高さ（横書きなら幅）に下限を設けて、漫画の短い吹き出しを除外する。
func endsAtLineEnd(para ocr.Paragraph, kept []ocr.Paragraph, vertical bool) bool {
	if len(para.Lines) == 0 {
		return false
	}
	last := para.Lines[len(para.Lines)-1].BBox
	limit := 0.0
	for _, o := range kept {
		for _, l := range o.Lines {
			if vertical {
				limit = max(limit, l.BBox.Bottom())
			} else {
				limit = max(limit, l.BBox.Right())
			}
		}
	}
	if vertical {
		return last.H() >= continueLineMin && last.Bottom() >= limit-continueEndTol
	}
	return last.W() >= continueLineMin && last.Right() >= limit-continueEndTol
}

// startsAtLineHead は段落の最初の行が字下げなしで行頭（縦書きなら列の上端）から
// 始まっているかを返す。日本語の段落は1字下げで始まるので、字下げが無ければ
// 前のページからの続きと考えられる。
//
// 許容幅は indentTol 文字分。実画像（c_inv）の実測では、列の頭から始まる列は
// 列の頭から 0.2 文字以内、字下げした段落の1列目は 1 文字前後下がっていた。
// ただし「「」で始まる段落は字下げが約 0.4 文字と浅いので、それより小さい値にしている。
func startsAtLineHead(para ocr.Paragraph, kept []ocr.Paragraph, vertical bool, p ocr.Page) bool {
	if len(para.Lines) == 0 || p.Width <= 0 || p.Height <= 0 {
		return false
	}
	first := para.Lines[0].BBox
	head := 1.0
	var sizes []float64 // 1文字の大きさの目安（縦書きなら列幅、横書きなら行の高さ）
	for _, o := range kept {
		if isTTB(o) != vertical {
			continue
		}
		for _, l := range o.Lines {
			if vertical {
				head = min(head, l.BBox.Y())
				sizes = append(sizes, l.BBox.W())
			} else {
				head = min(head, l.BBox.X())
				sizes = append(sizes, l.BBox.H())
			}
		}
	}
	if len(sizes) == 0 {
		return false
	}
	slices.Sort(sizes)
	char := sizes[len(sizes)/2]
	// 正規化座標は縦横で基準が違う（幅と高さで割っている）ので、列幅を縦方向の比率に直す
	aspect := float64(p.Width) / float64(p.Height)
	if vertical {
		return first.Y() <= head+char*aspect*indentTol
	}
	return first.X() <= head+char/aspect*indentTol
}
