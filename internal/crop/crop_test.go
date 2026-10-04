package crop

import (
	"image"
	"image/color"
	"testing"
)

// テスト画像はすべて合成で作る（書籍の実画像はリポジトリに入れない）。
// 実際のウィンドウ画像を縮小した寸法感（タイトルバー28px・左右の大きな余白）に合わせている。

var (
	black     = color.NRGBA{0, 0, 0, 255}
	white     = color.NRGBA{255, 255, 255, 255}
	titleGray = color.NRGBA{40, 40, 41, 255}
)

// newFilled は c で塗りつぶした w×h の画像を返す。
func newFilled(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fill(img, img.Bounds(), c)
	return img
}

func fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// drawTitleBar は上端 h 行に macOS 風のタイトルバーを描く。
// 最上段のハイライト行・信号ボタン（赤黄緑）・中央のタイトル文字まで再現し、
// 「標準偏差は大きいが大半は一様な灰色」の行になるようにする。
func drawTitleBar(img *image.NRGBA, h int) {
	w := img.Bounds().Dx()
	fill(img, image.Rect(0, 0, w, h), titleGray)
	fill(img, image.Rect(0, 0, w, 1), color.NRGBA{62, 62, 62, 255})
	for i, c := range []color.NRGBA{{255, 95, 87, 255}, {254, 188, 46, 255}, {40, 200, 64, 255}} {
		x := 8 + i*20
		fill(img, image.Rect(x, 8, x+12, 20), c)
	}
	fill(img, image.Rect(w/2-20, 10, w/2+20, 18), color.NRGBA{200, 200, 200, 255})
	// 角の透明画素（alpha=0）
	fill(img, image.Rect(0, 0, 3, 3), color.NRGBA{})
	fill(img, image.Rect(w-3, 0, w, 3), color.NRGBA{})
}

// drawText は r の中に「文字の行」を模した横縞を描く（行間は背景色のまま）。
func drawText(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if (y-r.Min.Y)%10 < 6 || y == r.Max.Y-1 {
			fill(img, image.Rect(r.Min.X, y, r.Max.X, y+1), c)
		}
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name      string
		img       func() *image.NRGBA
		want      image.Rectangle
		wantTitle int // 除外したタイトルバーの高さ
	}{
		{
			name: "灰色タイトルバー＋黒背景＋中央の白い本文矩形",
			img: func() *image.NRGBA {
				img := newFilled(600, 400, black)
				drawTitleBar(img, 28)
				fill(img, image.Rect(150, 60, 450, 380), white)
				return img
			},
			want:      image.Rect(150, 60, 450, 380),
			wantTitle: 28,
		},
		{
			name: "白背景＋黒文字（ライトテーマ、タイトルバーあり）",
			img: func() *image.NRGBA {
				img := newFilled(600, 400, white)
				drawTitleBar(img, 28)
				drawText(img, image.Rect(100, 50, 500, 370), black)
				return img
			},
			want:      image.Rect(100, 50, 500, 370),
			wantTitle: 28,
		},
		{
			name: "タイトルバー無し（フルスクリーン）で本文が上端近くから始まる",
			img: func() *image.NRGBA {
				img := newFilled(600, 400, black)
				drawText(img, image.Rect(120, 5, 480, 395), white)
				return img
			},
			want:      image.Rect(120, 5, 480, 395),
			wantTitle: 0,
		},
		{
			name: "2xのタイトルバー（56px）も帯として除外する",
			img: func() *image.NRGBA {
				img := newFilled(1200, 800, black)
				drawTitleBar(img, 56)
				drawText(img, image.Rect(300, 100, 900, 760), white)
				return img
			},
			want:      image.Rect(300, 100, 900, 760),
			wantTitle: 56,
		},
		{
			name: "下端の角の透明画素は背景扱いで矩形を広げない",
			img: func() *image.NRGBA {
				img := newFilled(600, 400, black)
				drawTitleBar(img, 28)
				drawText(img, image.Rect(150, 60, 450, 380), white)
				fill(img, image.Rect(0, 390, 10, 400), color.NRGBA{255, 255, 255, 0})
				fill(img, image.Rect(590, 390, 600, 400), color.NRGBA{255, 255, 255, 0})
				return img
			},
			want:      image.Rect(150, 60, 450, 380),
			wantTitle: 28,
		},
		{
			name: "孤立したゴミ画素（しきい値以下）は無視する",
			img: func() *image.NRGBA {
				img := newFilled(600, 400, black)
				drawTitleBar(img, 28)
				fill(img, image.Rect(150, 60, 450, 380), white)
				img.SetNRGBA(5, 200, white)
				img.SetNRGBA(300, 395, white)
				return img
			},
			want:      image.Rect(150, 60, 450, 380),
			wantTitle: 28,
		},
		{
			name: "上端が一様な灰色でも高さの1/10以上続くならタイトルバーとみなさない",
			img: func() *image.NRGBA {
				// フルスクリーンで灰色の地の図版を表示した状態を想定
				img := newFilled(600, 400, black)
				fill(img, image.Rect(0, 0, 600, 100), titleGray)
				fill(img, image.Rect(100, 100, 500, 380), white)
				return img
			},
			want:      image.Rect(0, 0, 600, 380),
			wantTitle: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, title, ok := Detect(tt.img())
			if !ok {
				t.Fatal("検出に失敗した")
			}
			if got != tt.want {
				t.Errorf("Detect = %v, want %v", got, tt.want)
			}
			if title != tt.wantTitle {
				t.Errorf("titleBar = %d, want %d", title, tt.wantTitle)
			}
		})
	}
}

func TestDetectBlank(t *testing.T) {
	tests := []struct {
		name string
		img  *image.NRGBA
	}{
		{"真っ白なページ", newFilled(600, 400, white)},
		{"タイトルバーだけで本文が真っ黒", func() *image.NRGBA {
			img := newFilled(600, 400, black)
			drawTitleBar(img, 28)
			return img
		}()},
		{"空の画像", image.NewNRGBA(image.Rect(0, 0, 0, 0))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r, _, ok := Detect(tt.img); ok {
				t.Errorf("白紙なのに検出された: %v", r)
			}
		})
	}
}

func TestUnion(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 800)
	pad := 8 // 短辺 800 の1%

	t.Run("1枚だけ全面にUIが写った外れ値は無視する", func(t *testing.T) {
		rects := []image.Rectangle{
			image.Rect(200, 100, 800, 700),
			image.Rect(205, 100, 795, 690), // 行の折り返しによる小さな揺れ
			image.Rect(200, 102, 800, 700),
			image.Rect(400, 100, 800, 300), // 章末の短いページ（内側の揺れ）
			image.Rect(0, 0, 1000, 800),    // ツールバーとスライダーが写ったページ
		}
		want := image.Rect(200-pad, 100-pad, 800+pad, 700+pad)
		if got := Union(rects, bounds, 0); got != want {
			t.Errorf("Union = %v, want %v", got, want)
		}
	})

	t.Run("中央値から少し外までのページは本文を切らないよう含める", func(t *testing.T) {
		rects := []image.Rectangle{
			image.Rect(200, 100, 800, 700),
			image.Rect(200, 100, 800, 700),
			image.Rect(180, 100, 820, 715), // 3% 以内の揺れ
		}
		want := image.Rect(180-pad, 100-pad, 820+pad, 715+pad)
		if got := Union(rects, bounds, 0); got != want {
			t.Errorf("Union = %v, want %v", got, want)
		}
	})

	t.Run("複数ページが届く外側の位置は本物の内容として含める", func(t *testing.T) {
		// 固定レイアウトの扉ページが上下いっぱいまで広がるケース
		rects := []image.Rectangle{
			image.Rect(200, 100, 800, 700),
			image.Rect(200, 100, 800, 700),
			image.Rect(200, 100, 800, 700),
			image.Rect(200, 0, 800, 800),
			image.Rect(200, 0, 800, 800),
		}
		want := image.Rect(200-pad, 0, 800+pad, 800)
		if got := Union(rects, bounds, 0); got != want {
			t.Errorf("Union = %v, want %v", got, want)
		}
	})

	t.Run("有効な矩形が無ければ切り出さない", func(t *testing.T) {
		if got := Union(nil, bounds, 0); got != bounds {
			t.Errorf("Union = %v, want %v", got, bounds)
		}
		if got := Union([]image.Rectangle{{}}, bounds, 0); got != bounds {
			t.Errorf("空矩形のみ: Union = %v, want %v", got, bounds)
		}
	})

	t.Run("パディングは画像の範囲内に収める", func(t *testing.T) {
		rects := []image.Rectangle{image.Rect(2, 3, 998, 797)}
		if got := Union(rects, bounds, 0); got != bounds {
			t.Errorf("Union = %v, want %v", got, bounds)
		}
	})
}

func TestForPage(t *testing.T) {
	bounds := image.Rect(0, 0, 1000, 800)
	union := image.Rect(192, 92, 808, 708)

	t.Run("Unionに収まるページはUnionのまま", func(t *testing.T) {
		if got := ForPage(union, image.Rect(200, 100, 800, 700), bounds, 0); got != union {
			t.Errorf("ForPage = %v, want %v", got, union)
		}
	})

	t.Run("はみ出す表紙はそのページだけ広げる", func(t *testing.T) {
		// 表紙がウィンドウの高さいっぱいに広がるケース（パディングは画像内に切り詰め）
		want := image.Rect(192, 0, 808, 800)
		if got := ForPage(union, image.Rect(250, 0, 750, 800), bounds, 0); got != want {
			t.Errorf("ForPage = %v, want %v", got, want)
		}
	})
}

// TestPaddingStopsAtTitleBar は、Union・ForPage のパディングが Detect の除外した
// タイトルバーの中まで広がらないことを確かめる。実画像（1x・タイトルバー 0〜27 行）では、
// 表紙だけ本文がタイトルバー直下（28行目）から始まり、それが Union の上端に採用された
// うえで 14px のパディングが足され、全ページの上端 14 行にタイトルバーの帯と信号ボタンの
// 下半分が写っていた。
func TestPaddingStopsAtTitleBar(t *testing.T) {
	const w, h, title = 600, 400, 28
	bounds := image.Rect(0, 0, w, h)
	page := func(top int) *image.NRGBA {
		img := newFilled(w, h, black)
		drawTitleBar(img, title)
		drawText(img, image.Rect(150, top, 450, 380), white)
		return img
	}

	// 表紙（タイトルバー直下から本文）＋本文ページ。表紙は中央値から 3% 以内なので Union が採用する
	var rects []image.Rectangle
	var titles []int
	for _, top := range []int{title, 36, 36, 36, 36} {
		r, tb, ok := Detect(page(top))
		if !ok {
			t.Fatal("検出に失敗した")
		}
		rects = append(rects, r)
		titles = append(titles, tb)
	}
	if rects[0].Min.Y != title || titles[0] != title {
		t.Fatalf("前提: 表紙の上端 = %d・titleBar = %d, want %d", rects[0].Min.Y, titles[0], title)
	}

	u := Union(rects, bounds, title)
	if u.Min.Y != title {
		t.Errorf("Union の上端 = %d, want %d（タイトルバーの中まで広げない）", u.Min.Y, title)
	}
	// 上以外の辺には従来どおりパディングが付く
	if pad := padding(bounds); u.Min.X != 150-pad || u.Max.X != 450+pad || u.Max.Y != 380+pad {
		t.Errorf("Union = %v, 上以外の辺にパディング %d が付いていない", u, pad)
	}

	t.Run("ForPage も広げたページの上端をタイトルバーで止める", func(t *testing.T) {
		narrow := image.Rect(150, 60, 450, 380) // 本文ページだけで決めた共通矩形を想定
		got := ForPage(narrow, rects[0], bounds, titles[0])
		if got.Min.Y != title {
			t.Errorf("ForPage の上端 = %d, want %d", got.Min.Y, title)
		}
	})

	t.Run("タイトルバーにかかる内容は削らない", func(t *testing.T) {
		// 代表値の titleBar がそのページの実際の帯より厚くても、検出した内容の上端までは残す
		got := Union([]image.Rectangle{image.Rect(150, 20, 450, 380)}, bounds, title)
		if got.Min.Y != 20 {
			t.Errorf("Union の上端 = %d, want 20", got.Min.Y)
		}
	})

	t.Run("タイトルバーが無ければ従来どおりパディングを付ける", func(t *testing.T) {
		got := Union([]image.Rectangle{image.Rect(150, 30, 450, 380)}, bounds, 0)
		if want := 30 - padding(bounds); got.Min.Y != want {
			t.Errorf("Union の上端 = %d, want %d", got.Min.Y, want)
		}
	})
}

func TestApply(t *testing.T) {
	img := newFilled(100, 80, black)
	for y := 0; y < 80; y++ {
		for x := 0; x < 100; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}

	r := image.Rect(10, 20, 60, 70)
	got := Apply(img, r)
	if got.Bounds() != r {
		t.Fatalf("Bounds = %v, want %v", got.Bounds(), r)
	}
	sub, ok := got.(*image.NRGBA)
	if !ok {
		t.Fatalf("型が変わった: %T", got)
	}
	// SubImage は元のバッファを共有する（コピーしていない）
	if &sub.Pix[0] != &img.Pix[img.PixOffset(10, 20)] {
		t.Error("SubImage ではなくコピーになっている")
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if got.At(x, y) != img.At(x, y) {
				t.Fatalf("(%d,%d) の画素が変わった", x, y)
			}
		}
	}

	t.Run("はみ出した矩形は画像内に切り詰める", func(t *testing.T) {
		got := Apply(img, image.Rect(-10, -10, 50, 200))
		if want := image.Rect(0, 0, 50, 80); got.Bounds() != want {
			t.Errorf("Bounds = %v, want %v", got.Bounds(), want)
		}
	})

	t.Run("重ならない矩形なら元画像をそのまま返す", func(t *testing.T) {
		if got := Apply(img, image.Rect(200, 200, 300, 300)); got.Bounds() != img.Bounds() {
			t.Errorf("Bounds = %v, want %v", got.Bounds(), img.Bounds())
		}
	})
}
