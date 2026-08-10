// Package session は Web UI 版のキャプチャワーカーを管理する。
// Python 版 src/server.py の capture_worker + CaptureState の Go 移植。
//
// Manager はミューテックスで保護された状態を持ち、Start でバックグラウンド
// goroutine を起動してキャプチャ〜PDF結合までを進める。/api/status はこの
// 状態をポーリングで読み取るだけの薄いハンドラになる（internal/server が担当）。
package session

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/capture"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/output"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/pdf"
)

// ステータス文字列。Python 版 src/server.py の CaptureState.status と一字一句同じ日本語文字列。
// web/static/script.js はバッジ表示にこの値をそのまま使う。
const (
	StatusIdle    = "待機中"
	StatusRunning = "実行中"
	StatusDone    = "完了"
	StatusError   = "エラー"
)

// デフォルト設定値。Python 版 src/config.py と同じ値。GET /api/config はこれらをそのまま返す。
const (
	DefaultStartDelay          = 5 * time.Second
	DefaultPDFPagesPerFile     = 50
	DefaultMaxPages            = 0
	DefaultPageWaitTime        = 500 * time.Millisecond
	DefaultInitialPagesCount   = 5
	DefaultInitialPageWaitTime = 2 * time.Second
	DefaultNormalPageWaitTime  = 500 * time.Millisecond
	DefaultDuplicateThreshold  = 5
	DefaultDuplicateCheckCount = 2

	// cornerThresholdPx はホットコーナー判定の角からの距離（px）。Python版 config.CORNER_THRESHOLD_PX と同じ。
	cornerThresholdPx = 10.0
)

// DefaultDirection は既定のページめくり方向（日本語の本＝右綴じ）。
const DefaultDirection = capture.Left

// ErrAlreadyRunning は実行中に Start を呼んだ場合に返る。
var ErrAlreadyRunning = errors.New("既に実行中です")

// ErrNotRunning は未実行時に Stop を呼んだ場合に返る。
var ErrNotRunning = errors.New("実行中ではありません")

// State は /api/status が返す値のスナップショット（呼び出し時点のコピー）。
type State struct {
	IsRunning   bool
	CurrentPage int
	TotalPages  int
	Status      string
	Message     string
	OutputDir   string // 未設定なら空文字（Python版の None 相当。JSON化はserver側の責務）
	Error       string // 未設定なら空文字（Python版の None 相当）
}

// Manager はキャプチャ処理の状態とライフサイクルを管理する。ゼロ値では使えない。NewManager で作る。
type Manager struct {
	outRoot string

	mu          sync.Mutex
	isRunning   bool
	shouldStop  bool
	currentPage int
	totalPages  int
	status      string
	message     string
	outputDir   string
	bookName    string
	err         string
}

// NewManager は出力先ルートフォルダ outRoot を紐付けた Manager を作る。
func NewManager(outRoot string) *Manager {
	return &Manager{
		outRoot: outRoot,
		status:  StatusIdle,
	}
}

// IsRunning は現在キャプチャ中かどうかを返す。
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isRunning
}

// Snapshot は現在の状態のコピーを返す（/api/status 用）。
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return State{
		IsRunning:   m.isRunning,
		CurrentPage: m.currentPage,
		TotalPages:  m.totalPages,
		Status:      m.status,
		Message:     m.message,
		OutputDir:   m.outputDir,
		Error:       m.err,
	}
}

// Start はバックグラウンド goroutine でキャプチャ処理を開始する。
// 既に実行中なら ErrAlreadyRunning を返し、状態は一切変更しない。
func (m *Manager) Start(bookName string, maxPages, pdfPagesPerFile int, autoDeletePNG bool, direction capture.Direction) error {
	m.mu.Lock()
	if m.isRunning {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.isRunning = true
	m.shouldStop = false
	m.currentPage = 0
	m.totalPages = 0
	m.status = StatusRunning
	m.message = "準備中..."
	m.err = ""
	m.outputDir = ""
	m.bookName = bookName
	m.mu.Unlock()

	go m.runWorker(bookName, maxPages, pdfPagesPerFile, autoDeletePNG, direction)
	return nil
}

// Stop は実行中のキャプチャ処理に停止要求を送る（非同期。実際の停止は次のループ判定時）。
// 実行中でなければ ErrNotRunning を返す。
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isRunning {
		return ErrNotRunning
	}
	m.shouldStop = true
	return nil
}

// --- 以下、runWorker（バックグラウンドgoroutine）内部から呼ぶヘルパー ---

func (m *Manager) shouldStopNow() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shouldStop
}

func (m *Manager) setMessage(msg string) {
	m.mu.Lock()
	m.message = msg
	m.mu.Unlock()
}

// setErrorState はPython版の「except Exception」に相当する共通のエラー終了処理。
func (m *Manager) setErrorState(errMsg string) {
	m.mu.Lock()
	m.status = StatusError
	m.err = errMsg
	m.message = "エラー: " + errMsg
	m.mu.Unlock()
}

// runWorker はキャプチャ〜PDF結合の一連の処理を行う。Start からgoroutineとして起動される。
func (m *Manager) runWorker(bookName string, maxPages, pdfPagesPerFile int, autoDeletePNG bool, direction capture.Direction) {
	defer func() {
		m.mu.Lock()
		m.isRunning = false
		m.mu.Unlock()
	}()

	// 出力ディレクトリ作成（同名フォルダに既存ファイルがあれば連番で別フォルダ）
	outDir, err := output.CreateUniqueDir(m.outRoot, bookName)
	if err != nil {
		m.setErrorState(err.Error())
		return
	}
	m.mu.Lock()
	m.outputDir = outDir
	m.mu.Unlock()

	// Kindle を自動で前面に出す（失敗しても致命的ではない。手動切り替えを促すだけ）
	kindle := &capture.Kindle{}
	activated := kindle.Activate() == nil

	// 開始待機（1秒ごとにカウントダウンを表示し、停止要求にも即応する）
	for remaining := int(DefaultStartDelay.Seconds()); remaining > 0; remaining-- {
		if m.shouldStopNow() {
			break
		}
		if activated {
			m.setMessage(fmt.Sprintf("Kindleを前面に表示しました。%d秒後に開始します（フルスクリーン推奨）...", remaining))
		} else {
			m.setMessage(fmt.Sprintf("⚠️ Kindleを自動で前面に出せませんでした。%d秒以内に手動で切り替えてください...", remaining))
		}
		time.Sleep(time.Second)
	}

	if m.shouldStopNow() {
		m.mu.Lock()
		m.status = StatusIdle
		m.message = "中断されました"
		m.mu.Unlock()
		return
	}

	// スクリーンショット取得
	m.setMessage("スクリーンショット取得中...")
	captured, cancelMsg, cancelled, err := m.captureLoop(kindle, outDir, maxPages, direction)
	if cancelled {
		// 右上ホットコーナーによるキャンセル：PDF化せず終了
		m.mu.Lock()
		m.status = StatusIdle
		m.message = "🖱️ 右上ホットコーナーでキャンセル: " + cancelMsg
		m.mu.Unlock()
		return
	}
	if err != nil {
		m.setErrorState(err.Error())
		return
	}

	m.mu.Lock()
	m.totalPages = captured
	stopped := m.shouldStop
	m.mu.Unlock()

	if stopped {
		m.mu.Lock()
		m.status = StatusIdle
		m.message = fmt.Sprintf("中断されました（%dページまで保存）", captured)
		m.mu.Unlock()
		return
	}

	if captured == 0 {
		m.mu.Lock()
		m.status = StatusError
		m.err = "スクリーンショットが取得できませんでした"
		m.mu.Unlock()
		return
	}

	// PDF結合
	m.setMessage("PDF結合中...")
	createdPDFs, err := pdf.CreateFromDir(outDir, bookName, pdfPagesPerFile)
	if err != nil {
		m.setErrorState(err.Error())
		return
	}
	if len(createdPDFs) == 0 {
		m.mu.Lock()
		m.status = StatusError
		m.err = "PDF作成に失敗しました"
		m.mu.Unlock()
		return
	}

	// PNG削除
	if autoDeletePNG {
		m.setMessage("PNGファイル削除中...")
		deletedCount, _ := pdf.DeletePNGs(outDir)
		m.setMessage(fmt.Sprintf("完了！%d個のPDFを作成し、%d個のPNGを削除しました", len(createdPDFs), deletedCount))
	} else {
		m.setMessage(fmt.Sprintf("完了！%d個のPDFを作成しました", len(createdPDFs)))
	}

	m.mu.Lock()
	m.status = StatusDone
	m.mu.Unlock()
}
