package pdf

import (
	"fmt"
	"image"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/imutaroh/kindle-screenshot-app/internal/crop"
)

// maxCropAreaRatio は「トリミングする意味がある」とみなす切り出し矩形の面積比の上限。
// Union が画像のこれ以上を占めるなら、切っても余白はほとんど減らない。そのような本は
// たいてい全ページに Page Flip のツールバー等が写っているか、余白の無いレイアウトなので、
// 何も切らずに埋め込み、警告で知らせる。
const maxCropAreaRatio = 0.95

// maxDetectWorkers は余白検出を並列に走らせる goroutine 数の上限。
// 1ページのデコード結果は 2x で約21MB（2940x1846 の NRGBA）になるので、
// CPU 数が多くてもメモリを食いすぎないよう抑える。
const maxDetectWorkers = 4

// cropPlan は本1冊分のトリミング計画。
type cropPlan struct {
	rects   []image.Rectangle // ページごとの切り出し矩形（空ならページ全体を埋め込む）
	applied bool              // トリミングを適用するか
	union   image.Rectangle   // 全ページ共通の矩形（applied=false なら空）
	bounds  image.Rectangle   // union の基準にしたページの画像サイズ
	note    string            // 適用しなかった理由、または適用時の補足
}

// detection は1ページ分の余白検出結果。
type detection struct {
	bounds image.Rectangle // ページ画像の範囲
	rect   image.Rectangle // 本文の外接矩形（ok=false なら空）
	title  int             // 除外したタイトルバーの高さ（px。無ければ 0）
	ok     bool            // 本文を検出できたか（白紙ページは false）
}

// planCrop は全ページで crop.Detect を走らせ、crop.Union で全ページ共通の矩形を決める。
//
// ページの画像サイズが揃っていない場合（取り込み途中でウィンドウをリサイズした等）は、
// 最も多いサイズのページだけで Union を求めてトリミングし、それ以外のページは切らずに
// 埋め込む。座標系の違う矩形を混ぜて Union を求めると、どのページにも合わない矩形になるため。
//
// 本文が Union からはみ出すページ（表紙など）は crop.ForPage でそのページだけ広げる。
// そのページだけ MediaBox が大きくなるが、本文が欠けるよりよい。
func planCrop(pngs []string, progress func(done, total int)) (cropPlan, error) {
	n := len(pngs)
	plan := cropPlan{rects: make([]image.Rectangle, n)}

	dets, err := detectAll(pngs, progress)
	if err != nil {
		return plan, err
	}

	// 最も多い画像サイズを基準にする（同数なら先に現れたサイズ）
	counts := map[image.Point]int{}
	var mode image.Point
	for _, d := range dets {
		sz := d.bounds.Size()
		counts[sz]++
		if counts[sz] > counts[mode] {
			mode = sz
		}
	}

	var rs []image.Rectangle
	var titles []int
	for _, d := range dets {
		if d.bounds.Size() == mode {
			if plan.bounds.Empty() {
				plan.bounds = d.bounds
			}
			titles = append(titles, d.title)
			if d.ok {
				rs = append(rs, d.rect)
			}
		}
	}
	if len(rs) == 0 {
		plan.note = "全ページで本文を検出できなかったため、余白のトリミングはしませんでした"
		return plan, nil
	}

	// タイトルバーの高さは本1冊分の中央値を使う。Page Flip のツールバーが写ったページは
	// 帯がツールバーと一体化して厚く検出されるので、最大値を使うとそれ1枚で全ページの
	// 上端（パディング）が削られる。
	union := crop.Union(rs, plan.bounds, median(titles))
	ratio := area(union) / area(plan.bounds)
	if ratio > maxCropAreaRatio {
		plan.note = fmt.Sprintf("トリミングしても余白がほとんど減らない（残る範囲が画像の %.0f%%）ため、余白のトリミングはしませんでした。"+
			"Page Flip 表示やツールバーが写っている可能性があります", ratio*100)
		return plan, nil
	}

	plan.applied = true
	plan.union = union
	mismatched, widened := 0, 0
	for i, d := range dets {
		switch {
		case d.bounds.Size() != mode:
			mismatched++ // plan.rects[i] は空のまま＝ページ全体を埋め込む
		case d.ok:
			r := crop.ForPage(union, d.rect, d.bounds, d.title)
			if r != union {
				widened++
			}
			plan.rects[i] = r
		default:
			plan.rects[i] = union // 白紙ページは共通の矩形で切る
		}
	}

	var notes []string
	if widened > 0 {
		notes = append(notes, fmt.Sprintf("本文が共通の範囲からはみ出す %d ページは、そのページだけ範囲を広げました", widened))
	}
	if mismatched > 0 {
		notes = append(notes, fmt.Sprintf("画像サイズが他と異なる %d ページはトリミングせずに埋め込みました", mismatched))
	}
	plan.note = strings.Join(notes, "。")
	return plan, nil
}

// detectAll は全ページの余白検出を並列に行う。結果はページ順に並ぶ。
// progress は呼び出し元の goroutine（この関数を呼んだ goroutine）からだけ呼ぶ。
func detectAll(pngs []string, progress func(done, total int)) ([]detection, error) {
	n := len(pngs)
	dets := make([]detection, n)
	errs := make([]error, n)

	jobs := make(chan int)
	finished := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(runtime.NumCPU(), maxDetectWorkers); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				dets[i], errs[i] = detectFile(pngs[i])
				finished <- i
			}
		}()
	}
	go func() {
		for i := range pngs {
			jobs <- i
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(finished)
	}()

	done := 0
	for range finished {
		done++
		if progress != nil {
			progress(done, n)
		}
	}

	for _, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("余白の検出に失敗しました: %w", err)
		}
	}
	return dets, nil
}

// detectFile は1ページを読み込んで余白を検出する。
func detectFile(path string) (detection, error) {
	img, err := loadPNG(path)
	if err != nil {
		return detection{}, err
	}
	r, title, ok := crop.Detect(img)
	return detection{bounds: img.Bounds(), rect: r, title: title, ok: ok}, nil
}

// median は vs の中央値を返す（偶数個なら小さい側。空なら 0）。vs は並べ替えない。
func median(vs []int) int {
	if len(vs) == 0 {
		return 0
	}
	s := append([]int(nil), vs...)
	sort.Ints(s)
	return s[(len(s)-1)/2]
}

func area(r image.Rectangle) float64 {
	return float64(r.Dx()) * float64(r.Dy())
}
