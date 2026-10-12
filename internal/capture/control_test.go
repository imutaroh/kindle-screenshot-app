package capture

import (
	"slices"
	"strings"
	"testing"
)

// このファイルのテストはスクリプトや引数を「作る」だけで、実行はしない
// （実際にマウスを動かしたり Kindle を撮ったりしない）。

func TestScreencaptureArgs(t *testing.T) {
	got := screencaptureArgs(1234, "/tmp/x.png")
	want := []string{"-l", "1234", "-o", "-x", "/tmp/x.png"}
	if !slices.Equal(got, want) {
		t.Errorf("screencaptureArgs = %v, want %v", got, want)
	}
}

func TestClickScript(t *testing.T) {
	t.Run("シングルクリック", func(t *testing.T) {
		s := clickScript(100.5, -20, false)
		for _, want := range []string{
			"const p = {x: 100.5, y: -20};",
			"const clicks = 1;",
			"$.CGEventPost(0, ev);",      // kCGHIDEventTap
			"$.CGEventSetFlags(ev, 0);",  // 物理的に押されている修飾キーを乗せない
			"post(5, 0);",                // まず mouseMoved でカーソルを移す
			"post(1, i);", "post(2, i);", // down / up にクリック回数を付ける
			"$.CGEventSetIntegerValueField(ev, 1, clickState);",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("clickScript に %q がありません", want)
			}
		}
		if strings.Contains(s, "CGEventPostToPid") {
			t.Error("クリックはプロセス直送ではなく HID イベントタップに流すべき")
		}
	})

	t.Run("ダブルクリック", func(t *testing.T) {
		if s := clickScript(1, 2, true); !strings.Contains(s, "const clicks = 2;") {
			t.Error("ダブルクリックで clicks = 2 になっていない")
		}
	})

	t.Run("大きな座標も指数表記にしない", func(t *testing.T) {
		if s := clickScript(12345678, 0.25, false); !strings.Contains(s, "{x: 12345678, y: 0.25}") {
			t.Errorf("座標の書式が想定外: %s", s)
		}
	})
}

func TestClick_RejectsInvalidCoords(t *testing.T) {
	// NaN はスクリプトを作る前に弾く（osascript は起動しない）
	nan := func() float64 { var z float64; return z / z }()
	if err := Click(nan, 0, false); err == nil {
		t.Error("NaN の座標はエラーにすべき")
	}
}

func TestInfo_ZeroBeforeDiscover(t *testing.T) {
	var k Kindle
	if k.Info().WindowID != 0 {
		t.Error("未発見なのに WindowID が入っている")
	}
}

func TestParkPoint(t *testing.T) {
	main := Rect{X: 0, Y: 0, W: 3440, H: 1440}
	for _, tt := range []struct {
		name    string
		win     Rect
		screens []Rect
		wantX   float64
		wantY   float64
		wantOK  bool
	}{
		{"右に余白があれば右", Rect{X: 216, Y: 128, W: 1470, H: 1064}, []Rect{main}, 1726, 660, true},
		{"右端に寄せたウィンドウは左", Rect{X: 1970, Y: 100, W: 1470, H: 1000}, []Rect{main}, 1930, 600, true},
		{"横いっぱいなら下", Rect{X: 0, Y: 0, W: 3440, H: 1000}, []Rect{main}, 1720, 1040, true},
		{"画面いっぱいなら右隣の画面へ", Rect{X: 0, Y: 0, W: 3440, H: 1440}, []Rect{main, {X: 3440, Y: 0, W: 1512, H: 982}}, 3480, 720, true},
		{"上下左右が外なら別の画面の隅へ", Rect{X: 0, Y: 0, W: 3440, H: 1440}, []Rect{main, {X: 0, Y: 1440, W: 1000, H: 800}}, 1000 - 5, 1440 + 800 - 5, true},
		{"画面いっぱいで逃げ場が無い", Rect{X: 0, Y: 0, W: 3440, H: 1440}, []Rect{main}, 0, 0, false},
		{"画面が無い", Rect{X: 0, Y: 0, W: 100, H: 100}, nil, 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			x, y, ok := parkPoint(tt.win, tt.screens)
			if ok != tt.wantOK || x != tt.wantX || y != tt.wantY {
				t.Errorf("parkPoint = (%v, %v, %v), want (%v, %v, %v)", x, y, ok, tt.wantX, tt.wantY, tt.wantOK)
			}
			if ok && tt.win.contains(x, y) {
				t.Errorf("選んだ点 (%v, %v) がウィンドウの内側です", x, y)
			}
		})
	}
}

func TestMoveScript(t *testing.T) {
	s := moveScript(1726, 660.5)
	for _, want := range []string{"{x: 1726, y: 660.5}", "CGEventSetFlags(ev, 0)", "CGEventPost(0, ev)"} {
		if !strings.Contains(s, want) {
			t.Errorf("moveScript に %q がありません", want)
		}
	}
}
