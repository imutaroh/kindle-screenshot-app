// Package crop は Kindle ウィンドウのスクリーンショットから、ページ本体以外
// （上端のタイトルバー・左右上下の背景色の余白）を切り落とすための矩形を求める。
//
// screencapture -l で撮ったウィンドウ画像には、本文の外側にタイトルバー（1xで28px、
// 2xで56px の灰色の帯。フルスクリーン時は無い）と、背景色（ダークなら黒、ライトなら白）
// の大きな余白が写る。これらをそのまま PDF にすると、紙面のかなりの割合が無駄な余白になる。
//
// 切り出しは SubImage で行い、画素値には一切手を加えない（無劣化）。
// 書名や位置表示などのヘッダ/フッタは除去しない。本文との区別を誤って本文を削る危険が、
// 数十pxの余白を詰める利益を上回るため。
package crop

import (
	"image"
	"image/draw"
	"sort"
)

// 判定しきい値。実画像（ダーク/ライト、1x/2x、Page Flip 表示）で確認した値。
const (
	// fgDiff は背景色との差（RGB各chの差の最大値）がこれを超える画素を前景とみなす値。
	// 変化検出と同じ 24 を使う（JPEG的なノイズは無いが、アンチエイリアスの薄い縁は拾わない）。
	fgDiff = 24

	// タイトルバー行の判定に使う値。macOS のダークアピアランスのタイトルバーは
	// 輝度 38〜58 程度の無彩色。ただし最上段の縁（1xで1行、2xで2行）だけは
	// ハイライトで 60〜97 まで明るくなるので、そこだけ上限を緩める。
	titleLumaMin     = 20
	titleLumaMax     = 90
	titleEdgeRows    = 2
	titleEdgeLumaMax = 140
	// titleNear は「行の中央値の色に近い」とみなす輝度差。
	titleNear = 10
	// titleChroma は無彩色（灰色）とみなす RGB の最大−最小。
	titleChroma = 12
	// titleUniformRatio は行内の不透明画素のうち「中央値に近い灰色」が占めるべき割合。
	// 信号ボタン（赤黄緑）や中央のタイトル文字が乗る行でも、2xの実画像で 0.92 以上だった。
	titleUniformRatio = 0.85
)

// Detect は1枚のウィンドウ画像から「内容の外接矩形」を求める。
//
// 手順:
//  1. 上端から続く「ほぼ一様な灰色の行」の帯をタイトルバーとして除外する。
//     高さを 28px と決め打ちしないのは、Retina(56px) やフルスクリーン（帯が無い）、
//     Page Flip 表示（ツールバーと一体化して厚くなる）に対応するため。
//  2. 残り領域の外周画素の最頻色を背景色とする。
//  3. 背景色との差が fgDiff を超える画素を前景とし、前景画素数がしきい値
//     （max(2, 辺長の0.2%)）を超える最初/最後の行・列を外接矩形とする。
//     しきい値を設けるのは、ウィンドウ縁の1px線や孤立したゴミ画素で矩形が広がるのを防ぐため。
//
// alpha<255 の画素（ウィンドウの角の透明部分など）は背景として扱う。
// 前景が見つからない（白紙ページなど）ときは ok=false を返す。
//
// titleBar は手順1で除外したタイトルバーの高さ（px。無ければ 0）。ok=false でも返す。
// Union・ForPage に渡し、パディングがタイトルバーの中まで広がらないようにするために使う。
func Detect(img image.Image) (r image.Rectangle, titleBar int, ok bool) {
	b := img.Bounds()
	if b.Empty() {
		return image.Rectangle{}, 0, false
	}
	at := newSampler(img)

	titleBar = titleBarHeight(at, b)
	region := b
	region.Min.Y += titleBar
	if region.Empty() {
		return image.Rectangle{}, titleBar, false
	}

	bg, found := borderMode(at, region)
	if !found {
		return image.Rectangle{}, titleBar, false
	}

	rowCount := make([]int, region.Dy())
	colCount := make([]int, region.Dx())
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if isForeground(at(x, y), bg) {
				rowCount[y-region.Min.Y]++
				colCount[x-region.Min.X]++
			}
		}
	}

	// 行の前景数は行の長さ（幅）に対して、列の前景数は列の長さ（高さ）に対して評価する
	y0, y1, okY := span(rowCount, threshold(region.Dx()))
	x0, x1, okX := span(colCount, threshold(region.Dy()))
	if !okY || !okX {
		return image.Rectangle{}, titleBar, false
	}
	return image.Rect(
		region.Min.X+x0, region.Min.Y+y0,
		region.Min.X+x1+1, region.Min.Y+y1+1,
	), titleBar, true
}

// Union は本1冊分の各ページの矩形から、全ページ共通で使う切り出し矩形を求める。
// rects には Detect が成功したページの矩形だけを渡す（白紙ページは除外しておく）。
// 有効な矩形が1つも無いときは bounds（切り出さない）を返す。
//
// 設計: 単純な外接（全ページの最外値）にすると、マウスが乗って Page Flip の
// ツールバーやスライダーが写った1枚だけで、本全体の矩形がウィンドウ全面に広がってしまう。
// 逆に中央値だけを使うと、他より少しだけ外まで文字があるページ（行の折り返し位置の
// 揺れ、やや大きい図版など）の端が切れる。
//
// そこで辺ごとに、次の2つのうち外側の方を採用する。
//  1. 中央値から外側へ outlierTol 以内に収まる値のうち最も外側のもの。
//     本文の揺れ（数px〜十数px）はここで拾える。UI の写り込みはウィンドウ端まで
//     一気に広がる（実画像で辺長の8%以上）ので、ここでは除外される。
//  2. 外側から数えて minSupport 番目の値（＝少なくとも minSupport ページが到達している位置）。
//     固定レイアウトの本では、扉や図版のページだけ紙面が上下いっぱいまで広がり、
//     中央値から outlierTol を大きく超えることがある（実画像で辺長の6%強）。
//     こうした「複数ページに現れる本物の内容」を 1 だけで外れ値扱いすると本文が切れるため、
//     2ページ以上（長い本では1%以上）が同じ位置まで届いていれば本物とみなして含める。
//     UI の写り込みは通常1ページ単位の事故なのでこの条件を満たさない。
//     満たしてしまった場合も、切り落とし不足（余白が残る）側に倒れるだけで本文は失われない。
//
// 最後に短辺の1%のパディングを付けて、しきい値付近で落ちた薄い画素（ルビ・
// 傍点の先端など）が縁で欠けないようにする。ただし上側のパディングは、titleBar
// （Detect が除外したタイトルバーの高さ。本1冊分の代表値）より上には広げない。
// 本文がタイトルバーの直下から始まるページ（表紙など）があると、パディングが
// タイトルバーの中に入り、全ページの上端に灰色の帯と信号ボタンの一部が写るため。
func Union(rects []image.Rectangle, bounds image.Rectangle, titleBar int) image.Rectangle {
	var minX, minY, maxX, maxY []int
	for _, r := range rects {
		r = r.Intersect(bounds)
		if r.Empty() {
			continue
		}
		// 左端・上端は値が小さいほど外側なので、符号を反転して「大きいほど外側」に揃える
		minX = append(minX, -r.Min.X)
		minY = append(minY, -r.Min.Y)
		maxX = append(maxX, r.Max.X)
		maxY = append(maxY, r.Max.Y)
	}
	n := len(minX)
	if n == 0 {
		return bounds
	}

	support := max(minSupport, (n+99)/100)
	tolX := int(float64(bounds.Dx()) * outlierTol)
	tolY := int(float64(bounds.Dy()) * outlierTol)
	u := image.Rect(
		-outerEdge(minX, tolX, support), -outerEdge(minY, tolY, support),
		outerEdge(maxX, tolX, support), outerEdge(maxY, tolY, support),
	)

	return pad(u, bounds, titleBar)
}

// padding は Union・ForPage が矩形の外側に足す余白（短辺の1%、最低1px）。
func padding(bounds image.Rectangle) int {
	return max(1, min(bounds.Dx(), bounds.Dy())/100)
}

// pad は r の外側に padding を足して bounds に収める。上側だけは、タイトルバー
// （bounds の上端から titleBar px）の中までは広げない。r 自体がタイトルバーに
// かかっている場合（呼び出し側の titleBar がそのページの実際の帯より厚いなど）は、
// 内容を削らないよう r の上端で止める。
func pad(r, bounds image.Rectangle, titleBar int) image.Rectangle {
	p := r.Inset(-padding(bounds)).Intersect(bounds)
	if p.Empty() {
		return p
	}
	p.Min.Y = max(p.Min.Y, min(r.Min.Y, bounds.Min.Y+titleBar))
	return p
}

// ForPage は1ページ分の切り出し矩形を返す。own・titleBar はそのページの Detect の結果
// （ok=false のページは呼ばずに union をそのまま使う）。
//
// own（Union と同じパディング付き）が union に収まっていれば union を返す。
// はみ出す場合は union と own の外接矩形を返す。Union は外れ値に強くした代償として、
// 1ページだけ紙面がウィンドウいっぱいに広がる表紙などを UI の写り込みと区別できず
// 切ってしまう（実画像で表紙の上下が欠けた）。そのページだけ広げることで、
// ページサイズが揃わなくなる代わりに本文の欠けを防ぐ。
// UI の写り込みがあるページもこの規則で広がるが、余白が残るだけで本文は失われない。
// パディングの付け方（タイトルバーの中に広げない）は Union と同じ。
func ForPage(union, own, bounds image.Rectangle, titleBar int) image.Rectangle {
	own = pad(own, bounds, titleBar)
	if own.Empty() || own.In(union) {
		return union
	}
	return union.Union(own)
}

// outlierTol は Union で「中央値から外れすぎ」とみなす距離（画像の辺長に対する割合）。
// 実画像では、本文の外接矩形の辺のページ間の揺れは数px〜十数px程度、
// UI 写り込み時の広がりはウィンドウ端までの数百px（辺長の8%以上）だった。
const outlierTol = 0.03

// minSupport は、中央値から外れた値を「本物の内容」とみなすのに必要な最少ページ数。
const minSupport = 2

// outerEdge は「大きいほど外側」に揃えた1辺分の値 vs から、Union の採用値を返す。
// 中央値+tol 以内で最大の値と、大きい方から support 番目の値のうち大きい方。
func outerEdge(vs []int, tol, support int) int {
	s := append([]int(nil), vs...)
	sort.Sort(sort.Reverse(sort.IntSlice(s)))
	// 偶数個のときは内側寄りの値を使う（実在する値を返すため平均はとらない）
	med := s[len(s)/2]

	edge := med
	for _, v := range s {
		if v <= med+tol {
			edge = v // 降順なので最初に条件を満たした値が最大
			break
		}
	}
	if k := min(support, len(s)); s[k-1] > edge {
		edge = s[k-1]
	}
	return edge
}

// Apply は img を r で切り出す。SubImage で元の画素バッファを共有するので
// コピーも色変換も発生しない（無劣化）。r が画像外にはみ出す分は切り詰める。
// r と画像が重ならないときは img をそのまま返す。
func Apply(img image.Image, r image.Rectangle) image.Image {
	r = r.Intersect(img.Bounds())
	if r.Empty() {
		return img
	}
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return s.SubImage(r)
	}
	// SubImage を持たない稀な型だけコピーする。8bit の不透明画像なら RGBA への変換で値は変わらない
	dst := image.NewRGBA(r)
	draw.Draw(dst, r, img, r.Min, draw.Src)
	return dst
}

// rgba は 8bit に落とした1画素。alpha<255 は背景扱いにするだけなので
// premultiplied かどうかは区別しない（alpha=255 なら両者は一致する）。
type rgba struct{ r, g, b, a uint8 }

// newSampler は画素を読む関数を返す。image.Image.At は1画素ごとに color.Color の
// インタフェース値を作るため、2940x1846 の画像では遅い。png.Decode が返す主要な型は
// Pix を直接読み、それ以外だけ At にフォールバックする。
func newSampler(img image.Image) func(x, y int) rgba {
	switch p := img.(type) {
	case *image.NRGBA:
		return func(x, y int) rgba {
			i := p.PixOffset(x, y)
			return rgba{p.Pix[i], p.Pix[i+1], p.Pix[i+2], p.Pix[i+3]}
		}
	case *image.RGBA:
		return func(x, y int) rgba {
			i := p.PixOffset(x, y)
			return rgba{p.Pix[i], p.Pix[i+1], p.Pix[i+2], p.Pix[i+3]}
		}
	case *image.Gray:
		return func(x, y int) rgba {
			v := p.Pix[p.PixOffset(x, y)]
			return rgba{v, v, v, 255}
		}
	default:
		return func(x, y int) rgba {
			r, g, b, a := img.At(x, y).RGBA()
			return rgba{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
		}
	}
}

// titleBarHeight は上端から続くタイトルバーの帯の高さ（px）を返す。帯が無ければ 0。
//
// 「標準偏差が小さい行」ではなく「中央値に近い灰色の画素が大半を占める行」で判定する。
// タイトルバーには信号ボタンやタイトル文字が乗っており、それらを通る行は標準偏差が
// 実画像で 40 近くまで上がる一方、灰色の画素は依然として行の9割以上を占めるため。
//
// 帯が画像の高さの1/10以上続く場合は、タイトルバーではなく灰色の紙面
// （フルスクリーンで表示した図版など）とみなして除外しない。
func titleBarHeight(at func(x, y int) rgba, b image.Rectangle) int {
	limit := b.Dy() / 10
	n := 0
	for y := b.Min.Y; y < b.Max.Y && isTitleBarRow(at, b, y, y-b.Min.Y < titleEdgeRows); y++ {
		n++
		if n >= limit {
			return 0
		}
	}
	return n
}

// isTitleBarRow は y 行目が「ほぼ一様な灰色」かどうかを返す。
// edge は最上段の縁の行（ハイライトで明るい）かどうか。
func isTitleBarRow(at func(x, y int) rgba, b image.Rectangle, y int, edge bool) bool {
	var hist [256]int
	opaque := 0
	for x := b.Min.X; x < b.Max.X; x++ {
		c := at(x, y)
		if c.a != 255 {
			continue
		}
		hist[luma(c)]++
		opaque++
	}
	// 角の透明部分は数pxしかないので、行の半分以上が透明なら帯ではない
	if opaque == 0 || opaque*2 < b.Dx() {
		return false
	}

	med, acc := 0, 0
	for v, c := range hist {
		acc += c
		if acc*2 >= opaque {
			med = v
			break
		}
	}
	lumaMax := titleLumaMax
	if edge {
		lumaMax = titleEdgeLumaMax
	}
	if med < titleLumaMin || med > lumaMax {
		return false
	}

	near := 0
	for x := b.Min.X; x < b.Max.X; x++ {
		c := at(x, y)
		if c.a != 255 {
			continue
		}
		if absDiff(int(luma(c)), med) <= titleNear && chroma(c) <= titleChroma {
			near++
		}
	}
	return float64(near) >= float64(opaque)*titleUniformRatio
}

// borderMode は region の外周画素（不透明なもののみ）の最頻色を返す。
// 本文は余白の内側にあるので、外周はほぼ背景色で占められる。
func borderMode(at func(x, y int) rgba, region image.Rectangle) (rgba, bool) {
	counts := map[rgba]int{}
	add := func(x, y int) {
		if c := at(x, y); c.a == 255 {
			counts[c]++
		}
	}
	for x := region.Min.X; x < region.Max.X; x++ {
		add(x, region.Min.Y)
		add(x, region.Max.Y-1)
	}
	for y := region.Min.Y + 1; y < region.Max.Y-1; y++ {
		add(region.Min.X, y)
		add(region.Max.X-1, y)
	}

	var best rgba
	bestN := 0
	for c, n := range counts {
		// 同数のときに map の走査順で結果が変わらないよう、色の値でも順序を決める
		if n > bestN || (n == bestN && less(c, best)) {
			best, bestN = c, n
		}
	}
	return best, bestN > 0
}

// isForeground は c が背景色 bg と十分に異なる不透明画素かどうかを返す。
func isForeground(c, bg rgba) bool {
	if c.a != 255 {
		return false
	}
	d := max(absDiff(int(c.r), int(bg.r)), absDiff(int(c.g), int(bg.g)), absDiff(int(c.b), int(bg.b)))
	return d > fgDiff
}

// threshold は長さ length の行（列）を「内容あり」とみなす前景画素数の下限（これを超えたら内容あり）。
func threshold(length int) int {
	return max(2, length*2/1000)
}

// span は counts[i] > thr となる最初と最後の添字を返す。
func span(counts []int, thr int) (first, last int, ok bool) {
	first, last = -1, -1
	for i, c := range counts {
		if c > thr {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	return first, last, first >= 0
}

// luma は ITU-R BT.601 の重みで輝度（0〜255）を求める。
func luma(c rgba) uint8 {
	return uint8((299*int(c.r) + 587*int(c.g) + 114*int(c.b)) / 1000)
}

// chroma は RGB の最大−最小（0なら完全な無彩色）を返す。
func chroma(c rgba) int {
	return max(int(c.r), int(c.g), int(c.b)) - min(int(c.r), int(c.g), int(c.b))
}

func less(a, b rgba) bool {
	if a.r != b.r {
		return a.r < b.r
	}
	if a.g != b.g {
		return a.g < b.g
	}
	return a.b < b.b
}

func absDiff(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}
