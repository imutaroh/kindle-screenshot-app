package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// hookRecorder は OnLog / OnState に渡された値を記録する。
// フックはワーカー goroutine から呼ばれるので、テスト側から読むときはロックを取る。
type hookRecorder struct {
	mu     sync.Mutex
	logs   []LogEntry
	states []State
}

func (r *hookRecorder) attach(m *Manager) {
	m.OnLog = func(e LogEntry) {
		r.mu.Lock()
		r.logs = append(r.logs, e)
		r.mu.Unlock()
	}
	m.OnState = func(st State) {
		// フックの中から Manager を呼んでもデッドロックしない（ミューテックスの外で呼ばれる）ことも確かめる
		_ = m.Snapshot()
		r.mu.Lock()
		r.states = append(r.states, st)
		r.mu.Unlock()
	}
}

func (r *hookRecorder) snapshot() ([]LogEntry, []State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogEntry(nil), r.logs...), append([]State(nil), r.states...)
}

// TestHooks_FinishWithDeferredPNGDelete は、OnLog / OnState が呼ばれること、最終状態に
// PDF の一覧と終わり方（EndReason）が載ること、DeferPNGDelete なら AutoDeletePNG でも
// finalize の中で PNG を消さないこと（CLI が Markdown を作ってから消すため）を確かめる。
func TestHooks_FinishWithDeferredPNGDelete(t *testing.T) {
	m, _ := newTestManager(t)
	var rec hookRecorder
	rec.attach(m)
	f := newFakeKindle(3)
	outDir := t.TempDir()
	s := testSettings()
	s.AutoDeletePNG = true
	s.DeferPNGDelete = true

	done := startRunCapture(m, f, outDir, s)
	waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
	if err := m.Finish(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done)

	logs, states := rec.snapshot()
	if len(logs) == 0 || len(logs) != len(m.Logs()) {
		t.Errorf("OnLog の呼び出し回数 = %d, want ログ件数 %d", len(logs), len(m.Logs()))
	}
	if len(states) == 0 {
		t.Fatal("OnState が一度も呼ばれていない")
	}

	sawPaused, sawPage3 := false, false
	for _, st := range states {
		if st.Status == StatusPaused {
			sawPaused = true
		}
		if st.CurrentPage == 3 {
			sawPage3 = true
		}
	}
	if !sawPaused || !sawPage3 {
		t.Errorf("途中経過の通知が足りない: 一時停止=%v ページ3=%v", sawPaused, sawPage3)
	}

	last := states[len(states)-1]
	if last.Status != StatusDone || last.EndReason != EndDone {
		t.Errorf("最後の通知: status=%q endReason=%q, want 完了/done", last.Status, last.EndReason)
	}
	if len(last.PDFFiles) != 1 || filepath.Dir(last.PDFFiles[0]) != outDir {
		t.Errorf("PDFFiles = %v, want outDir 直下の PDF 1個", last.PDFFiles)
	}
	if _, err := os.Stat(last.PDFFiles[0]); err != nil {
		t.Errorf("PDFFiles のファイルが無い: %v", err)
	}
	if last.CropRect.Empty() {
		t.Error("AutoCrop なのに CropRect が空")
	}

	// DeferPNGDelete: AutoDeletePNG=true でも PNG は残っている
	assertSavedPages(t, outDir, f, seq(3))
}

// TestHooks_SnapshotPDFFilesIsCopy は、Snapshot が返す PDFFiles を書き換えても
// Manager の内部状態に響かないことを確かめる（フック先で加工されても壊れないように）。
func TestHooks_SnapshotPDFFilesIsCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	m.pdfFiles = []string{"a.pdf"}
	st := m.Snapshot()
	st.PDFFiles[0] = "changed.pdf"
	if got := m.Snapshot().PDFFiles[0]; got != "a.pdf" {
		t.Errorf("内部の PDFFiles が書き換わった: %q", got)
	}
}

// TestEndReason は終わり方ごとに EndReason が区別されることを確かめる。
// Status だけでは「中断」が StatusIdle、「一時停止の時間切れ」が StatusDone に畳まれて見分けられない。
func TestEndReason(t *testing.T) {
	t.Run("停止は interrupted", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		outDir := t.TempDir()
		done := startRunCapture(m, f, outDir, testSettings())
		waitFor(t, "一時停止", func() bool { return m.Snapshot().Status == StatusPaused })
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
		waitDone(t, done)
		st := m.Snapshot()
		if st.EndReason != EndInterrupted {
			t.Errorf("EndReason = %q, want %q", st.EndReason, EndInterrupted)
		}
		if len(st.PDFFiles) != 1 {
			t.Errorf("中断でも PDFFiles は載るべき: %v", st.PDFFiles)
		}
	})

	t.Run("一時停止の時間切れは pause-timeout", func(t *testing.T) {
		m, _ := newTestManager(t)
		m.timing.pauseLimit = 300 * time.Millisecond
		f := newFakeKindle(2)
		done := startRunCapture(m, f, t.TempDir(), testSettings())
		waitDone(t, done)
		if got := m.Snapshot().EndReason; got != EndPauseTimeout {
			t.Errorf("EndReason = %q, want %q", got, EndPauseTimeout)
		}
	})

	t.Run("撮影エラーは error", func(t *testing.T) {
		m, _ := newTestManager(t)
		f := newFakeKindle(2)
		f.alwaysFail = true
		done := startRunCapture(m, f, t.TempDir(), testSettings())
		waitDone(t, done)
		st := m.Snapshot()
		if st.EndReason != EndError || st.Status != StatusError {
			t.Errorf("EndReason=%q Status=%q, want error/エラー", st.EndReason, st.Status)
		}
	})
}

// TestWait_NotStarted は、一度も Start していない Manager の Wait がすぐ返ることを確かめる。
func TestWait_NotStarted(t *testing.T) {
	m := NewManager(t.TempDir())
	ch := make(chan struct{})
	go func() {
		m.Wait()
		close(ch)
	}()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("Start していないのに Wait が返らない")
	}
}
