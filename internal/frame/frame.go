// Package frame は Kindle ウィンドウのスクショ1枚を「比較用の縮小輝度画像」に
// 変換し、2枚の間で画面が変わったか（ページ送りが効いたか）・止まったか
// （描画が安定したか）を判定する。
//
// pHash（旧 internal/dedupe。削除済み）は画像の大まかな構図しか見ないため、縦書き本文の
// 別ページ同士を近距離と判定してしまうことがある（実機で確認済み）。
// ここでは代わりに「縮小グレースケールで輝度差が pixelTol を超えた画素の割合」を
// 使う。実機の計測値（幅512・|ΔY|>24）は次の通りで、桁が離れているため
// 単純な閾値で分けられる:
//   - 本物のページ送り : 最小でも 2.15%
//   - 本文1列だけの変化: 0.137%
//   - 静止した画面     : 0%（screencapture -l は静止ウィンドウに対し毎回
//     バイト単位で同一の PNG を返す）
package frame

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
)

const (
	// ChangedFrac は「前ページから変わった（ページ送りが効いた）」とみなす変化画素率の下限。
	// 本文1列だけの変化（0.137%）でも確実に超え、静止（0%）や数画素のノイズには
	// 反応しない値として、1列変化の約1/3に置いている。
	ChangedFrac = 0.0005

	// StableFrac は「直前のポーリングと同じ（描画が止まって安定した）」とみなす変化画素率の上限。
	// 静止画面は差分0になるのが実測なので、本来は0でもよいが、将来のアンチエイリアス
	// 揺れ等に備えて数十画素分（幅512で約15〜18画素）の余裕を持たせている。
	StableFrac = 0.0001

	// pixelTol は「この画素は変わった」とみなす輝度差（0〜255）。
	// PNG は可逆なので静止画面の差は0だが、縮小時の丸めや描画の微差を吸収する。
	pixelTol = 24

	// targetWidth は縮小後の幅。ROI がこれより狭いときは拡大せずそのまま使う。
	targetWidth = 512
)

// ROI（比較対象にする領域）。左右と下端は、ウィンドウサイズや Retina 倍率に依存しないよう
// 画素数ではなく比率で切り出す。上端だけは比率と画素数の両方で決める（下記）。
//
//   - 上端     : タイトルバー（28pt。フルスクリーン時は無い）と、マウスが乗ると出る
//     上部ツールバー（約40pt）を除く。これらの高さはウィンドウの大きさによらず
//     ポイント単位で一定なので、比率（roiTop）だけでは縦に十分高いウィンドウでしか除けない
//     （実画像の 2x ウィンドウ 1846px では、6%=110行に対しツールバーが 56〜135 行にあり、
//     ツールバーが出ただけで変化画素率が ChangedFrac の約30倍になった）。
//     そこで topChromePt をバッキングスケール倍した画素数と roiTop の大きい方で切る。
//     ただし小さなウィンドウで本文を削りすぎないよう、maxTopFrac を上限にする
//     （高さがおよそ topChromePt/maxTopFrac ポイント未満のウィンドウでは、ツールバーの
//     一部が ROI に残る）
//   - 下端 8%  : Page Flip のスライダーや、ページ番号・読書位置のフッターを除く
//   - 左右 3%  : ウィンドウ端の余白と角の丸み（透明画素）を除く
//
// ROI 外だけが変わるケースは「変化なし」と判定される点に注意。典型例は
// 白紙ページが続く区間（見開きの白紙→白紙、章扉の裏など）で、ページ番号の
// フッターだけが変わる場合。このとき Diff は0になり、呼び出し側からは
// 「ページ送りが効いていない」と区別できない。白紙ページを1枚取り逃しても
// 本文は失われないため、フッターやスライダー（読書位置の表示など、ページ内容と
// 無関係に変わりうる要素）を比較に混ぜる誤検出のほうを重く見て、この割り切りにしている。
// 呼び出し側は「変化なし」を直ちに最終ページと結論づけず、複数回の確認や
// ユーザーへの確認と組み合わせること。
const (
	roiLeft   = 0.03
	roiRight  = 0.97
	roiTop    = 0.06
	roiBottom = 0.92

	// topChromePt は上端から除く高さ（ポイント）。タイトルバー 28pt + ツールバー 40pt
	// （実画像の 2x で 56〜135 行）に、影や丸めの余裕 4pt を足した値。
	// フルスクリーン時のツールバー（2x で約80行）もこの範囲に収まる。
	topChromePt = 72

	// maxTopFrac は上端から除く高さの上限（画像の高さに対する割合）。
	maxTopFrac = 0.25

	// unknownScale はウィンドウのポイント幅が不明なときに仮定するバッキングスケール。
	// 1x を仮定して 2x の画像を処理するとツールバーが ROI に残るので、多めに切る側（2x）に倒す。
	unknownScale = 2
)

// Frame はスクショ1枚分の比較用データを包むゼロ値安全な型。
// var f Frame のままなら「未計算」を表す。
type Frame struct {
	w, h  int      // 縮小後のサイズ
	luma  []uint8  // 縮小後の輝度（行優先、長さ w*h）
	roiW  int      // 縮小前の ROI 幅（exact の比較でサイズ違いを弾くため）
	roiH  int      // 縮小前の ROI 高さ
	exact [32]byte // 縮小前 ROI 画素の sha256（完全一致判定用）
}

// IsZero は Frame が未計算かどうかを返す。
func (a Frame) IsZero() bool { return a.luma == nil }

// Load は PNG ファイルを読み込んで Frame を作る。pointWidth は FromImage を参照。
func Load(path string, pointWidth float64) (Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return Frame{}, err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return Frame{}, fmt.Errorf("PNGデコード失敗 %s: %w", path, err)
	}
	return FromImage(img, pointWidth), nil
}

// FromImage は画像から ROI を切り出し、輝度に変換して幅 targetWidth へ面積平均で縮小する。
// 同時に、縮小前の ROI 画素から完全一致判定用の sha256 を計算する。
//
// pointWidth は撮影したウィンドウの幅（ポイント単位。capture.Kindle.WindowWidth）。
// 「画像の幅 ÷ pointWidth」をバッキングスケールとして、上端から除く画素数
// （topChromePt）の換算に使う。0 以下なら不明として unknownScale を仮定する。
// 比較する2枚には同じ pointWidth を渡すこと（ROI がずれると Same/Diff が意味を失う）。
//
// 透明画素（alpha<255。ウィンドウの角の丸みに現れる）は「黒の上に合成した色」として
// 扱う。alpha=0 は不透明の黒になるため、透明部分に残っている RGB 値の違い（PNG では
// 見えない情報）が差分やハッシュに混ざらない。角は ROI の外なので通常は影響しないが、
// 小さなウィンドウや将来の ROI 変更でも結果が安定するよう明示的に決めている。
func FromImage(img image.Image, pointWidth float64) Frame {
	b := img.Bounds()
	x0 := b.Min.X + int(float64(b.Dx())*roiLeft)
	x1 := b.Min.X + int(float64(b.Dx())*roiRight)
	y0 := b.Min.Y + topCut(b, pointWidth)
	y1 := b.Min.Y + int(float64(b.Dy())*roiBottom)
	sw, sh := x1-x0, y1-y0
	if sw <= 0 || sh <= 0 {
		// 極端に小さい画像（1×1 等）は ROI が空になる。比較不能を避けるため全体を使う
		x0, x1, y0, y1 = b.Min.X, b.Max.X, b.Min.Y, b.Max.Y
		sw, sh = b.Dx(), b.Dy()
	}

	// 縮小後のサイズ（縦横比を保つ。拡大はしない）
	dw := min(sw, targetWidth)
	dh := max(1, (sh*dw+sw/2)/sw)

	// 元画素 → 縮小後画素の対応表。縮小後の1画素は元画像の
	// [o*sw/dw, (o+1)*sw/dw) の矩形に対応する（整数境界の面積平均。重なり・隙間なし）
	xmap := boxMap(sw, dw)
	ymap := boxMap(sh, dh)
	sums := make([]uint32, dw*dh)
	counts := make([]uint32, dw*dh)

	hasher := sha256.New()
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(sw))
	binary.BigEndian.PutUint32(hdr[4:8], uint32(sh))
	hasher.Write(hdr[:])

	rgb := make([]byte, sw*3) // 1行分の RGB（黒合成済み）。ハッシュ入力と輝度計算に共用
	for y := y0; y < y1; y++ {
		readRow(img, y, x0, x1, rgb)
		hasher.Write(rgb)

		row := ymap[y-y0] * dw
		for i := 0; i < sw; i++ {
			r, g, bl := uint32(rgb[i*3]), uint32(rgb[i*3+1]), uint32(rgb[i*3+2])
			// ITU-R BT.601 の輝度（PIL の "L" 変換と同じ係数）を 16bit 固定小数で計算
			yv := (19595*r + 38470*g + 7471*bl + 1<<15) >> 16
			idx := row + xmap[i]
			sums[idx] += yv
			counts[idx]++
		}
	}

	luma := make([]uint8, dw*dh)
	for i := range luma {
		luma[i] = uint8((sums[i] + counts[i]/2) / counts[i])
	}

	fr := Frame{w: dw, h: dh, luma: luma, roiW: sw, roiH: sh}
	copy(fr.exact[:], hasher.Sum(nil))
	return fr
}

// topCut は ROI の上端として画像の上から除く行数を返す。
// roiTop の比率と、topChromePt をバッキングスケール倍した画素数（ただし maxTopFrac が上限）の大きい方。
func topCut(b image.Rectangle, pointWidth float64) int {
	scale := float64(unknownScale)
	if pointWidth > 0 {
		// ディスプレイに 1x 未満は無い。ウィンドウが発見後に縮められて幅の記録が古い場合などに
		// 1 未満の値が出ても、ツールバーを除けなくなる側には倒さない
		scale = max(1, float64(b.Dx())/pointWidth)
	}
	h := float64(b.Dy())
	return int(max(h*roiTop, min(topChromePt*scale, h*maxTopFrac)))
}

// boxMap は長さ src の元画素それぞれが、長さ dst に縮小したときどの画素に入るかを返す。
func boxMap(src, dst int) []int {
	m := make([]int, src)
	for o := 0; o < dst; o++ {
		for i := o * src / dst; i < (o+1)*src/dst; i++ {
			m[i] = o
		}
	}
	return m
}

// readRow は img の y 行目 [x0, x1) を、黒の上に合成した 8bit RGB として dst に書き込む。
// png.Decode が返す代表的な型（RGBA 付き PNG は *image.NRGBA、アルファ無しは
// *image.RGBA）は Pix を直接読んで高速化し、それ以外は汎用の At() で処理する。
func readRow(img image.Image, y, x0, x1 int, dst []byte) {
	switch m := img.(type) {
	case *image.NRGBA:
		// 非乗算アルファ: 黒合成 = 色 × alpha / 255
		p := m.Pix[m.PixOffset(x0, y):m.PixOffset(x1, y)]
		for i, j := 0, 0; i < len(p); i, j = i+4, j+3 {
			a := uint32(p[i+3])
			if a == 0xff {
				dst[j], dst[j+1], dst[j+2] = p[i], p[i+1], p[i+2]
				continue
			}
			dst[j] = uint8((uint32(p[i])*a + 127) / 255)
			dst[j+1] = uint8((uint32(p[i+1])*a + 127) / 255)
			dst[j+2] = uint8((uint32(p[i+2])*a + 127) / 255)
		}
	case *image.RGBA:
		// 乗算済みアルファ: RGB がそのまま黒合成済みの値
		p := m.Pix[m.PixOffset(x0, y):m.PixOffset(x1, y)]
		for i, j := 0, 0; i < len(p); i, j = i+4, j+3 {
			dst[j], dst[j+1], dst[j+2] = p[i], p[i+1], p[i+2]
		}
	default:
		// color.Color.RGBA() は乗算済みの16bit値を返すので、上位8bitが黒合成済みの値になる
		for x, j := x0, 0; x < x1; x, j = x+1, j+3 {
			r, g, b, _ := img.At(x, y).RGBA()
			dst[j], dst[j+1], dst[j+2] = uint8(r>>8), uint8(g>>8), uint8(b>>8)
		}
	}
}

// Diff は2枚の間で輝度差が pixelTol を超えた画素の割合（0〜1）を返す。
//
// 縮小後のサイズが違う場合（撮影の合間にウィンドウがリサイズされた等）はエラーに
// せず frac=1（全面が変わった）として返す。リサイズ後の画面は前ページと比べられない
// 「新しい画面」であり、呼び出し側の判定に自然に乗るため:
//   - 前ページとの比較では「変わった」側に倒れる（取り逃しより重複保存を選ぶ）
//   - 安定判定では「まだ動いている」側に倒れ、同じサイズで2回撮れるまで待つ
//
// エラーになるのは未計算（ゼロ値）の Frame を渡した場合だけ。
func (a Frame) Diff(b Frame) (frac float64, err error) {
	if a.IsZero() || b.IsZero() {
		return 0, errors.New("未計算のフレーム同士は比較できません")
	}
	if a.w != b.w || a.h != b.h {
		return 1, nil
	}
	changed := 0
	for i, av := range a.luma {
		d := int(av) - int(b.luma[i])
		if d > pixelTol || d < -pixelTol {
			changed++
		}
	}
	return float64(changed) / float64(len(a.luma)), nil
}

// Same は2枚の ROI 画素が完全に一致するかを返す（縮小前の sha256 比較）。
// screencapture -l は静止ウィンドウに対し毎回同一の画素を返すため、
// 「画面がまったく動いていない」ことの最も強い証拠になる。
// どちらかが未計算なら false。
func (a Frame) Same(b Frame) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	return a.roiW == b.roiW && a.roiH == b.roiH && a.exact == b.exact
}
