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
