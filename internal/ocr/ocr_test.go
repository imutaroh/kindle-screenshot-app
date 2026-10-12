package ocr

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"26.6.2", 26, true},
		{"26\n", 26, true},
		{"15.7", 15, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, err := majorVersion(c.in)
		if (err == nil) != c.ok || got != c.want {
			t.Errorf("majorVersion(%q) = %d, %v; want %d ok=%v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestDecodePages(t *testing.T) {
	in := `{"path":"a.png","width":10,"height":20,"paragraphs":[{"text":"縦","bbox":[0.1,0.2,0.3,0.4],"direction":"ttb","isTitle":true,"confidence":0.4,"lines":[]}],"lists":[],"tables":[]}

{"path":"b.png","error":"画像を読み込めません"}
`
	var got []Page
	n, err := decodePages(strings.NewReader(in), []string{"a.png", "b.png"}, func(p Page) { got = append(got, p) })
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	p := got[0].Paragraphs[0]
	if p.Text != "縦" || !p.IsTitle || math.Abs(p.BBox.Right()-0.4) > 1e-9 || math.Abs(p.BBox.Bottom()-0.6) > 1e-9 {
		t.Errorf("paragraph = %+v", p)
	}
	if got[1].Error == "" {
		t.Errorf("error が読めていない: %+v", got[1])
	}
}

func TestDecodePagesOrderMismatch(t *testing.T) {
	in := `{"path":"b.png"}` + "\n"
	_, err := decodePages(strings.NewReader(in), []string{"a.png"}, func(Page) {})
	if err == nil {
		t.Fatal("出力順のずれを検出していない")
	}
}

// fakeHelper は本物のヘルパーの代わりに、引数ごとに {"path":...} を1行書く sh スクリプトを作る。
// body は各引数 $p について、行を書く前に実行される（exit で「そのページで落ちた」を再現する）。
func fakeHelper(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeocr")
	script := "#!/bin/sh\nfor p in \"$@\"; do\n" + body + "\nprintf '{\"path\":\"%s\"}\\n' \"$p\"\ndone\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// ヘルパーが途中のページで落ちても、そのページだけを失敗にして残りを読み直す。
func TestRunChunkRestartsAfterCrash(t *testing.T) {
	// bad.png で落ちる。2回目の起動（bad.png の次から）は最後まで読める
	bin := fakeHelper(t, `[ "$p" = bad.png ] && { echo "Vision crashed" >&2; exit 1; }`)
	var got []Page
	err := runChunk(context.Background(), bin, []string{"a.png", "bad.png", "c.png", "d.png"}, func(p Page) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.png", "bad.png", "c.png", "d.png"}
	if len(got) != len(want) {
		t.Fatalf("got %d pages: %+v", len(got), got)
	}
	for i, p := range got {
		if p.Path != want[i] {
			t.Errorf("got[%d].Path = %q, want %q", i, p.Path, want[i])
		}
		if (p.Error != "") != (p.Path == "bad.png") {
			t.Errorf("got[%d].Error = %q", i, p.Error)
		}
	}
	if !strings.Contains(got[1].Error, "Vision crashed") {
		t.Errorf("落ちたページのエラーに stderr が無い: %q", got[1].Error)
	}
}

// ヘルパーが1ページ分の行を書き損ねても（順番がずれても）、そのページだけを失敗にする。
func TestRunChunkRestartsAfterMissingLine(t *testing.T) {
	bin := fakeHelper(t, `[ "$p" = skip.png ] && continue`)
	var got []Page
	err := runChunk(context.Background(), bin, []string{"a.png", "skip.png", "c.png"}, func(p Page) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].Path != "skip.png" || got[1].Error == "" || got[2].Path != "c.png" || got[2].Error != "" {
		t.Errorf("got %+v", got)
	}
}

// 毎回1ページ目で落ちるヘルパーは壊れているとみなし、全ページ分起動し直さずに打ち切る。
func TestRunChunkGivesUpOnBrokenHelper(t *testing.T) {
	bin := fakeHelper(t, "exit 1")
	paths := []string{"1.png", "2.png", "3.png", "4.png", "5.png", "6.png"}
	calls := 0
	err := runChunk(context.Background(), bin, paths, func(Page) { calls++ })
	if err == nil {
		t.Fatal("壊れたヘルパーでエラーにならない")
	}
	if calls != maxHelperFailures-1 {
		t.Errorf("打ち切るまでに %d ページを渡した（want %d）", calls, maxHelperFailures-1)
	}
}

// ヘルパーを起動できないときは、ページを飛ばして続けずにすぐエラーにする。
func TestRunChunkMissingHelper(t *testing.T) {
	calls := 0
	err := runChunk(context.Background(), filepath.Join(t.TempDir(), "missing"), []string{"a.png", "b.png"}, func(Page) { calls++ })
	if err == nil || calls != 0 {
		t.Errorf("err=%v calls=%d", err, calls)
	}
}

func TestSetupErrorMessage(t *testing.T) {
	e := &SetupError{Msg: "swiftc が使えません", Fix: "xcode-select --install"}
	if !strings.Contains(e.Error(), "xcode-select --install") {
		t.Errorf("Error() に対処が含まれない: %s", e.Error())
	}
}

func TestHelperNameStable(t *testing.T) {
	a, b := helperName(), helperName()
	if a != b || !strings.HasPrefix(a, "visionocr-") || len(a) != len("visionocr-")+12 {
		t.Errorf("helperName = %q", a)
	}
}

// benchAsset はリポジトリ直下の .claude/tmp/bench/assets にある実画像のパスを返す。
// 著作物を含むためリポジトリには入れていない。無い環境ではテストを飛ばす。
func benchAsset(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", ".claude", "tmp", "bench", "assets", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("ベンチ画像がありません: %s", p)
	}
	return p
}

// TestRecognizeReal は実際にヘルパーをビルド・起動し、縦書きページを読めることを確かめる。
// macOS 26 未満や swiftc が無い環境では飛ばす。
func TestRecognizeReal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS 専用")
	}
	img := benchAsset(t, "c_inv.png")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := CheckContext(ctx); err != nil {
		t.Skipf("OCR が使えない環境: %v", err)
	}

	pages, err := Recognize(ctx, []string{img, filepath.Join(t.TempDir(), "missing.png")})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d", len(pages))
	}
	p := pages[0]
	if p.Error != "" || p.Width != 1470 || p.Height != 895 {
		t.Fatalf("page = %+v", p)
	}
	var title *Paragraph
	ttb := 0
	for i := range p.Paragraphs {
		if p.Paragraphs[i].IsTitle {
			title = &p.Paragraphs[i]
		}
		if p.Paragraphs[i].Direction == DirTTB {
			ttb++
		}
	}
	// ベンチ画像は序章の最初のページ（著作物なので本文の文字列はソースに書かない）
	if title == nil || !strings.HasPrefix(title.Text, "【序章】") {
		t.Errorf("見出しが取れていない: %+v", title)
	}
	if ttb < 8 {
		t.Errorf("縦書き段落が少ない: %d", ttb)
	}
	// 座標は左上原点: 柱（横書きの書名）は画像の上端付近にある
	headY := 1.0
	for _, para := range p.Paragraphs {
		if para.Direction == DirLTR {
			headY = min(headY, para.BBox.Y())
		}
	}
	if headY > 0.1 {
		t.Errorf("左上原点になっていない: 横書き段落の最上端 y = %v", headY)
	}
	if pages[1].Error == "" {
		t.Errorf("存在しない画像が error にならない: %+v", pages[1])
	}
}
