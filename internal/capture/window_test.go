package capture

import (
	"os"
	"strings"
	"testing"
)

func TestParseWindowList(t *testing.T) {
	t.Run("正常なJSON", func(t *testing.T) {
		wins, err := parseWindowList(`[{"owner":"Kindle","pid":100,"windowID":1,"layer":0,"x":0,"y":0,"width":1470,"height":923}]`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(wins) != 1 || wins[0].Owner != "Kindle" {
			t.Fatalf("unexpected result: %+v", wins)
		}
	})

	t.Run("onscreenを読む", func(t *testing.T) {
		wins, err := parseWindowList(`[
			{"owner":"Kindle","pid":100,"windowID":1,"layer":0,"onscreen":true,"x":10,"y":-20,"width":1470,"height":923},
			{"owner":"Kindle","pid":100,"windowID":2,"layer":0,"onscreen":false,"x":0,"y":0,"width":1470,"height":33}
		]`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !wins[0].OnScreen || wins[1].OnScreen {
			t.Errorf("OnScreen = %v, %v, want true, false", wins[0].OnScreen, wins[1].OnScreen)
		}
		if wins[0].X != 10 || wins[0].Y != -20 {
			t.Errorf("位置 = (%g, %g), want (10, -20)", wins[0].X, wins[0].Y)
		}
	})

	t.Run("onscreenが無ければfalse", func(t *testing.T) {
		// 旧スクリプトの出力（onscreen フィールドなし）も読める
		wins, err := parseWindowList(`[{"owner":"Kindle","pid":100,"windowID":1,"layer":0,"x":0,"y":0,"width":1470,"height":923}]`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if wins[0].OnScreen {
			t.Error("onscreen が無いのに OnScreen=true になった")
		}
	})

	t.Run("スクリプトがonscreenを出力する", func(t *testing.T) {
		if !strings.Contains(windowListScript, "onscreen: !!w.kCGWindowIsOnscreen") {
			t.Error("windowListScript が onscreen を出力していない")
		}
	})

	t.Run("不正なJSON", func(t *testing.T) {
		if _, err := parseWindowList("not json"); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestSelectKindleWindow(t *testing.T) {
	t.Run("layer!=0は除外される", func(t *testing.T) {
		wins := []WindowInfo{
			{Owner: "Kindle", PID: 100, WindowID: 1, Layer: 1, Width: 1470, Height: 923},
		}
		_, ok := selectKindleWindow(wins)
		if ok {
			t.Fatal("layer!=0 のウィンドウが選ばれてしまった")
		}
	})

	t.Run("複数候補から最大面積が選ばれる", func(t *testing.T) {
		// 実機観測: Kindle は layer 0 に空タイトルのバー状ウィンドウ
		// （3440x30 や 1470x33）を複数持つ。本体（1470x923）を選ぶべき。
		wins := []WindowInfo{
			{Owner: "Kindle", PID: 100, WindowID: 1, Layer: 0, Width: 3440, Height: 30},
			{Owner: "Kindle", PID: 100, WindowID: 2, Layer: 0, Width: 1470, Height: 923},
			{Owner: "Kindle", PID: 100, WindowID: 3, Layer: 0, Width: 1470, Height: 33},
		}
		got, ok := selectKindleWindow(wins)
		if !ok {
			t.Fatal("候補が見つからなかった")
		}
		if got.WindowID != 2 {
			t.Fatalf("expected windowID=2 (本体), got %+v", got)
		}
	})

	t.Run("面積同点ならWindowID最小", func(t *testing.T) {
		wins := []WindowInfo{
			{Owner: "Kindle", PID: 100, WindowID: 5, Layer: 0, Width: 1470, Height: 923},
			{Owner: "Kindle", PID: 100, WindowID: 2, Layer: 0, Width: 1470, Height: 923},
		}
		got, ok := selectKindleWindow(wins)
		if !ok {
			t.Fatal("候補が見つからなかった")
		}
		if got.WindowID != 2 {
			t.Fatalf("expected windowID=2 (最小), got %+v", got)
		}
	})

	t.Run("画面外のウィンドウも選ぶ", func(t *testing.T) {
		// 別 Space にある通常ウィンドウも screencapture -l で撮れるので、OnScreen では絞らない
		wins := []WindowInfo{
			{Owner: "Kindle", PID: 100, WindowID: 1, Layer: 0, OnScreen: true, Width: 1470, Height: 33},
			{Owner: "Kindle", PID: 100, WindowID: 2, Layer: 0, OnScreen: false, Width: 1470, Height: 923},
		}
		got, ok := selectKindleWindow(wins)
		if !ok || got.WindowID != 2 {
			t.Fatalf("expected windowID=2 (画面外の本体), got %+v ok=%v", got, ok)
		}
	})

	t.Run("候補ゼロ_owner不一致のみ", func(t *testing.T) {
		wins := []WindowInfo{
			{Owner: "Finder", PID: 1, WindowID: 1, Layer: 0, Width: 1000, Height: 1000},
			{Owner: "Safari", PID: 2, WindowID: 2, Layer: 0, Width: 1000, Height: 1000},
		}
		_, ok := selectKindleWindow(wins)
		if ok {
			t.Fatal("owner不一致なのに選ばれてしまった")
		}
	})

	t.Run("候補ゼロ_KindleHelperは拾わない", func(t *testing.T) {
		// 部分一致にすると "Kindle Helper" のようなヘルパープロセスを
		// 誤って拾ってしまうため、完全一致であることを確認する。
		wins := []WindowInfo{
			{Owner: "Kindle Helper", PID: 1, WindowID: 1, Layer: 0, Width: 1470, Height: 923},
		}
		_, ok := selectKindleWindow(wins)
		if ok {
			t.Fatal("Kindle Helper が選ばれてしまった")
		}
	})
}

// TestDiscoverSmoke は実際に CGWindowListCopyWindowInfo を叩くスモークテスト。
// Kindle for Mac が起動している必要があり環境に依存するため、通常の
// go test では実行せず、KINDLESNAP_SMOKE=1 を付けたときだけ動かす。
//
//	KINDLESNAP_SMOKE=1 go test ./internal/capture/ -run Discover -v
func TestDiscoverSmoke(t *testing.T) {
	if os.Getenv("KINDLESNAP_SMOKE") != "1" {
		t.Skip("KINDLESNAP_SMOKE=1 のときだけ実行（実際にウィンドウ一覧を取得するため）")
	}

	var k Kindle
	if err := k.Discover(); err != nil {
		t.Skipf("Discover失敗（Kindle未起動などの環境要因の可能性）: %v", err)
	}
	t.Log(k.Describe())
}
