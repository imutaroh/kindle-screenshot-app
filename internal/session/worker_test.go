package session

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/frame"
)

// テスト画像はすべて合成（書籍の実画像はリポジトリに入れない）。
// 白い紙面に「文字の列」を模した黒い縦帯を、ページごとに違う位置へ描く。

const testW, testH = 160, 120

// makePage はページ番号 i に固有の画面を作る。
func makePage(i int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, testW, testH))
	fillRect(img, img.Bounds(), color.NRGBA{255, 255, 255, 255})
	for k := 0; k < 3; k++ {
		x := 20 + (i*17+k*37)%110
		fillRect(img, image.Rect(x, 20, x+8, 100), color.NRGBA{0, 0, 0, 255})
	}
	return img
}

func fillRect(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// withBlock は img の複製に灰色の帯を足した画面を返す（描画途中・ノイズの再現用）。
func withBlock(img *image.NRGBA, x int) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	copy(out.Pix, img.Pix)
	fillRect(out, image.Rect(x, 30, x+12, 90), color.NRGBA{128, 128, 128, 255})
	return out
}

// fakeKindle は kindleDriver の偽物。pages[idx] を「今の画面」として撮らせ、
// TurnPage で idx を進める。最終ページで TurnPage しても画面は変わらない（本の最後）。
type fakeKindle struct {
	mu    sync.Mutex
	pages []*image.NRGBA
	idx   int

	keys        int          // TurnPage が呼ばれた回数
	ignoreKey   map[int]bool // この番号（1始まり）のキーを無視する（キー不感の再現）
	transition  int          // ページ送り直後、この枚数だけ描画途中の画面を返す
	pendingMid  int
	noisy       map[int]bool // このページは撮るたびに画面が変わる（描画が安定しない）
	shots       int
	failNext    int  // 次のこの回数の撮影を失敗させる
	alwaysFail  bool // 撮影を常に失敗させる（最小化などの再現）
	windowWidth float64

	runawayAt int  // この番号（1始まり）のキーで「押しっぱなし」扱いになり、ページ送りが暴走する
	running   bool // 暴走中は撮るたびに1ページ進む。次のキーで止まる（実機と同じ）
	backKeys  int  // 逆方向（capture.Right）のキーが呼ばれた回数
}

func newFakeKindle(n int) *fakeKindle {
	f := &fakeKindle{ignoreKey: map[int]bool{}, noisy: map[int]bool{}, windowWidth: testW}
	for i := 0; i < n; i++ {
		f.pages = append(f.pages, makePage(i))
	}
	return f
}

func (f *fakeKindle) Screenshot(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shots++
	os.Remove(path)
	if f.alwaysFail {
		return errors.New("could not create image from window")
	}
	if f.failNext > 0 {
		f.failNext--
		return errors.New("could not create image from window")
	}
	if f.running && f.idx < len(f.pages)-1 {
		f.idx++
	}
	img := f.pages[f.idx]
	switch {
	case f.pendingMid > 0:
		f.pendingMid--
		img = withBlock(img, 10+f.pendingMid*20)
	case f.noisy[f.idx]:
		img = withBlock(img, 10+(f.shots%8)*15)
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, img)
}

func (f *fakeKindle) TurnPage(dir capture.Direction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys++
	f.running = false // 新しいキーで「押しっぱなし」が解除される
	if f.ignoreKey[f.keys] {
		return nil
	}
	// テストでは capture.Left を「次のページ」として使う。capture.Right は前のページへ戻る
	if dir == capture.Right {
		f.backKeys++
		if f.idx > 0 {
			f.idx--
			f.pendingMid = f.transition
		}
		return nil
	}
	if f.idx < len(f.pages)-1 {
		f.idx++
		f.pendingMid = f.transition
	}
	if f.keys == f.runawayAt {
		f.running = true
	}
	return nil
}

func (f *fakeKindle) WindowWidth() float64 { return f.windowWidth }

// newTestManager は待ち時間を短くした Manager を作る。
// 安定待ちのタイムアウトは、-race や並列実行で1回のポーリング（PNG の書き出し＋読み込み）が
// 数十msかかっても、描画途中の画面を経て安定するまでの数回が収まるよう余裕を持たせている。
func newTestManager(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	m := NewManager(t.TempDir())
	m.timing = timing{
		poll:              2 * time.Millisecond,
		settle:            250 * time.Millisecond,
		initialSettle:     400 * time.Millisecond,
		resendDelay:       5 * time.Millisecond,
		pausedKeyInterval: 20 * time.Millisecond,
		pauseLimit:        5 * time.Second,
		captureFailWindow: 30 * time.Millisecond,
	}
	var mu sync.Mutex
	notified := []string{}
	m.notify = func(title, message string) {
		mu.Lock()
		notified = append(notified, message)
		mu.Unlock()
	}
	return m, &notified
}

// waitFor は cond が true になるまで待つ（最大5秒）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s を待っている間にタイムアウトしました", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// assertSavedPages は outDir の page_0001.png〜 が want の順に pages と一致すること
// （飛ばし・重複・描画途中の画面が無いこと）と、ポーリング用ファイルが残っていないことを確かめる。
func assertSavedPages(t *testing.T, outDir string, f *fakeKindle, want []int) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(outDir, "page_*.png"))
	if len(files) != len(want) {
		t.Fatalf("保存されたページ数 = %d, want %d (%v)", len(files), len(want), files)
	}
	for i, idx := range want {
		got, err := frame.Load(pagePath(outDir, i+1), testW)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Same(frame.FromImage(f.pages[idx], testW)) {
			t.Errorf("page_%04d.png が pages[%d] と一致しません（飛ばし・重複・描画途中の画面）", i+1, idx)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, pollFileName)); !os.IsNotExist(err) {
		t.Errorf("%s が残っています", pollFileName)
	}
}

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

func logText(m *Manager) string {
	var b strings.Builder
	for _, e := range m.Logs() {
		b.WriteString(e.Text + "\n")
	}
	return b.String()
}

func logsContain(m *Manager, sub string) bool {
	for _, l := range m.Logs() {
		if strings.Contains(l.Text, sub) {
			return true
		}
	}
	return false
}

func TestClassify(t *testing.T) {
	left := frame.FromImage(makePage(0), testW)
	leftAgain := frame.FromImage(makePage(0), testW)
	next := frame.FromImage(makePage(1), testW)
	nextAgain := frame.FromImage(makePage(1), testW)
	var zero frame.Frame

	tests := []struct {
		name               string
		cur, prev, last    frame.Frame
		wantChanged, wantS bool
	}{
		{"前ページと同じままなら変化なし", leftAgain, left, leftAgain, false, true},
		{"変化したが直前ポーリングと違う（まだ描画中）", next, left, left, true, false},
		{"変化して直前ポーリングと同じ", next, left, nextAgain, true, true},
		{"1ページ目（prev未計算）は無条件に変化あり", next, zero, nextAgain, true, true},
		{"最初のポーリング（last未計算）は安定とみなさない", next, zero, zero, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, stable := classify(tt.cur, tt.prev, tt.last)
			if changed != tt.wantChanged || stable != tt.wantS {
				t.Errorf("classify = (changed=%v, stable=%v), want (%v, %v)", changed, stable, tt.wantChanged, tt.wantS)
			}
		})
	}
}

func TestCaptureSettled(t *testing.T) {
	t.Run("描画途中の画面は採用せず安定した画面を採用する", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		f.idx, f.pendingMid = 1, 3
		poll := filepath.Join(t.TempDir(), pollFileName)
		res, cur, err := m.captureSettled(f, poll, frame.FromImage(f.pages[0], testW), m.timing.settle, 2)
		if err != nil || res != settleAccepted {
			t.Fatalf("res=%v err=%v, want settleAccepted", res, err)
		}
		if !cur.Same(frame.FromImage(f.pages[1], testW)) {
			t.Error("描画途中の画面を採用した")
		}
	})

	t.Run("安定しない画面は延長後に Unstable で採用", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		f.idx = 1
		f.noisy[1] = true
		poll := filepath.Join(t.TempDir(), pollFileName)
		start := time.Now()
		res, _, err := m.captureSettled(f, poll, frame.FromImage(f.pages[0], testW), m.timing.settle, 1)
		if err != nil || res != settleUnstable {
			t.Fatalf("res=%v err=%v, want settleUnstable", res, err)
		}
		if el := time.Since(start); el < 2*m.timing.settle {
			t.Errorf("延長せずに諦めた（%v < %v）", el, 2*m.timing.settle)
		}
	})

	t.Run("変化しなければ Unchanged", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(1)
		poll := filepath.Join(t.TempDir(), pollFileName)
		res, _, err := m.captureSettled(f, poll, frame.FromImage(f.pages[0], testW), m.timing.settle, 1)
		if err != nil || res != settleUnchanged {
			t.Fatalf("res=%v err=%v, want settleUnchanged", res, err)
		}
	})

	t.Run("一時的な撮影失敗は続行する", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		// 回数は minCaptureFailures を超えるが、失敗が続いた時間が短いのでエラーにしない
		m.timing.captureFailWindow = 5 * time.Second
		f.idx, f.failNext = 1, 5
		poll := filepath.Join(t.TempDir(), pollFileName)
		res, _, err := m.captureSettled(f, poll, frame.FromImage(f.pages[0], testW), m.timing.settle, 1)
		if err != nil || res != settleAccepted {
			t.Fatalf("res=%v err=%v, want settleAccepted", res, err)
		}
	})

	t.Run("連続する撮影失敗は原因候補つきのエラー", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(1)
		f.alwaysFail = true
		poll := filepath.Join(t.TempDir(), pollFileName)
		_, _, err := m.captureSettled(f, poll, frame.Frame{}, m.timing.settle, 1)
		if err == nil || !strings.Contains(err.Error(), "最小化") {
			t.Fatalf("err = %v, want 原因候補を含むエラー", err)
		}
	})

	t.Run("停止要求で Stopped", func(t *testing.T) {
		m, _ := newTestManager(t)
		m.isRunning = true
		_ = m.Stop()
		f := newFakeKindle(1)
		res, _, err := m.captureSettled(f, filepath.Join(t.TempDir(), pollFileName), frame.Frame{}, m.timing.settle, 1)
		if err != nil || res != settleStopped {
			t.Fatalf("res=%v err=%v, want settleStopped", res, err)
		}
	})
}

func TestCaptureLoop_MaxPagesWithTransitions(t *testing.T) {
	m, notified := newTestManager(t)
	f := newFakeKindle(8)
	f.transition = 2
	outDir := t.TempDir()

	captured, end, err := m.captureLoop(f, outDir, 7, capture.Left)
	if err != nil {
		t.Fatal(err)
	}
	if captured != 7 || end != endMaxPages {
		t.Fatalf("captured=%d end=%v, want 7 endMaxPages", captured, end)
	}
	assertSavedPages(t, outDir, f, seq(7))
	if len(*notified) != 0 {
		t.Errorf("一時停止していないのに通知した: %v", *notified)
	}
}

func TestCaptureLoop_ResendRecoversWithoutSkipping(t *testing.T) {
	m, notified := newTestManager(t)
	f := newFakeKindle(5)
	f.ignoreKey[2] = true // 2ページ目→3ページ目のキーだけ効かない
	outDir := t.TempDir()

	captured, end, err := m.captureLoop(f, outDir, 5, capture.Left)
	if err != nil {
		t.Fatal(err)
	}
	if captured != 5 || end != endMaxPages {
		t.Fatalf("captured=%d end=%v, want 5 endMaxPages", captured, end)
	}
	assertSavedPages(t, outDir, f, seq(5))
	if !logsContain(m, "キーを再送します（1/2）") {
		t.Error("再送のログが無い")
	}
	if logsContain(m, "一時停止") || len(*notified) != 0 {
		t.Error("再送で復旧したのに一時停止した")
	}
}

// TestCaptureLoop_RunawayRewinds は、キーの押しっぱなし扱いでページ送りが暴走しても、
// 止めて前ページまで巻き戻し、1枚も飛ばさずに撮り続けることを確かめる。
func TestCaptureLoop_RunawayRewinds(t *testing.T) {
	m, _ := newTestManager(t)
	// 実機の暴走は撮るたびに画面が変わる（1秒に3〜4ページ）。偽物は撮るたびに1ページ進むので、
	// ポーリング間隔を長めにして、暴走が本の最後まで届かない（十数ページで止まる）ようにする
	m.timing.poll = 20 * time.Millisecond
	m.timing.settle = 100 * time.Millisecond
	f := newFakeKindle(60)
	f.runawayAt = 3 // 3ページ目→4ページ目のキーで暴走
	outDir := t.TempDir()

	captured, end, err := m.captureLoop(f, outDir, 8, capture.Left)
	if err != nil {
		t.Fatal(err)
	}
	if captured != 8 || end != endMaxPages {
		t.Fatalf("captured=%d end=%v, want 8 endMaxPages", captured, end)
	}
	assertSavedPages(t, outDir, f, seq(8))
	if f.backKeys == 0 {
		t.Error("巻き戻しのキー（逆方向）を送っていない")
	}
	if !logsContain(m, "巻き戻しました") {
		t.Error("巻き戻しのログが無い")
	}
}

// TestCaptureLoop_NoisyPageNotRewoundTwice は、描画が重いだけのページ（毎回画面が変わる）で
// 巻き戻しを繰り返して止まらなくなったりせず、1回だけ確かめて採用することを確かめる。
func TestCaptureLoop_NoisyPageNotRewoundTwice(t *testing.T) {
	m, _ := newTestManager(t)
	f := newFakeKindle(5)
	f.noisy[2] = true // 3ページ目は撮るたびに画面が変わる
	outDir := t.TempDir()

	captured, _, err := m.captureLoop(f, outDir, 5, capture.Left)
	if err != nil {
		t.Fatal(err)
	}
	if captured != 5 {
		t.Fatalf("captured=%d, want 5", captured)
	}
	// 暴走を止めるキーで1枚進むので、前ページまで戻るのに逆方向のキーは2回。
	// 撮り直しでまた不安定でも、2回目は巻き戻さない（1ページにつき1回まで）
	if f.backKeys != 2 {
		t.Errorf("逆方向のキー = %d回, want 2", f.backKeys)
	}
	if n := strings.Count(logText(m), "巻き戻しました"); n != 1 {
		t.Errorf("巻き戻しの回数 = %d, want 1", n)
	}
	if !logsContain(m, "描画が安定しなかった可能性") {
		t.Error("2回目の不安定で採用したときの警告が無い")
	}
}

func TestCaptureLoop_PauseThenAutoResume(t *testing.T) {
	m, notified := newTestManager(t)
	f := newFakeKindle(5)
	// 3ページ目→4ページ目: 最初のキー＋再送2回＋一時停止中の2回が効かず、6回目で復旧
	for k := 3; k <= 7; k++ {
		f.ignoreKey[k] = true
	}
	outDir := t.TempDir()

	captured, end, err := m.captureLoop(f, outDir, 5, capture.Left)
	if err != nil {
		t.Fatal(err)
	}
	if captured != 5 || end != endMaxPages {
		t.Fatalf("captured=%d end=%v, want 5 endMaxPages", captured, end)
	}
	assertSavedPages(t, outDir, f, seq(5))
	if f.keys != 9 {
		// 効いたページ送り4回（最後のページの後は送らない）＋無視された5回
		t.Errorf("送ったキーの回数 = %d, want 9（変化を確認する前に追加で送っていないか）", f.keys)
	}
	if !logsContain(m, "一時停止") || !logsContain(m, "再開しました") {
		t.Error("一時停止→再開のログが無い")
	}
	if len(*notified) != 1 {
		t.Errorf("通知回数 = %d, want 1", len(*notified))
	}
	if st := m.Snapshot(); st.Status != StatusRunning {
		t.Errorf("再開後の status = %q, want %q", st.Status, StatusRunning)
	}
}

func TestCaptureLoop_Resolution(t *testing.T) {
	t.Run("1xなら画質注意を出す", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		if _, _, err := m.captureLoop(f, t.TempDir(), 1, capture.Left); err != nil {
			t.Fatal(err)
		}
		if got, want := m.Snapshot().Resolution, "160x120px（1x）"; got != want {
			t.Errorf("Resolution = %q, want %q", got, want)
		}
		if !logsContain(m, "画質注意") {
			t.Error("1x なのに画質注意のログが無い")
		}
	})

	t.Run("2xなら画質注意を出さない", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		f.windowWidth = testW / 2
		if _, _, err := m.captureLoop(f, t.TempDir(), 1, capture.Left); err != nil {
			t.Fatal(err)
		}
		if got, want := m.Snapshot().Resolution, "160x120px（2x）"; got != want {
			t.Errorf("Resolution = %q, want %q", got, want)
		}
		if logsContain(m, "画質注意") {
			t.Error("2x なのに画質注意のログが出た")
		}
	})
}

func TestFormatResolution(t *testing.T) {
	tests := []struct {
		w, h int
		pt   float64
		want string
	}{
		{1720, 1410, 1720, "1720x1410px（1x）"},
		{2940, 1846, 1470, "2940x1846px（2x）"},
		{2580, 1800, 1720, "2580x1800px（1.5x）"},
		{1720, 1410, 0, "1720x1410px"},
	}
	for _, tt := range tests {
		if got := formatResolution(tt.w, tt.h, tt.pt); got != tt.want {
			t.Errorf("formatResolution(%d, %d, %g) = %q, want %q", tt.w, tt.h, tt.pt, got, tt.want)
		}
	}
}

// startRunCapture は runCapture を goroutine で動かす（Finish/Stop は isRunning が前提）。
func startRunCapture(m *Manager, f *fakeKindle, outDir string, s Settings) <-chan struct{} {
	m.mu.Lock()
	m.isRunning = true
	m.outputDir = outDir
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.runCapture(f, outDir, s)
		m.mu.Lock()
		m.isRunning = false
		m.mu.Unlock()
	}()
	return done
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runCapture が終わりません")
	}
}

func pdfCount(t *testing.T, dir string) int {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
	return len(files)
}

func testSettings() Settings {
	return Settings{BookName: "テスト本", PDFPagesPerFile: 50, AutoCrop: true, Direction: capture.Left}
}

func TestRunCapture_FinishWhilePausedAtLastPage(t *testing.T) {
	m, _ := newTestManager(t)
	f := newFakeKindle(4) // 4ページ目が本の最後（以降のキーは画面を変えない）
	outDir := t.TempDir()

	done := startRunCapture(m, f, outDir, testSettings())
	waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
	st := m.Snapshot()
	if !st.IsRunning || st.Message != pausedMessage {
		t.Errorf("一時停止中の状態: IsRunning=%v Message=%q", st.IsRunning, st.Message)
	}
	if err := m.Finish(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)

	st = m.Snapshot()
	if st.Status != StatusDone || !strings.HasPrefix(st.Message, "完了！") {
		t.Errorf("status=%q message=%q, want 完了", st.Status, st.Message)
	}
	if st.TotalPages != 4 {
		t.Errorf("TotalPages = %d, want 4", st.TotalPages)
	}
	if pdfCount(t, outDir) != 1 {
		t.Error("PDF が作られていない")
	}
	if _, err := os.Stat(filepath.Join(outDir, pollFileName)); !os.IsNotExist(err) {
		t.Errorf("%s が残っています", pollFileName)
	}
}

func TestRunCapture_PauseTimeout(t *testing.T) {
	m, _ := newTestManager(t)
	m.timing.pauseLimit = 500 * time.Millisecond
	f := newFakeKindle(2)
	outDir := t.TempDir()

	done := startRunCapture(m, f, outDir, testSettings())
	waitDone(t, done)

	st := m.Snapshot()
	if st.Status != StatusDone || !strings.Contains(st.Message, "最終ページか確認できないまま終了しました（2 ページ）") {
		t.Errorf("status=%q message=%q", st.Status, st.Message)
	}
	if pdfCount(t, outDir) != 1 {
		t.Error("PDF が作られていない")
	}
}

func TestRunCapture_StopKeepsPDF(t *testing.T) {
	m, _ := newTestManager(t)
	f := newFakeKindle(3)
	outDir := t.TempDir()
	s := testSettings()
	s.AutoDeletePNG = false

	done := startRunCapture(m, f, outDir, s)
	waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)

	st := m.Snapshot()
	if st.Status != StatusIdle || !strings.Contains(st.Message, "中断されました（3ページまで保存）") || !strings.Contains(st.Message, "PDF") {
		t.Errorf("status=%q message=%q", st.Status, st.Message)
	}
	if pdfCount(t, outDir) != 1 {
		t.Error("中断でも PDF は作るべき")
	}
	assertSavedPages(t, outDir, f, seq(3))
}

func TestRunCapture_ErrorKeepsPDF(t *testing.T) {
	m, _ := newTestManager(t)
	f := newFakeKindle(3)
	outDir := t.TempDir()

	done := startRunCapture(m, f, outDir, testSettings())
	waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
	f.mu.Lock()
	f.alwaysFail = true // 一時停止中に Kindle が最小化された
	f.mu.Unlock()
	waitDone(t, done)

	st := m.Snapshot()
	if st.Status != StatusError || !strings.Contains(st.Message, "それまでの 3 ページで") {
		t.Errorf("status=%q message=%q", st.Status, st.Message)
	}
	if pdfCount(t, outDir) != 1 {
		t.Error("エラー終了でも撮れたページの PDF は作るべき")
	}
}

func TestFinishAndStop_NotRunning(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Finish(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Finish = %v, want ErrNotRunning", err)
	}
	if err := m.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Stop = %v, want ErrNotRunning", err)
	}
}

// TestRunCapture_NotPausedDuringFinalize は、一時停止から抜けた後の PDF 作成（余白検出・
// PDF結合）の間、status が StatusPaused のまま残らないことを確かめる。残ると「一時停止」の
// 状態が出続け、finish / stop の要求が効いていないように見える。
func TestRunCapture_NotPausedDuringFinalize(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  func(m *Manager) error // nil なら一時停止の時間切れを待つ
	}{
		{"終了してPDF作成", (*Manager).Finish},
		{"停止", (*Manager).Stop},
		{"一時停止の時間切れ", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestManager(t)
			if tt.end == nil {
				m.timing.pauseLimit = 300 * time.Millisecond
			}
			// 余白検出の進捗（10ページごと）が出るよう、ページ数を多めにする
			f := newFakeKindle(30)
			outDir := t.TempDir()

			done := startRunCapture(m, f, outDir, testSettings())
			waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
			if tt.end != nil {
				if err := tt.end(m); err != nil {
					t.Fatal(err)
				}
			}

			sawFinalize := false
			for {
				st := m.Snapshot()
				if st.Status == StatusPaused && st.Message != pausedMessage {
					t.Fatalf("PDF 作成中なのに一時停止のまま: Message=%q", st.Message)
				}
				if st.IsRunning && (strings.HasPrefix(st.Message, "PDF結合中") || strings.HasPrefix(st.Message, "余白を検出中")) {
					sawFinalize = true
					if st.Status != StatusRunning {
						t.Errorf("PDF 作成中の status = %q, want %q", st.Status, StatusRunning)
					}
				}
				select {
				case <-done:
				default:
					continue
				}
				break
			}
			if !sawFinalize {
				t.Error("PDF 作成中の状態を一度も観測できませんでした")
			}
			if pdfCount(t, outDir) != 1 {
				t.Error("PDF が作られていない")
			}
		})
	}
}
