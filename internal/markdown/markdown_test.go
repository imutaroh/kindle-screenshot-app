package markdown

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// loadFixture は testdata の OCR 結果（実画像に本物のヘルパーをかけて保存したもの）を読む。
func loadFixture(t *testing.T, name string) ocr.Page {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p ocr.Page
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// texts は kind の種類のブロックの文字列を返す。
func texts(l pageLayout, kind blockKind) []string {
	var out []string
	for _, b := range l.blocks {
		if b.kind == kind {
			out = append(out, b.text)
		}
	}
	return out
}

func containsText(l pageLayout, s string) bool {
	for _, b := range l.blocks {
		if strings.Contains(b.text, s) {
			return true
		}
	}
	return false
}

// 本文ページ: 柱を捨て、右端の章題を見出しにし、本文9段落を読み順に並べる。
func TestLayoutBodyPage(t *testing.T) {
	for _, name := range []string{"c_inv", "c_dark"} {
		t.Run(name, func(t *testing.T) {
			l := layoutPage(loadFixture(t, name))
			if !l.vertical {
				t.Fatal("縦書きページと判定されない")
			}
			h := texts(l, kindHeading)
			if len(h) != 1 || h[0] != "【序章】れは例として作った文字列で" {
				t.Errorf("見出し = %q", h)
			}
			body := texts(l, kindBody)
			if len(body) != 8 {
				t.Fatalf("本文段落数 = %d: %q", len(body), body)
			}
			if !strings.HasSuffix(body[0], "文字ー列で「す内容に意」味は。") ||
				!strings.HasPrefix(body[7], "「めの例文です見出しや本文の配置」") {
				t.Errorf("本文の順序が違う: %q ... %q", body[0], body[7])
			}
			for _, b := range l.blocks {
				if b.text == "サンプル書籍" {
					t.Errorf("柱（書名）が残っている: %+v", l.blocks)
				}
			}
			if len(texts(l, kindAside)) != 0 {
				t.Errorf("引用があるはずがない: %q", texts(l, kindAside))
			}
			if l.startsMidParagraph {
				t.Error("章題で始まるページを前ページの続きと判定")
			}
			if l.meanConfidence < ReviewConfidence {
				t.Errorf("正しく読めたページの確信度がしきい値未満: %.3f", l.meanConfidence)
			}
		})
	}
}

// 漫画ページ: Vision が見出しに選んだセリフを見出しにしない。ルビ6個を捨てる。
func TestLayoutMangaRuby(t *testing.T) {
	l := layoutPage(loadFixture(t, "hyaku1"))
	if h := texts(l, kindHeading); len(h) != 0 {
		t.Errorf("右端でないセリフを見出しにした: %q", h)
	}
	for _, ruby := range []string{"るのこ", "ならべたみ", "えおかき", "むめもやゆ", "をさんぷ", "うえおか"} {
		for _, b := range l.blocks {
			if b.text == ruby {
				t.Errorf("ルビ %q が残っている", ruby)
			}
		}
	}
	if !containsText(l, "設計た例みほ？") || !containsText(l, "こまみ……マミ？") {
		t.Errorf("本文（または規則4の……）が無い: %+v", l.blocks)
	}
	if containsText(l, "Kindle") {
		t.Error("ウィンドウタイトルが残っている")
	}
}

// 漫画ページ: 柱（Kindle・ページ番号）とルビを捨て、横書きの擬音などは引用で末尾へ。
// 最後の吹き出し「キクケコ」は文末記号が無いが、短いので次ページと連結しない。
func TestLayoutMangaFurniture(t *testing.T) {
	l := layoutPage(loadFixture(t, "hs_3"))
	for _, b := range l.blocks {
		switch b.text {
		case "Kindle", "15", "ぷるのこ", "ばをな":
			t.Errorf("柱かルビ %q が残っている", b.text)
		}
	}
	asides := texts(l, kindAside)
	if len(asides) == 0 || l.blocks[len(l.blocks)-1].kind != kindAside {
		t.Errorf("横書きの文字が末尾の引用になっていない: %q", asides)
	}
	if l.endsMidParagraph {
		t.Error("吹き出しを段落の途中と誤判定")
	}
}

// 図のページ: 図のラベルを見出しにせず、箇条書きは引用の中に1回だけ出す。
func TestLayoutFigureList(t *testing.T) {
	l := layoutPage(loadFixture(t, "fig1"))
	if h := texts(l, kindHeading); len(h) != 0 {
		t.Errorf("図のラベルを見出しにした: %q", h)
	}
	md, _ := Build("", []BookPage{{Number: 1, OCR: loadFixture(t, "fig1")}})
	if n := strings.Count(md, "練習課と題ばを"); n != 1 {
		t.Errorf("箇条書きの項目が %d 回出ている:\n%s", n, md)
	}
	// Vision の itemString は行頭の「・」を除いて返す
	if !strings.Contains(md, "> - 練習課と題ばを") || strings.Contains(md, "\n> .\n") {
		t.Errorf("箇条書きが引用になっていない:\n%s", md)
	}
	body := texts(l, kindBody)
	if len(body) == 0 || !strings.HasPrefix(body[0], "文章が入りますこれは例として") {
		t.Errorf("本文の先頭が違う: %q", body)
	}
}

// 章扉の後に本文が続くページ: 章題を見出しにし、末尾が「ありませ」で切れているので
// 次ページへの連結候補になる。
func TestLayoutChapterAndContinuation(t *testing.T) {
	l := layoutPage(loadFixture(t, "fig3"))
	h := texts(l, kindHeading)
	if len(h) != 1 || h[0] != "【第1章】 作る架空" {
		t.Errorf("見出し = %q", h)
	}
	body := texts(l, kindBody)
	if last := body[len(body)-1]; !strings.HasSuffix(last, "容に意味はありませ") {
		t.Errorf("最後の本文 = %q", last)
	}
	if !l.endsMidParagraph {
		t.Error("列の下端まで埋まった最後の段落を連結候補と判定しない")
	}
}

// contPage は縦書きページを合成する。列の頭は y=0.079、1字下げは +0.03
// （c_inv の実測: 列幅 0.0174 × 縦横比 1.64 ≒ 1文字 0.029）。
// indent が false なら最初の段落は字下げなし（前ページの続き）で始まる。
// 2つ目の段落は1字下げの1列目と、列の頭から始まる2列目を持つ。
func contPage(indent bool) ocr.Page {
	const head, w = 0.079, 0.0174
	line := func(x, y float64, text string) ocr.Line {
		return ocr.Line{Text: text, BBox: ocr.Rect{x, y, w, 0.9 - y}, Direction: ocr.DirTTB, Confidence: 0.4}
	}
	para := func(lines ...ocr.Line) ocr.Paragraph {
		var text string
		x0, x1 := 1.0, 0.0
		for _, l := range lines {
			text += l.Text
			x0, x1 = min(x0, l.BBox.X()), max(x1, l.BBox.Right())
		}
		return ocr.Paragraph{Text: text, BBox: ocr.Rect{x0, head, x1 - x0, 0.9 - head},
			Direction: ocr.DirTTB, Confidence: 0.4, Lines: lines}
	}
	top := head
	if indent {
		top += 0.03
	}
	return ocr.Page{Width: 1470, Height: 895, Paragraphs: []ocr.Paragraph{
		para(line(0.85, top, "んが次のページで文が終わります。")),
		para(line(0.80, head+0.03, "次の段落"), line(0.78, head, "です。")),
	}}
}

// 実ページの段落はすべて字下げで始まる（「「」始まりの浅い字下げも含む）ので、
// どれも「列の頭から始まる＝前ページの続き」と判定してはいけない。
func TestStartsAtLineHeadRealIndents(t *testing.T) {
	p := loadFixture(t, "c_inv")
	for _, para := range p.Paragraphs {
		if !isTTB(para) || para.IsTitle {
			continue
		}
		if startsAtLineHead(para, p.Paragraphs, true, p) {
			t.Errorf("字下げを見落とした: %q", para.Text[:min(len(para.Text), 30)])
		}
	}
}

func TestBuildMergesAcrossPages(t *testing.T) {
	prev := loadFixture(t, "fig3")
	md, review := Build("サンプル書籍", []BookPage{
		{Number: 1, OCR: prev},
		{Number: 2, OCR: contPage(false)},
	})
	if !strings.HasPrefix(md, "# サンプル書籍\n\n<!-- p.1 -->\n\n## 【第1章】 作る架空\n\n") {
		t.Errorf("先頭の形が違う:\n%s", md[:min(len(md), 200)])
	}
	if !strings.Contains(md, "容に意味はありませんが次のページで文が終わります。\n\n") {
		t.Errorf("ページをまたぐ段落がつながっていない:\n%s", md)
	}
	// つないだ段落の後ろに図の文字（引用）と次ページの区切りが来る
	i := strings.Index(md, "ありませんが")
	j := strings.Index(md, "<!-- p.2 -->")
	k := strings.Index(md, "次の段落です。")
	if !(i < j && j < k) {
		t.Errorf("順序が違う: merged=%d marker=%d next=%d", i, j, k)
	}
	if len(review) != 0 {
		t.Errorf("review = %v", review)
	}
}

func TestBuildDoesNotMergeNewParagraph(t *testing.T) {
	md, _ := Build("", []BookPage{
		{Number: 1, OCR: loadFixture(t, "fig3")},
		{Number: 2, OCR: contPage(true)}, // 字下げあり = 新しい段落
	})
	if strings.Contains(md, "ありませんが") {
		t.Errorf("字下げで始まる段落をつないだ:\n%s", md)
	}
	md, _ = Build("", []BookPage{
		{Number: 1, OCR: loadFixture(t, "hs_3")},
		{Number: 2, OCR: contPage(false)},
	})
	if strings.Contains(md, "キクケコんが") {
		t.Errorf("漫画の吹き出しをつないだ:\n%s", md)
	}
}

// 段落が3ページにまたがる場合（真ん中のページ全体が1段落の途中）もつなぐ。
func TestBuildMergesThroughFullPage(t *testing.T) {
	full := func(text string) ocr.Page {
		var paras []ocr.Paragraph
		r := ocr.Rect{0.5, 0.079, 0.0174, 0.85}
		paras = append(paras, ocr.Paragraph{Text: text, BBox: r, Direction: ocr.DirTTB, Confidence: 0.4,
			Lines: []ocr.Line{{Text: text, BBox: r, Direction: ocr.DirTTB, Confidence: 0.4}}})
		return ocr.Page{Width: 1470, Height: 895, Paragraphs: paras}
	}
	md, _ := Build("", []BookPage{
		{Number: 1, OCR: full("一ページ目の")},
		{Number: 2, OCR: full("二ページ目の")},
		{Number: 3, OCR: full("三ページ目。")},
	})
	if !strings.Contains(md, "一ページ目の二ページ目の三ページ目。") {
		t.Errorf("3ページにまたがる段落がつながっていない:\n%s", md)
	}
}

// ltrPara は横書きの段落を合成する。lines は各行の [x, y, w] で、行の高さは 0.03。
func ltrPara(text string, lines ...[3]float64) ocr.Paragraph {
	const h = 0.03
	var ls []ocr.Line
	x0, y0, x1, y1 := 1.0, 1.0, 0.0, 0.0
	for _, l := range lines {
		r := ocr.Rect{l[0], l[1], l[2], h}
		ls = append(ls, ocr.Line{Text: text, BBox: r, Direction: ocr.DirLTR, Confidence: 0.4})
		x0, y0 = min(x0, r.X()), min(y0, r.Y())
		x1, y1 = max(x1, r.Right()), max(y1, r.Bottom())
	}
	return ocr.Paragraph{Text: text, BBox: ocr.Rect{x0, y0, x1 - x0, y1 - y0},
		Direction: ocr.DirLTR, Confidence: 0.4, Lines: ls}
}

// 章扉のページ: 本文が短い章題だけでも、柱（横書きの書名）の文字数に引きずられて
// 横書きページと判定しない。柱を捨て、章題を見出しにする。
func TestLayoutChapterOpenerWithRunningHead(t *testing.T) {
	head := ltrPara("サンプル書籍", [3]float64{0.45, 0.031, 0.08})
	title := ocr.Paragraph{Text: "はじめに", BBox: ocr.Rect{0.8, 0.1, 0.02, 0.3},
		Direction: ocr.DirTTB, IsTitle: true, Confidence: 0.4,
		Lines: []ocr.Line{{Text: "はじめに", BBox: ocr.Rect{0.8, 0.1, 0.02, 0.3}, Direction: ocr.DirTTB, Confidence: 0.4}}}
	p := ocr.Page{Width: 1470, Height: 895, Paragraphs: []ocr.Paragraph{head, title}}

	l := layoutPage(p)
	if !l.vertical {
		t.Fatal("柱の文字数で横書きページと判定した")
	}
	if h := texts(l, kindHeading); len(h) != 1 || h[0] != "はじめに" {
		t.Errorf("見出し = %q", h)
	}
	if containsText(l, "サンプル書籍") {
		t.Errorf("柱が残っている: %+v", l.blocks)
	}
}

// horizontalPage は横書きの本の1ページを合成する。上端に柱（書名）、下端にページ番号があり、
// 本文の段落は x=0.1 から始まる（字下げは +0.02 ≒ 1文字: 行の高さ 0.03 ÷ 縦横比 1.64）。
func horizontalPage(num string, paras ...ocr.Paragraph) ocr.Page {
	all := []ocr.Paragraph{ltrPara("Clean Architecture", [3]float64{0.4, 0.03, 0.2})}
	all = append(all, paras...)
	all = append(all, ltrPara(num, [3]float64{0.49, 0.94, 0.02}))
	return ocr.Page{Width: 1470, Height: 895, Paragraphs: all}
}

// 横書きの本: 柱とページ番号を捨て、ページをまたぐ段落をつなぐ。
func TestBuildHorizontalPages(t *testing.T) {
	p1 := horizontalPage("15",
		ltrPara("A short paragraph.", [3]float64{0.12, 0.1, 0.3}),
		// 字下げで始まり、最後の行が右端まで埋まって文の途中で終わる
		ltrPara("This sentence starts on one page and", [3]float64{0.12, 0.2, 0.78}, [3]float64{0.1, 0.24, 0.8}),
	)
	p2 := horizontalPage("16",
		// 字下げなしで始まる = 前のページの続き
		ltrPara("ends on the next.", [3]float64{0.1, 0.1, 0.3}),
		ltrPara("Next paragraph.", [3]float64{0.12, 0.2, 0.3}),
	)

	l := layoutPage(p1)
	if l.vertical {
		t.Fatal("横書きページを縦書きと判定した")
	}
	if containsText(l, "Clean Architecture") || containsText(l, "15") {
		t.Errorf("柱かページ番号が残っている: %+v", l.blocks)
	}
	if !l.endsMidParagraph || !layoutPage(p2).startsMidParagraph {
		t.Errorf("段落連結の判定: ends=%v starts=%v", l.endsMidParagraph, layoutPage(p2).startsMidParagraph)
	}

	md, _ := Build("", []BookPage{{Number: 1, OCR: p1}, {Number: 2, OCR: p2}})
	if !strings.Contains(md, "This sentence starts on one page and ends on the next.\n\n<!-- p.2 -->\n\nNext paragraph.") {
		t.Errorf("横書きページをまたぐ段落がつながっていない:\n%s", md)
	}
}

// 柱とページ番号だけのページ（白紙ページなど）は本文なしとして扱う。
func TestLayoutOnlyFurniture(t *testing.T) {
	p := ocr.Page{Width: 1470, Height: 895, Paragraphs: []ocr.Paragraph{
		ltrPara("Kindle", [3]float64{0.48, 0.01, 0.04}),
		ltrPara("15", [3]float64{0.49, 0.94, 0.02}),
	}}
	if l := layoutPage(p); l.hasText() {
		t.Errorf("柱だけのページに本文がある: %+v", l.blocks)
	}
}

func TestBuildReviewPages(t *testing.T) {
	low := contPage(false)
	for i := range low.Paragraphs {
		for j := range low.Paragraphs[i].Lines {
			low.Paragraphs[i].Lines[j].Confidence = 0.05
		}
	}
	_, review := Build("", []BookPage{
		{Number: 1, OCR: loadFixture(t, "c_inv")},
		{Number: 2, OCR: ocr.Page{Error: "画像を読み込めません"}},
		{Number: 3, OCR: ocr.Page{Width: 10, Height: 10}, Blank: true},  // 白紙: 確認不要
		{Number: 4, OCR: ocr.Page{Width: 10, Height: 10}, Blank: false}, // 文字なし: 要確認
		{Number: 5, OCR: low},
	})
	want := []int{2, 4, 5}
	if len(review) != len(want) {
		t.Fatalf("review = %v, want %v", review, want)
	}
	for i := range want {
		if review[i] != want[i] {
			t.Fatalf("review = %v, want %v", review, want)
		}
	}
}

func TestFixDots(t *testing.T) {
	cases := map[string]string{
		"あれ・・・どこ？": "あれ……どこ？",
		"ジョン・スミス":  "ジョン・スミス",
		"待て・・":     "待て……",
		"半角･･･も":   "半角……も",
		"例えば……など":  "例えば……など",
	}
	for in, want := range cases {
		if got := fixDots(in); got != want {
			t.Errorf("fixDots(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndsSentence(t *testing.T) {
	for s, want := range map[string]bool{
		"です。": true, "でしょう？」": true, "Yes!": true, "ほと": false, "つまり、": false, "": true,
	} {
		if got := endsSentence(s); got != want {
			t.Errorf("endsSentence(%q) = %v", s, got)
		}
	}
}

func TestJoinText(t *testing.T) {
	if got := joinText("ほと", "んど"); got != "ほとんど" {
		t.Errorf("got %q", got)
	}
	if got := joinText("in the", "world"); got != "in the world" {
		t.Errorf("got %q", got)
	}
}

func TestListPagesNumericOrder(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"page_10000.png", "page_9999.png", "page_0002.png", "poll.png", "note.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, nums, err := listPages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(nums) != 3 || nums[0] != 2 || nums[1] != 9999 || nums[2] != 10000 {
		t.Errorf("nums = %v", nums)
	}
	if _, _, err := listPages(t.TempDir()); err == nil {
		t.Error("空のフォルダでエラーにならない")
	}
}

// ttbPara は縦書きの段落を合成する（1列）。
func ttbPara(text string, x, y, h float64) ocr.Paragraph {
	r := ocr.Rect{x, y, 0.02, h}
	return ocr.Paragraph{Text: text, BBox: r, Direction: ocr.DirTTB, Confidence: 0.4,
		Lines: []ocr.Line{{Text: text, BBox: r, Direction: ocr.DirTTB, Confidence: 0.4}}}
}

// 縦書きの本の図だけのページ: 図の横書きラベルで横書きページと判定されても、
// 縦書きページで見つけた長い柱（30文字超）と似た段落は柱として捨てる。
// OCR が柱を毎回少しずつ読み違えても（2文字違い）同じ柱とみなす。
func TestBuildDropsLongRunningHeadOnFigurePage(t *testing.T) {
	const head = "サンプルの長い書名をここに置く――副題もつけて三十文字を超える柱の例"
	const misread = "サンプルの長い書名をここに買く――副題もつけて三十文学を超える柱の例"
	text := ocr.Page{Width: 1146, Height: 1410, Paragraphs: []ocr.Paragraph{
		ltrPara(head, [3]float64{0.3, 0.04, 0.4}),
		ttbPara("本文の一列目です。", 0.8, 0.1, 0.8),
		ttbPara("本文の二列目です。", 0.7, 0.1, 0.8),
	}}
	figure := ocr.Page{Width: 1146, Height: 1410, Paragraphs: []ocr.Paragraph{
		ltrPara(misread, [3]float64{0.3, 0.04, 0.4}),
		ltrPara("図のタイトル", [3]float64{0.3, 0.35, 0.4}),
		ltrPara("図のラベルその一", [3]float64{0.2, 0.5, 0.3}),
		// 柱と似ていない長い段落は、帯の中でも本文に残す
		ltrPara("帯の中にあるが柱とは似ていない長い文章で三十文字をこえているもの", [3]float64{0.1, 0.94, 0.8}),
	}}
	if isVerticalPage(figure) {
		t.Fatal("図のページは横書きページと判定される前提")
	}

	md, _ := Build("", []BookPage{{Number: 1, OCR: text}, {Number: 2, OCR: figure}})
	if strings.Contains(md, "サンプルの長い書名") {
		t.Errorf("柱が残っている:\n%s", md)
	}
	for _, want := range []string{"図のタイトル", "図のラベルその一", "柱とは似ていない"} {
		if !strings.Contains(md, want) {
			t.Errorf("%q が消えた:\n%s", want, md)
		}
	}
}

func TestMatchesKnownHead(t *testing.T) {
	heads := []string{"冒頭の書名と副題からなる長い柱"}
	for _, tt := range []struct {
		s    string
		want bool
	}{
		{"冒頭の書名と副題からなる長い柱", true},
		{"冒頭の書名と副題から なる長い柱", true}, // 空白は無視
		{"冒頭の書名と副題からなる長い枝", true}, // 1文字違い
		{"まったく別の本文の一文です。", false},
		{"", false},
	} {
		if got := matchesKnownHead(tt.s, heads); got != tt.want {
			t.Errorf("matchesKnownHead(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
	if matchesKnownHead("何か", nil) {
		t.Error("柱が無いのに一致した")
	}
}
