package capture

import (
	"errors"
	"strings"
	"testing"
)

// jxaResult は差し替えた jxaRunner が返す1回分の結果。
type jxaResult struct {
	out string
	err error
}

// stubJXA は jxaRunner を差し替え、呼ばれるたびに results を順に返す（最後の値は繰り返す）。
// osascript は起動しないので、実機の Kindle にキーは送られない。
func stubJXA(t *testing.T, results ...jxaResult) *int {
	t.Helper()
	calls := 0
	orig := jxaRunner
	jxaRunner = func(script string) (string, error) {
		if !strings.Contains(script, "CGEventPostToPid") {
			t.Errorf("想定外のスクリプトが実行されました: %s", script)
		}
		r := results[min(calls, len(results)-1)]
		calls++
		return r.out, r.err
	}
	t.Cleanup(func() { jxaRunner = orig })
	return &calls
}

// discovered は Discover 済み（ensure が osascript を呼ばない）の Kindle を返す。
func discovered() *Kindle {
	return &Kindle{appName: "Kindle", pid: 1, windowID: 1, width: 100, height: 100}
}

func TestTurnPage_Retry(t *testing.T) {
	t.Run("成功は1回だけ送る", func(t *testing.T) {
		calls := stubJXA(t, jxaResult{out: "ok"})
		if err := discovered().TurnPage(Left); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 {
			t.Errorf("呼び出し回数 = %d, want 1", *calls)
		}
	})

	t.Run("起動失敗はリトライして成功する", func(t *testing.T) {
		calls := stubJXA(t,
			jxaResult{err: errors.New("exit status 1")},
			jxaResult{out: "ok"})
		if err := discovered().TurnPage(Left); err != nil {
			t.Fatal(err)
		}
		if *calls != 2 {
			t.Errorf("呼び出し回数 = %d, want 2", *calls)
		}
	})

	t.Run("キー送信前の失敗は上限までリトライしてエラー", func(t *testing.T) {
		calls := stubJXA(t, jxaResult{out: "error: Error: CGEventCreateKeyboardEvent が失敗しました"})
		if err := discovered().TurnPage(Left); err == nil {
			t.Fatal("エラーになるべき")
		}
		if *calls != turnPageRetries+1 {
			t.Errorf("呼び出し回数 = %d, want %d", *calls, turnPageRetries+1)
		}
	})

	// 以下はキーが届いた可能性があるので、再送するとページを飛ばす。1回で止めること
	for name, out := range map[string]string{
		"送信後の失敗": "posted: Error: boom",
		"想定外の出力": "ok\nwarning: something",
		"空の出力":   "",
	} {
		t.Run(name+"は再送しない", func(t *testing.T) {
			calls := stubJXA(t, jxaResult{out: out}, jxaResult{out: "ok"})
			if err := discovered().TurnPage(Left); err == nil {
				t.Fatal("エラーになるべき")
			}
			if *calls != 1 {
				t.Errorf("呼び出し回数 = %d, want 1（再送するとページを飛ばす）", *calls)
			}
		})
	}
}

// TestKeyEventScriptPinsModifierFlags は、送るキーイベントの修飾キーフラグを必ず上書きしていることを確かめる。
// 上書きしないと、ユーザーがメインで押している ⌘ などが乗り、Kindle に「⌘+←」（章ジャンプ）が届く。
func TestKeyEventScriptPinsModifierFlags(t *testing.T) {
	for _, want := range []string{
		"const arrowKeyFlags = 0x00A00000;",
		"$.CGEventSetFlags(down, arrowKeyFlags);",
		"$.CGEventSetFlags(up, arrowKeyFlags);",
	} {
		if !strings.Contains(keyEventScript, want) {
			t.Errorf("keyEventScript に %q がありません", want)
		}
	}
}
