package main

import (
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
)

// windowRect はウィンドウの位置と大きさ（ポイント単位のグローバル座標）。
type windowRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

func rectOf(w capture.WindowInfo) windowRect {
	return windowRect{X: w.X, Y: w.Y, W: w.Width, H: w.Height}
}

// peekOutput は kindlesnap peek の出力。width / height は PNG の画素数で、
// Retina なら window の w / h（ポイント）の2倍になる。click の座標はポイントで指定するので、
// PNG 上の画素座標を click に渡すときは (width / window.w) で割ること。
type peekOutput struct {
	Path   string     `json:"path"`
	Width  int        `json:"width"`
	Height int        `json:"height"`
	Window windowRect `json:"window"`
}

// cmdPeek は kindlesnap peek。Kindle のウィンドウを PNG に撮り、パスと大きさを返す。
// Claude がこの画像を見て、ライブラリから本を開く位置などを決めるために使う。
func cmdPeek(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("peek", stderr)
	out := fs.String("out", filepath.Join(os.TempDir(), "kindlesnap-peek.png"), "保存先の PNG ファイル")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return usageError(fs, "--out を解決できません: %v", err)
	}

	win, err := capture.Peek(path)
	if err != nil {
		return fail(stdout, err)
	}
	w, h, err := pngSize(path)
	if err != nil {
		return fail(stdout, err)
	}
	writeJSON(stdout, peekOutput{Path: path, Width: w, Height: h, Window: rectOf(win)})
	return exitOK
}

// pngSize は PNG のヘッダだけを読んで画素数を返す（全体はデコードしない）。
func pngSize(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("PNGヘッダの読み込みに失敗しました: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// clickOutput は kindlesnap click の出力。
type clickOutput struct {
	OK     bool       `json:"ok"`
	X      float64    `json:"x"`      // 指定されたウィンドウ内の座標（ポイント）
	Y      float64    `json:"y"`      //
	Global point      `json:"global"` // 実際にクリックしたグローバル座標（ポイント）
	Double bool       `json:"double"`
	Window windowRect `json:"window"`
}

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// toGlobal はウィンドウ内の座標 (x, y)（ウィンドウの左上が原点、ポイント単位）を
// グローバル座標に変換する。ウィンドウの外を指していたらエラー（別のアプリをクリックしてしまうため）。
func toGlobal(win capture.WindowInfo, x, y float64) (point, error) {
	if x < 0 || y < 0 || x > win.Width || y > win.Height {
		return point{}, fmt.Errorf("座標 (%g, %g) が Kindle のウィンドウ（%gx%g ポイント）の外です。peek の画素座標ならポイントに換算してください", x, y, win.Width, win.Height)
	}
	return point{X: win.X + x, Y: win.Y + y}, nil
}

// cmdClick は kindlesnap click --x X --y Y [--double]。Kindle のウィンドウ内の座標
// （ポイント単位。peek の PNG の画素座標を Retina 倍率で割った値）をクリックする。
// 本物のマウスカーソルが動く。ライブラリから本を開くとき（ダブルクリック）などに使う。
func cmdClick(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("click", stderr)
	x := fs.Float64("x", 0, "ウィンドウ左上からの X 座標（ポイント。必須）")
	y := fs.Float64("y", 0, "ウィンドウ左上からの Y 座標（ポイント。必須）")
	double := fs.Bool("double", false, "ダブルクリックする")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	// 0 は正当な座標なので、既定値との比較ではなく「指定されたか」で必須を判定する
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["x"] || !set["y"] {
		return usageError(fs, "--x と --y を指定してください")
	}

	// クリックはマウスイベントを HID に流すので、権限が無いと黙って捨てられる。先に確かめる
	if err := capture.CheckAccessibility(); err != nil {
		return fail(stdout, err)
	}
	// 位置は毎回取り直す（ユーザーがウィンドウを動かしているかもしれない）
	k := &capture.Kindle{}
	if err := k.Discover(); err != nil {
		return fail(stdout, err)
	}
	win := k.Info()
	if !win.OnScreen {
		return fail(stdout, errors.New("Kindle のウィンドウが画面に表示されていない（最小化・別 Space）ため、クリックできません"))
	}
	g, err := toGlobal(win, *x, *y)
	if err != nil {
		return fail(stdout, err)
	}
	if err := capture.Click(g.X, g.Y, *double); err != nil {
		return fail(stdout, err)
	}
	writeJSON(stdout, clickOutput{OK: true, X: *x, Y: *y, Global: g, Double: *double, Window: rectOf(win)})
	return exitOK
}
