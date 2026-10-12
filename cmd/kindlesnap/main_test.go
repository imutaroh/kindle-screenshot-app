package main

// このパッケージのテストは Kindle にも権限にも触れない。Kindle を操作するサブコマンド
// （doctor / capture / peek / click）は、判定や組み立ての部分だけを偽物の入力で確かめる。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

// useTempLock はロックファイルの場所をテスト用の一時ディレクトリに差し替える。
func useTempLock(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kindlesnap.lock")
	orig := lockPath
	lockPath = func() string { return path }
	t.Cleanup(func() { lockPath = orig })
	return path
}

// stubConvert は convertMarkdown を差し替える。
func stubConvert(t *testing.T, f func(ctx context.Context, dir, title string, progress func(done, total int)) (string, int, []int, error)) {
	t.Helper()
	orig := convertMarkdown
	convertMarkdown = f
	t.Cleanup(func() { convertMarkdown = orig })
}

// jsonLines は出力を1行ずつ map にして返す（1行でも JSON でなければ失敗）。
func jsonLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("JSON でない行: %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func lastLine(t *testing.T, data []byte) map[string]any {
	t.Helper()
	lines := jsonLines(t, data)
	if len(lines) == 0 {
		t.Fatal("出力が空です")
	}
	return lines[len(lines)-1]
}

func writePNGs(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		// 中身は使わない（削除されるかだけを見る）
		if err := os.WriteFile(filepath.Join(dir, "page_000"+string(rune('0'+i))+".png"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func countPNGs(t *testing.T, dir string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "page_*.png"))
	return len(m)
}

func TestRun_Usage(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"引数なし", nil, exitUsage},
		{"不明なサブコマンド", []string{"nope"}, exitUsage},
		{"help", []string{"help"}, exitOK},
		{"capture に --book が無い", []string{"capture"}, exitUsage},
		{"capture の方向が不正", []string{"capture", "--book", "x", "--direction", "up"}, exitUsage},
		{"capture の pdf-pages が0", []string{"capture", "--book", "x", "--pdf-pages", "0"}, exitUsage},
		{"capture の max-pages が負", []string{"capture", "--book", "x", "--max-pages", "-1"}, exitUsage},
		{"capture の未知のフラグ", []string{"capture", "--book", "x", "--bogus"}, exitUsage},
		{"md に --dir が無い", []string{"md"}, exitUsage},
		{"click に座標が無い", []string{"click", "--x", "10"}, exitUsage},
		{"status に余分な引数", []string{"status", "extra"}, exitUsage},
		{"-h は成功", []string{"doctor", "-h"}, exitOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if got := run(tt.args, &out, &errOut); got != tt.want {
				t.Errorf("exit = %d, want %d (stderr=%s)", got, tt.want, errOut.String())
			}
		})
	}
}

func TestParseCaptureFlags(t *testing.T) {
	var errOut bytes.Buffer
	opts, _, ok := parseCaptureFlags([]string{"--book", " 本 ", "--direction", "right", "--max-pages", "3", "--no-md", "--keep-png", "--no-crop", "--pdf-pages", "20", "--out", "/x"}, &errOut)
	if !ok {
		t.Fatalf("解析に失敗: %s", errOut.String())
	}
	want := captureOptions{book: "本", out: "/x", maxPages: 3, dir: capture.Right, pdfPages: 20, noMD: true, keepPNG: true, noCrop: true}
	if opts != want {
		t.Errorf("opts = %+v, want %+v", opts, want)
	}

	opts, _, ok = parseCaptureFlags([]string{"--book", "本"}, &errOut)
	if !ok || opts.dir != capture.Left || opts.pdfPages != session.DefaultPDFPagesPerFile || opts.maxPages != 0 {
		t.Errorf("既定値が想定外: %+v", opts)
	}
}

func TestResolveOutRoot(t *testing.T) {
	home := func() (string, error) { return "/Users/me", nil }
	cwd, _ := os.Getwd()
	for _, tt := range []struct {
		name, flag, env, want string
	}{
		{"既定は ~/Documents/KindleBooks", "", "", "/Users/me/Documents/KindleBooks"},
		{"環境変数", "", "/data/books", "/data/books"},
		{"フラグが優先", "/flag", "/data/books", "/flag"},
		{"相対パスは絶対パスにする", "out", "", filepath.Join(cwd, "out")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveOutRoot(tt.flag, tt.env, home)
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := resolveOutRoot("", "", func() (string, error) { return "", errors.New("no home") }); err == nil {
		t.Error("ホームが分からないならエラーにすべき")
	}
}

func TestLock(t *testing.T) {
	path := useTempLock(t)

	l, err := acquireLock(path, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.setBookDir("/books/本"); err != nil {
		t.Fatal(err)
	}

	// 2つ目は断られ、保持者の情報がエラーに載る
	_, err = acquireLock(path, 99999)
	var le *errLocked
	if !errors.As(err, &le) || le.holder.PID != os.Getpid() || le.holder.BookDir != "/books/本" {
		t.Fatalf("err = %v, want 保持者つきの errLocked", err)
	}

	info, running, err := runningCapture()
	if err != nil || !running || info.PID != os.Getpid() || info.BookDir != "/books/本" {
		t.Errorf("runningCapture = %+v, %v, %v", info, running, err)
	}

	l.release()
	if _, running, _ := runningCapture(); running {
		t.Error("release 後も実行中と判定された")
	}
	// 解放後はまた取れる
	l2, err := acquireLock(path, os.Getpid())
	if err != nil {
		t.Fatalf("解放後に取れない: %v", err)
	}
	l2.release()
}

// TestRunningCapture_StaleContent は、クラッシュでロックファイルの中身だけが残った
// （flock は誰も持っていない）場合に、pid が生きていても実行中と判定しないことを確かめる。
// pid が再利用されて別のプロセスに SIGUSR1 を送る事故を防ぐため。
func TestRunningCapture_StaleContent(t *testing.T) {
	path := useTempLock(t)
	data, _ := json.Marshal(lockInfo{PID: os.Getpid(), BookDir: "/x"})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, running, err := runningCapture(); err != nil || running {
		t.Errorf("running=%v err=%v, want false（flock を持つプロセスがいない）", running, err)
	}
}

func TestReadLock_Broken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	for _, content := range []string{"", "not json", `{"pid":0}`} {
		_ = os.WriteFile(path, []byte(content), 0o644)
		if _, ok, err := readLock(path); ok || err != nil {
			t.Errorf("readLock(%q) = ok=%v err=%v, want 情報なし", content, ok, err)
		}
	}
	if _, ok, err := readLock(filepath.Join(t.TempDir(), "none")); ok || err != nil {
		t.Errorf("ファイルが無いとき ok=%v err=%v", ok, err)
	}
}

func TestStatusAndSignal_NotRunning(t *testing.T) {
	useTempLock(t)

	var out bytes.Buffer
	if code := run([]string{"status"}, &out, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("status exit = %d", code)
	}
	got := lastLine(t, out.Bytes())
	if got["running"] != false || got["status"] != nil {
		t.Errorf("status = %v", got)
	}

	for _, cmd := range []string{"finish", "stop"} {
		out.Reset()
		if code := run([]string{cmd}, &out, &bytes.Buffer{}); code != exitError {
			t.Errorf("%s exit = %d, want %d", cmd, code, exitError)
		}
		if got := lastLine(t, out.Bytes()); got["ok"] != false || got["error"] == "" {
			t.Errorf("%s = %v", cmd, got)
		}
	}
}

func TestStatus_Running(t *testing.T) {
	path := useTempLock(t)
	bookDir := t.TempDir()
	l, err := acquireLock(path, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer l.release()
	_ = l.setBookDir(bookDir)
	sink := newEventSink(&bytes.Buffer{}, os.Getpid(), "本")
	_ = sink.setBookDir(bookDir)
	sink.state(statusPaused, 12, "一時停止中")
	sink.close()

	var out bytes.Buffer
	if code := run([]string{"status"}, &out, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	got := lastLine(t, out.Bytes())
	st, _ := got["status"].(map[string]any)
	if got["running"] != true || got["book_dir"] != bookDir || st == nil || st["status"] != "paused" || st["page"] != float64(12) {
		t.Errorf("status = %v", got)
	}
}

func TestEventSink(t *testing.T) {
	var out bytes.Buffer
	sink := newEventSink(&out, 42, "本")
	sink.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

	// フォルダが決まる前のイベントは標準出力にだけ出て、run.jsonl 用に溜められる
	sink.logNow("info", "開始")
	sink.state(statusRunning, 0, "準備中...")
	sink.state(statusRunning, 0, "準備中...") // 同じ状態は出さない

	dir := t.TempDir()
	if err := sink.setBookDir(dir); err != nil {
		t.Fatal(err)
	}
	sink.state(statusRunning, 1, "ページ 1 を保存中...")
	sink.result(resultEvent{OK: true, Status: statusDone, BookDir: dir})
	sink.close()

	stdoutLines := jsonLines(t, out.Bytes())
	if len(stdoutLines) != 4 {
		t.Fatalf("標準出力の行数 = %d, want 4: %s", len(stdoutLines), out.String())
	}
	runJSONL, err := os.ReadFile(filepath.Join(dir, "run.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runJSONL, out.Bytes()) {
		t.Errorf("run.jsonl と標準出力が一致しません:\n%s\n---\n%s", runJSONL, out.String())
	}

	res := stdoutLines[3]
	if res["type"] != "result" || res["markdown"] != nil || res["error"] != nil {
		t.Errorf("result = %v", res)
	}
	if pdfs, ok := res["pdfs"].([]any); !ok || len(pdfs) != 0 {
		t.Errorf("pdfs は null ではなく [] にすべき: %v", res["pdfs"])
	}
	if rp, ok := res["review_pages"].([]any); !ok || len(rp) != 0 {
		t.Errorf("review_pages は null ではなく [] にすべき: %v", res["review_pages"])
	}

	var sf statusFile
	data, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatal(err)
	}
	if sf.PID != 42 || sf.Book != "本" || sf.BookDir != dir || sf.Status != statusDone || sf.Page != 1 || sf.Result == nil || !sf.Result.OK {
		t.Errorf("status.json = %+v", sf)
	}
	// 一時ファイルが残っていない
	if m, _ := filepath.Glob(filepath.Join(dir, ".status.json.tmp-*")); len(m) != 0 {
		t.Errorf("一時ファイルが残っています: %v", m)
	}
}

func TestCLIStatus(t *testing.T) {
	for _, tt := range []struct {
		name string
		st   session.State
		want string
	}{
		{"実行中", session.State{IsRunning: true, Status: session.StatusRunning}, statusRunning},
		{"一時停止", session.State{IsRunning: true, Status: session.StatusPaused}, statusPaused},
		{"PDF作成中のエラー表示", session.State{IsRunning: true, Status: session.StatusError}, statusError},
		// session は「完了」にしてからワーカーを終えるので、その間はまだ実行中として扱う
		// （CLI はこの後 Markdown を作ってから、自分で最後の done を出す）
		{"完了直後でワーカーがまだ動いている", session.State{IsRunning: true, Status: session.StatusDone, EndReason: session.EndDone}, statusRunning},
		{"一時停止の時間切れ直後でワーカーがまだ動いている", session.State{IsRunning: true, Status: session.StatusDone, EndReason: session.EndPauseTimeout}, statusRunning},
		// 中断・エラーは、終わり方が決まった時点で（ワーカーがまだ動いていても）その状態にする
		{"中断直後でワーカーがまだ動いている", session.State{IsRunning: true, Status: session.StatusIdle, EndReason: session.EndInterrupted}, statusInterrupted},
		{"エラー後にそこまでの PDF を作成中", session.State{IsRunning: true, Status: session.StatusError, EndReason: session.EndError}, statusError},
		{"完了", session.State{Status: session.StatusDone, EndReason: session.EndDone}, statusDone},
		{"一時停止の時間切れは done", session.State{Status: session.StatusDone, EndReason: session.EndPauseTimeout}, statusDone},
		{"中断（待機中に畳まれる）", session.State{Status: session.StatusIdle, EndReason: session.EndInterrupted}, statusInterrupted},
		{"エラー", session.State{Status: session.StatusError, EndReason: session.EndError}, statusError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := cliStatus(tt.st); got != tt.want {
				t.Errorf("cliStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFinishCapture(t *testing.T) {
	doneState := func(dir string) session.State {
		return session.State{Status: session.StatusDone, EndReason: session.EndDone, OutputDir: dir,
			TotalPages: 3, PDFFiles: []string{filepath.Join(dir, "本_part1.pdf")}, Message: "完了！1個のPDFを作成しました"}
	}

	t.Run("Markdown を作ってから PNG を消す", func(t *testing.T) {
		dir := t.TempDir()
		writePNGs(t, dir, 3)
		stubConvert(t, func(ctx context.Context, d, title string, progress func(done, total int)) (string, int, []int, error) {
			if countPNGs(t, d) != 3 {
				t.Error("Markdown 変換の前に PNG が消えている")
			}
			progress(3, 3)
			return filepath.Join(d, title+".md"), 3, []int{2}, nil
		})
		var out bytes.Buffer
		sink := newEventSink(&out, 1, "本")
		res, code := finishCapture(context.Background(), sink, doneState(dir), captureOptions{book: "本"}, &atomic.Bool{})
		if code != exitOK || !res.OK || res.Markdown == nil || *res.Markdown != filepath.Join(dir, "本.md") {
			t.Fatalf("code=%d res=%+v", code, res)
		}
		if len(res.ReviewPages) != 1 || res.ReviewPages[0] != 2 || len(res.PDFs) != 1 || res.Pages != 3 {
			t.Errorf("res = %+v", res)
		}
		if countPNGs(t, dir) != 0 {
			t.Error("Markdown 作成後に PNG が消えていない")
		}
		if !strings.Contains(finalMessage(doneState(dir), res), "Markdown を作成しました") {
			t.Error("最後のメッセージに Markdown の作成が無い")
		}
	})

	t.Run("Markdown に失敗したら PNG を残してエラー", func(t *testing.T) {
		dir := t.TempDir()
		writePNGs(t, dir, 3)
		// 本物の OCR は使わず、失敗する偽物に差し替える
		stubConvert(t, func(context.Context, string, string, func(int, int)) (string, int, []int, error) {
			return "", 0, nil, errors.New("OCR に失敗しました: テスト用の失敗")
		})
		var out bytes.Buffer
		res, code := finishCapture(context.Background(), newEventSink(&out, 1, "本"), doneState(dir), captureOptions{book: "本"}, &atomic.Bool{})
		if code != exitError || res.OK || res.Markdown != nil || res.Error == nil || !strings.Contains(*res.Error, "テスト用の失敗") {
			t.Fatalf("code=%d res=%+v", code, res)
		}
		if res.Status != statusDone || len(res.PDFs) != 1 {
			t.Errorf("撮影と PDF は成功しているはず: %+v", res)
		}
		if countPNGs(t, dir) != 3 {
			t.Error("Markdown に失敗したのに PNG を消した（md でやり直せない）")
		}
		if !strings.Contains(out.String(), "kindlesnap md --dir") {
			t.Error("やり直し方の案内が無い")
		}
	})

	t.Run("--no-md なら Markdown を作らず PNG を消す", func(t *testing.T) {
		dir := t.TempDir()
		writePNGs(t, dir, 3)
		stubConvert(t, func(context.Context, string, string, func(int, int)) (string, int, []int, error) {
			t.Error("--no-md なのに Markdown 変換を呼んだ")
			return "", 0, nil, nil
		})
		res, code := finishCapture(context.Background(), newEventSink(&bytes.Buffer{}, 1, "本"), doneState(dir), captureOptions{book: "本", noMD: true}, &atomic.Bool{})
		if code != exitOK || !res.OK || res.Markdown != nil {
			t.Fatalf("code=%d res=%+v", code, res)
		}
		if countPNGs(t, dir) != 0 {
			t.Error("PNG が消えていない")
		}
	})

	t.Run("--keep-png なら PNG を残す", func(t *testing.T) {
		dir := t.TempDir()
		writePNGs(t, dir, 3)
		res, code := finishCapture(context.Background(), newEventSink(&bytes.Buffer{}, 1, "本"), doneState(dir), captureOptions{book: "本", noMD: true, keepPNG: true}, &atomic.Bool{})
		if code != exitOK || !res.OK || countPNGs(t, dir) != 3 {
			t.Fatalf("code=%d res=%+v png=%d", code, res, countPNGs(t, dir))
		}
	})

	t.Run("中断は Markdown を作らず PNG を残す", func(t *testing.T) {
		dir := t.TempDir()
		writePNGs(t, dir, 2)
		stubConvert(t, func(context.Context, string, string, func(int, int)) (string, int, []int, error) {
			t.Error("中断なのに Markdown 変換を呼んだ")
			return "", 0, nil, nil
		})
		st := session.State{Status: session.StatusIdle, EndReason: session.EndInterrupted, OutputDir: dir, TotalPages: 2,
			PDFFiles: []string{"a.pdf"}, Message: "中断されました（2ページまで保存）。1個のPDFを作成しました"}
		res, code := finishCapture(context.Background(), newEventSink(&bytes.Buffer{}, 1, "本"), st, captureOptions{book: "本"}, &atomic.Bool{})
		if code != exitInterrupted || res.OK || res.Status != statusInterrupted || len(res.PDFs) != 1 {
			t.Fatalf("code=%d res=%+v", code, res)
		}
		if countPNGs(t, dir) != 2 {
			t.Error("中断で PNG を消した")
		}
		if finalMessage(st, res) != st.Message {
			t.Error("中断時の最後のメッセージは session のメッセージ（保存ページ数・PDF）を使うべき")
		}
	})

	t.Run("PDF作成中に中断を頼まれたら Markdown は作らない", func(t *testing.T) {
		dir := t.TempDir()
		var stop atomic.Bool
		stop.Store(true)
		res, code := finishCapture(context.Background(), newEventSink(&bytes.Buffer{}, 1, "本"), doneState(dir), captureOptions{book: "本"}, &stop)
		if code != exitInterrupted || res.Status != statusInterrupted || len(res.PDFs) != 1 {
			t.Fatalf("code=%d res=%+v", code, res)
		}
	})

	t.Run("エラーは session のエラーを返す", func(t *testing.T) {
		st := session.State{Status: session.StatusError, EndReason: session.EndError, Error: "撮影できません"}
		res, code := finishCapture(context.Background(), newEventSink(&bytes.Buffer{}, 1, "本"), st, captureOptions{book: "本"}, &atomic.Bool{})
		if code != exitError || res.Status != statusError || res.Error == nil || *res.Error != "撮影できません" {
			t.Fatalf("code=%d res=%+v", code, res)
		}
	})
}

func TestCmdMD(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		dir := t.TempDir()
		stubConvert(t, func(ctx context.Context, d, title string, progress func(done, total int)) (string, int, []int, error) {
			if d != dir || title != filepath.Base(dir) {
				t.Errorf("dir=%q title=%q", d, title)
			}
			progress(10, 12)
			progress(11, 12) // 10ページごとと最後だけ出す
			progress(12, 12)
			return filepath.Join(d, "x.md"), 12, []int{5}, nil
		})
		var out bytes.Buffer
		if code := run([]string{"md", "--dir", dir}, &out, &bytes.Buffer{}); code != exitOK {
			t.Fatalf("exit = %d: %s", code, out.String())
		}
		lines := jsonLines(t, out.Bytes())
		progress := 0
		for _, l := range lines {
			if l["type"] == "state" && strings.Contains(l["message"].(string), "ページ）") {
				progress++
			}
		}
		if progress != 2 {
			t.Errorf("進捗の state = %d行, want 2", progress)
		}
		res := lines[len(lines)-1]
		if res["ok"] != true || res["markdown"] != filepath.Join(dir, "x.md") || res["pages"] != float64(12) {
			t.Errorf("result = %v", res)
		}
		if _, err := os.Stat(filepath.Join(dir, "run.jsonl")); err != nil {
			t.Error("run.jsonl が無い")
		}
	})

	t.Run("page_*.png が無いとエラー", func(t *testing.T) {
		// 本物の convertMarkdown（markdown.ConvertDir）を使う。ページ画像を探す段階で
		// 失敗するので、OCR ヘルパーのビルドや Vision の呼び出しまでは進まない
		var out bytes.Buffer
		if code := run([]string{"md", "--dir", t.TempDir()}, &out, &bytes.Buffer{}); code != exitError {
			t.Fatalf("exit = %d", code)
		}
		res := lastLine(t, out.Bytes())
		if res["ok"] != false || !strings.Contains(res["error"].(string), "page_*.png が見つかりません") {
			t.Errorf("result = %v", res)
		}
	})

	t.Run("フォルダが無い", func(t *testing.T) {
		var out bytes.Buffer
		if code := run([]string{"md", "--dir", filepath.Join(t.TempDir(), "none")}, &out, &bytes.Buffer{}); code != exitError {
			t.Fatalf("exit = %d", code)
		}
	})
}

// fakeDoctorEnv はすべて成功する doctorEnv を返す（各テストで一部だけ失敗に差し替える）。
func fakeDoctorEnv() doctorEnv {
	return doctorEnv{
		macOSVersion:  func() (string, error) { return "26.6.2", nil },
		kindleRunning: func() (bool, error) { return true, nil },
		launch:        func() error { return errors.New("起動するべきでない") },
		discover: func() (capture.WindowInfo, error) {
			return capture.WindowInfo{PID: 1, WindowID: 2, OnScreen: true, Width: 100, Height: 80}, nil
		},
		testCapture:   func() error { return nil },
		accessibility: func() error { return nil },
		ocr:           func() error { return nil },
		sleep:         func(time.Duration) {},
		launchWait:    5 * time.Second,
	}
}

func checkByName(checks []check, name string) check {
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	return check{Name: "（無い）"}
}

func TestRunDoctor(t *testing.T) {
	t.Run("すべて成功", func(t *testing.T) {
		checks := runDoctor(fakeDoctorEnv(), false)
		if !allOK(checks) || len(checks) != 7 {
			t.Errorf("checks = %+v", checks)
		}
		for _, c := range checks {
			if c.Fix != "" {
				t.Errorf("成功した項目に fix がある: %+v", c)
			}
		}
	})

	t.Run("古いmacOS", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.macOSVersion = func() (string, error) { return "15.5", nil }
		if c := checkByName(runDoctor(env, false), "macos"); c.OK || c.Fix == "" {
			t.Errorf("macos = %+v", c)
		}
	})

	t.Run("--launch で起動してウィンドウを待つ", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.kindleRunning = func() (bool, error) { return false, nil }
		launched := false
		env.launch = func() error { launched = true; return nil }
		calls := 0
		ok := env.discover
		env.discover = func() (capture.WindowInfo, error) {
			calls++
			if calls < 3 {
				return capture.WindowInfo{}, errors.New("まだ無い")
			}
			return ok()
		}
		checks := runDoctor(env, true)
		if !launched || !allOK(checks) || calls != 3 {
			t.Errorf("launched=%v calls=%d checks=%+v", launched, calls, checks)
		}
	})

	t.Run("--launch でもウィンドウが出なければ上限で諦める", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.kindleRunning = func() (bool, error) { return false, nil }
		env.launch = func() error { return nil }
		calls := 0
		env.discover = func() (capture.WindowInfo, error) { calls++; return capture.WindowInfo{}, errors.New("無い") }
		checks := runDoctor(env, true)
		if allOK(checks) || calls != 6 { // 最初の1回 + 1秒ごとに5回（launchWait=5秒）
			t.Errorf("calls=%d", calls)
		}
		for _, name := range []string{"kindle_window", "window_onscreen", "screen_recording"} {
			if c := checkByName(checks, name); c.OK {
				t.Errorf("%s は失敗にすべき: %+v", name, c)
			}
		}
	})

	t.Run("起動していなければ --launch を案内", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.kindleRunning = func() (bool, error) { return false, nil }
		if c := checkByName(runDoctor(env, false), "kindle_running"); c.OK || !strings.Contains(c.Fix, "--launch") {
			t.Errorf("kindle_running = %+v", c)
		}
	})

	t.Run("画面外でも撮れれば成功", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.discover = func() (capture.WindowInfo, error) { return capture.WindowInfo{WindowID: 2, OnScreen: false}, nil }
		if checks := runDoctor(env, false); !allOK(checks) {
			t.Errorf("checks = %+v", checks)
		}
	})

	t.Run("画面外で撮れなければフルスクリーン・最小化を案内", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.discover = func() (capture.WindowInfo, error) { return capture.WindowInfo{WindowID: 2, OnScreen: false}, nil }
		env.testCapture = func() error { return errors.New("could not create image from window") }
		checks := runDoctor(env, false)
		if c := checkByName(checks, "window_onscreen"); c.OK || !strings.Contains(c.Fix, "フルスクリーン") {
			t.Errorf("window_onscreen = %+v", c)
		}
		if c := checkByName(checks, "screen_recording"); c.OK || strings.Contains(c.Fix, "画面収録") {
			t.Errorf("画面外が原因なのに権限を案内した: %+v", c)
		}
	})

	t.Run("画面上で撮れなければ画面収録の権限を案内", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.testCapture = func() error { return errors.New("could not create image from window") }
		if c := checkByName(runDoctor(env, false), "screen_recording"); c.OK || !strings.Contains(c.Fix, "画面収録") {
			t.Errorf("screen_recording = %+v", c)
		}
	})

	t.Run("権限とOCR", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.accessibility = func() error { return errors.New("権限なし") }
		env.ocr = func() error { return errors.New("swiftc なし") }
		checks := runDoctor(env, false)
		if checkByName(checks, "accessibility").OK || checkByName(checks, "ocr").OK {
			t.Errorf("checks = %+v", checks)
		}
	})

	t.Run("OCR の準備不足は ocr.SetupError の対処を出す", func(t *testing.T) {
		env := fakeDoctorEnv()
		env.ocr = func() error {
			return fmt.Errorf("包んだエラー: %w", &ocr.SetupError{Msg: "macOS が古い", Fix: "macOS 26 以降にアップデートする"})
		}
		c := checkByName(runDoctor(env, false), "ocr")
		if c.OK || !strings.Contains(c.Fix, "macOS 26 以降にアップデートする") || strings.Contains(c.Fix, "xcode-select") {
			t.Errorf("ocr = %+v", c)
		}
	})
}

func TestParseMajor(t *testing.T) {
	for v, want := range map[string]int{"26.6.2": 26, "26": 26, "15.5": 15} {
		if got, err := parseMajor(v); err != nil || got != want {
			t.Errorf("parseMajor(%q) = %d, %v", v, got, err)
		}
	}
	if _, err := parseMajor("abc"); err == nil {
		t.Error("数字でなければエラー")
	}
}

func TestToGlobal(t *testing.T) {
	win := capture.WindowInfo{X: -1440, Y: 25, Width: 1200, Height: 800}
	g, err := toGlobal(win, 100, 50.5)
	if err != nil || g.X != -1340 || g.Y != 75.5 {
		t.Errorf("toGlobal = %+v, %v", g, err)
	}
	for _, p := range [][2]float64{{-1, 0}, {0, -1}, {1201, 0}, {0, 801}} {
		if _, err := toGlobal(win, p[0], p[1]); err == nil {
			t.Errorf("ウィンドウ外 %v はエラーにすべき", p)
		}
	}
}
