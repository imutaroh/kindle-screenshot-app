package markdown

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// benchDir は実画像と正解テキストの置き場所（リポジトリ直下の .claude/tmp/bench/assets）。
// 著作物を含むためリポジトリには入れていない。無い環境ではテストを飛ばす。
func benchDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.Abs(filepath.Join("..", "..", ".claude", "tmp", "bench", "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(d, "truth_c.txt")); err != nil {
		t.Skipf("ベンチ素材がありません: %s", d)
	}
	return d
}

// normalize は score.py の norm の移植。全角英数記号を半角に寄せ（NFKC の近似。
// 標準ライブラリに NFKC が無いため、全角 ASCII と全角空白だけを変換する）、
// Markdown の記号・空白・三点リーダを除く。
func normalize(s string) []rune {
	var out []rune
	for _, r := range s {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		if unicode.IsSpace(r) || strings.ContainsRune("#*`>-|…", r) {
			continue
		}
		out = append(out, r)
	}
	return []rune(strings.ReplaceAll(string(out), "...", ""))
}

// cer は文字誤り率（編集距離 / 正解の文字数）。score.py は difflib の近似値だが、
// ここでは正確なレーベンシュタイン距離を使う（近似より大きくなることはあっても小さくはならない）。
func cer(truth, hyp string) float64 {
	a, b := normalize(truth), normalize(hyp)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return float64(prev[len(b)]) / float64(len(a))
}

var markerRe = regexp.MustCompile(`(?m)^(<!-- p\.\d+ -->|# .*)$`)

// bodyOnly は本1冊分の Markdown から書名とページ区切りの行を除く
// （正解テキストは1ページの本文だけなので、それらを含めると CER が水増しされる）。
func bodyOnly(md string) string { return markerRe.ReplaceAllString(md, "") }

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestConvertDirCER は本物の OCR ヘルパーで実画像（ライト・ダーク）を変換し、
// 正解テキストとの文字誤り率が 1% 以下であることを確かめる。
// macOS 26 未満・swiftc なし・素材なしの環境では飛ばす。
func TestConvertDirCER(t *testing.T) {
	assets := benchDir(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ocr.CheckContext(ctx); err != nil {
		t.Skipf("OCR が使えない環境: %v", err)
	}
	truth, err := os.ReadFile(filepath.Join(assets, "truth_c.txt"))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"c_inv", "c_dark"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			copyFile(t, filepath.Join(assets, name+".png"), filepath.Join(dir, "page_0001.png"))

			var calls int
			res, err := ConvertDir(ctx, dir, Options{
				BookTitle: "サンプル書籍",
				Progress:  func(done, total int) { calls++ },
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.MarkdownPath != filepath.Join(dir, "サンプル書籍.md") || res.Pages != 1 || calls != 1 {
				t.Errorf("result = %+v, progress calls = %d", res, calls)
			}
			if len(res.ReviewPages) != 0 {
				t.Errorf("正しく読めるページが要確認になった: %v", res.ReviewPages)
			}
			md, err := os.ReadFile(res.MarkdownPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(md), "# サンプル書籍\n\n<!-- p.1 -->\n\n## 【序章】") {
				t.Errorf("先頭の形が違う:\n%s", md)
			}
			got := cer(string(truth), bodyOnly(string(md)))
			t.Logf("%s: CER = %.2f%%", name, got*100)
			if got > 0.01 {
				t.Errorf("CER %.2f%% > 1%%\n%s", got*100, md)
			}
		})
	}
}

func TestCER(t *testing.T) {
	if got := cer("あいうえお", "あいうえお"); got != 0 {
		t.Errorf("同一で %v", got)
	}
	if got := cer("あいうえお", "あいXえお"); got != 0.2 {
		t.Errorf("1文字置換で %v", got)
	}
	// Markdown 記号・空白・全角/半角の違いは数えない
	if got := cer("【序　章】Ａ＝１", "# 【序章】 A=1"); got != 0 {
		t.Errorf("正規化で %v", got)
	}
}
