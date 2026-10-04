// Package session は Web UI 版のキャプチャワーカーを管理する。
// Python 版 src/server.py の capture_worker + CaptureState の Go 移植。
//
// Manager はミューテックスで保護された状態を持ち、Start でバックグラウンド
// goroutine を起動してキャプチャ〜PDF結合までを進める。GET /ui/status はこの
// 状態をポーリングで読み取るだけの薄いハンドラになる（internal/server が担当）。
package session

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/output"
	"github.com/imutaroh/kindle-screenshot-app/internal/pdf"
)

// ステータス文字列。Python 版 src/server.py の CaptureState.status と一字一句同じ日本語文字列。
// internal/server のテンプレートはバッジ表示にこの値をそのまま使う。
const (
	StatusIdle    = "待機中"
	StatusRunning = "実行中"
	StatusDone    = "完了"
	StatusError   = "エラー"

	// StatusPaused はページ送りが効かなくなって撮影を一時停止している状態（Go 版で追加）。
	// IsRunning は true のまま（ワーカーは生きていて、約5秒ごとにキーを送って復旧を待つ）。
	StatusPaused = "一時停止"
)

// デフォルト設定値。Python 版 src/config.py と同じ値。GET / の設定フォーム初期値はこれらをそのまま使う。
const (
	DefaultStartDelay          = 5 * time.Second
	DefaultPDFPagesPerFile     = 50
	DefaultMaxPages            = 0
	DefaultPageWaitTime        = 500 * time.Millisecond
	DefaultInitialPagesCount   = 5
	DefaultInitialPageWaitTime = 2 * time.Second
	DefaultNormalPageWaitTime  = 500 * time.Millisecond

	// 以下3つは適応待機（画面の変化を検知してから撮る）のパラメータ。
	// ページ送り直後、スクショが「前ページと変わった（frame.ChangedFrac 超）」かつ
	// 「直前のポーリングと同じ＝描画が止まって安定した（frame.StableFrac 以下）」と
	// 判定できるまで DefaultPollInterval 間隔でポーリングする（internal/session/worker.go の
	// captureSettled）。設定はUIに出さない固定パラメータ。
	DefaultPollInterval = 150 * time.Millisecond
	// DefaultSettleTimeout は通常ページの安定待ちタイムアウト。変化したのに安定しないまま
	// これに達したら1回だけ同じ長さ延長し、それでも安定しなければ最後の1枚を採用する。
	// 変化しないままこれに達したら「ページが進まない」として再送・一時停止の段階に入る。
	DefaultSettleTimeout = 3 * time.Second
	// DefaultInitialSettleTimeout は最初の DefaultInitialPagesCount ページの安定待ちタイムアウト
	// （Kindle の初期読み込みが遅いため長めに取る）。
	DefaultInitialSettleTimeout = 6 * time.Second

	// 以下4つは「ページが進まない」ときの段階的な対応のパラメータ（worker.go の recoverUnchanged）。
	// Kindle は起動直後やマウスが乗った後にバックグラウンドのキーを受け付けなくなることがあり
	// （本文を1回クリックすると復旧する）、最終ページと画面だけでは区別できない。そこで
	// 即終了せず、再送 → 一時停止（ユーザーの復旧待ち）の順に進める。
	//
	// DefaultResendCount は一時停止に入る前にキーを再送する回数。
	DefaultResendCount = 2
	// DefaultResendDelay は再送の前に空ける時間（Kindle の描画が遅れているだけの場合に、
	// 遅れて届いたページ送りと再送が重なって1ページ飛ばすのを避ける）。
	DefaultResendDelay = 2 * time.Second
	// DefaultPausedKeyInterval は一時停止中にキーを送る間隔。
	DefaultPausedKeyInterval = 5 * time.Second
	// DefaultPauseLimit は一時停止のまま待つ上限。超えたらそこまでのページで PDF を作って終了する。
	DefaultPauseLimit = 15 * time.Minute

	// DefaultCaptureFailWindow は撮影失敗を「恒常的」と判断するまでの連続失敗時間。
	// ページめくりアニメーション中は一時的に撮影できないことがある（実機で3連続失敗を観測）ため、
	// 1回の失敗では判断せず、これだけの時間かつ minCaptureFailures 回以上連続で失敗したらエラーにする。
	DefaultCaptureFailWindow = 5 * time.Second
	minCaptureFailures       = 3

	// DefaultAutoDeletePNG はPDF作成後にPNGを自動削除する設定の初期値。
	DefaultAutoDeletePNG = true

	// DefaultAutoCrop はPDF作成時に余白を自動トリミングする設定の初期値。
	DefaultAutoCrop = true

	// maxLogEntries はログ履歴のリングバッファ上限。
	maxLogEntries = 200
)

// DefaultDirection は既定のページめくり方向（日本語の本＝右綴じ）。
const DefaultDirection = capture.Left

// ErrAlreadyRunning は実行中に Start を呼んだ場合に返る。
var ErrAlreadyRunning = errors.New("既に実行中です")

// ErrNotRunning は未実行時に Stop / Finish を呼んだ場合に返る。
var ErrNotRunning = errors.New("実行中ではありません")

// Settings は Start に渡す取り込み設定（Web UI の設定フォームの値）。
type Settings struct {
	BookName        string
	MaxPages        int // 0 = 無制限
	PDFPagesPerFile int
	AutoDeletePNG   bool
	AutoCrop        bool // PDF作成時に余白を自動トリミングする（PNG自体は書き換えない）
	Direction       capture.Direction
}

// State は GET /ui/status が返す値のスナップショット（呼び出し時点のコピー）。
type State struct {
	IsRunning   bool
	CurrentPage int
	TotalPages  int
	Status      string
	Message     string
	OutputDir   string // 未設定なら空文字（Python版の None 相当。HTML化はserver側の責務）
	Error       string // 未設定なら空文字（Python版の None 相当）

	// BookName は現在実行中、または直近に実行した本の名前。
	// 実行中・結果表示（完了/エラー/中断）カードの「対象の本」表示に使う。
	BookName string

	// Resolution は1ページ目の撮影結果から求めた解像度の表示用文字列
	// （例: "1720x1410px（1x）"）。未取得なら空文字。
	Resolution string

	// 以下5つは直近の Start 呼び出しで使われた設定値（未実行なら初期値）。
	// 設定フォームの初期値の埋め戻し（GET / や GET /ui/reset）に使う。
	MaxPages        int
	PDFPagesPerFile int
	Direction       string
	AutoDeletePNG   bool
	AutoCrop        bool
}

// LogEntry はキャプチャ処理中にサーバー側で記録した1件のログ。
// 以前は web/static/script.js がステータスポーリングの差分からクライアント側で
// 生成していたログを、サーバー側で一元的に記録するようにしたもの。
type LogEntry struct {
	Time  time.Time
	Level string // "info" | "success" | "error"
	Text  string
}

// Manager はキャプチャ処理の状態とライフサイクルを管理する。ゼロ値では使えない。NewManager で作る。
type Manager struct {
	outRoot string

	mu           sync.Mutex
	isRunning    bool
	shouldStop   bool // 停止ボタン（中断。撮れたページがあれば PDF は作る）
	shouldFinish bool // 「終了してPDF作成」ボタン（そこまでのページで完了にする）
	currentPage  int
	totalPages   int
	status       string
	message      string
	outputDir    string
	bookName     string
	err          string
	resolution   string
	logs         []LogEntry

	// 直近の Start 呼び出しで使われた設定値。未実行時は下記デフォルト値のまま。
	// GET / や GET /ui/reset の設定フォーム初期値埋め戻しに使う（PDF結合枚数・
	// めくり方向・PNG自動削除は本をまたいで「前回の入力値」として引き継ぐ設計）。
	maxPages        int
	pdfPagesPerFile int
	direction       capture.Direction
	autoDeletePNG   bool
	autoCrop        bool

	// timing は待ち時間のパラメータ。NewManager で Default* の値が入る。
	// ユニットテストが短い値に差し替えるためだけに構造体にまとめている。
	timing timing

	// notify は一時停止時の macOS 通知。テストでは何もしない関数に差し替える。
	notify func(title, message string)
}

// timing は captureLoop が使う待ち時間の一式。
type timing struct {
	poll              time.Duration // DefaultPollInterval
	settle            time.Duration // DefaultSettleTimeout
	initialSettle     time.Duration // DefaultInitialSettleTimeout
	resendDelay       time.Duration // DefaultResendDelay
	pausedKeyInterval time.Duration // DefaultPausedKeyInterval
	pauseLimit        time.Duration // DefaultPauseLimit
	captureFailWindow time.Duration // DefaultCaptureFailWindow
}

func defaultTiming() timing {
	return timing{
		poll:              DefaultPollInterval,
		settle:            DefaultSettleTimeout,
		initialSettle:     DefaultInitialSettleTimeout,
		resendDelay:       DefaultResendDelay,
		pausedKeyInterval: DefaultPausedKeyInterval,
		pauseLimit:        DefaultPauseLimit,
		captureFailWindow: DefaultCaptureFailWindow,
	}
}

// NewManager は出力先ルートフォルダ outRoot を紐付けた Manager を作る。
func NewManager(outRoot string) *Manager {
	return &Manager{
		outRoot:         outRoot,
		status:          StatusIdle,
		maxPages:        DefaultMaxPages,
		pdfPagesPerFile: DefaultPDFPagesPerFile,
		direction:       DefaultDirection,
		autoDeletePNG:   DefaultAutoDeletePNG,
		autoCrop:        DefaultAutoCrop,
		timing:          defaultTiming(),
		notify: func(title, message string) {
			_ = capture.Notify(title, message) // 通知は補助なので失敗しても無視する
		},
	}
}

// IsRunning は現在キャプチャ中かどうかを返す。
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isRunning
}

// Snapshot は現在の状態のコピーを返す（GET /ui/status 用）。
func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return State{
		IsRunning:       m.isRunning,
		CurrentPage:     m.currentPage,
		TotalPages:      m.totalPages,
		Status:          m.status,
		Message:         m.message,
		OutputDir:       m.outputDir,
		Error:           m.err,
		BookName:        m.bookName,
		Resolution:      m.resolution,
		MaxPages:        m.maxPages,
		PDFPagesPerFile: m.pdfPagesPerFile,
		Direction:       string(m.direction),
		AutoDeletePNG:   m.autoDeletePNG,
		AutoCrop:        m.autoCrop,
	}
}

// addLog はログ履歴に1件追記する。上限 maxLogEntries を超えたら古いものから捨てる。
// Start() を跨いでも履歴はリセットされず、アプリ起動中は蓄積し続ける。
func (m *Manager) addLog(level, text string) {
	m.mu.Lock()
	m.logs = append(m.logs, LogEntry{Time: time.Now(), Level: level, Text: text})
	if len(m.logs) > maxLogEntries {
		m.logs = append([]LogEntry(nil), m.logs[len(m.logs)-maxLogEntries:]...)
	}
	m.mu.Unlock()
}

// Logs は記録済みログ履歴のコピーを古い順に返す。
func (m *Manager) Logs() []LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LogEntry, len(m.logs))
	copy(out, m.logs)
	return out
}

// SetRunningForTest は internal/server のハンドラテストが「実行中」状態のレンダリングを
// 検証するためだけに使うテスト専用フック。実際のキャプチャ処理は一切走らない。
// プロダクションコードから呼んではならない。
func (m *Manager) SetRunningForTest(running bool) {
	m.mu.Lock()
	m.isRunning = running
	if running {
		m.status = StatusRunning
	}
	m.mu.Unlock()
}

// SetStateForTest は internal/server のハンドラテストが任意の状態（一時停止・解像度表示など）の
// レンダリングを検証するためだけに使うテスト専用フック。st のうち IsRunning / CurrentPage /
// Status / Message / OutputDir / Error / BookName / Resolution だけを反映する。
// プロダクションコードから呼んではならない。
func (m *Manager) SetStateForTest(st State) {
	m.mu.Lock()
	m.isRunning = st.IsRunning
	m.currentPage = st.CurrentPage
	m.status = st.Status
	m.message = st.Message
	m.outputDir = st.OutputDir
	m.err = st.Error
	m.bookName = st.BookName
	m.resolution = st.Resolution
	m.mu.Unlock()
}

// Start はバックグラウンド goroutine でキャプチャ処理を開始する。
// 既に実行中なら ErrAlreadyRunning を返し、状態は一切変更しない。
func (m *Manager) Start(s Settings) error {
	m.mu.Lock()
	if m.isRunning {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}
	m.isRunning = true
	m.shouldStop = false
	m.shouldFinish = false
	m.currentPage = 0
	m.totalPages = 0
	m.status = StatusRunning
	m.message = "準備中..."
	m.err = ""
	m.outputDir = ""
	m.resolution = ""
	m.bookName = s.BookName
	m.maxPages = s.MaxPages
	m.pdfPagesPerFile = s.PDFPagesPerFile
	m.direction = s.Direction
	m.autoDeletePNG = s.AutoDeletePNG
	m.autoCrop = s.AutoCrop
	m.mu.Unlock()

	go m.runWorker(s)
	return nil
}

// Stop は実行中のキャプチャ処理に停止要求を送る（非同期。実際の停止は次のループ判定時）。
// 撮れたページが1枚以上あれば、中断でもそこまでの PDF は作る。
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

// Finish は「終了してPDF作成」の要求を送る（非同期）。撮影を止め、そこまでのページで
// PDF を作って完了にする。主に一時停止中（本の最後に着いてページが進まなくなった）に
// 使うが、実行中にも受け付ける。実行中でなければ ErrNotRunning を返す。
func (m *Manager) Finish() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isRunning {
		return ErrNotRunning
	}
	m.shouldFinish = true
	return nil
}

// --- 以下、runWorker（バックグラウンドgoroutine）内部から呼ぶヘルパー ---

// interrupted は停止または終了の要求が来ているかを返す。撮影ループはどちらでも
// すぐに抜け、どちらだったかは runCapture が見分ける。
func (m *Manager) interrupted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shouldStop || m.shouldFinish
}

// sleepInterruptible は d だけ待つ。途中で停止/終了の要求が来たらすぐに false を返す。
// 再送前の待機や一時停止中の待機（数秒）で、ボタンへの反応が遅れないようにするため。
func (m *Manager) sleepInterruptible(d time.Duration) bool {
	const step = 100 * time.Millisecond
	deadline := time.Now().Add(d)
	for {
		if m.interrupted() {
			return false
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true
		}
		time.Sleep(min(remaining, step))
	}
}

func (m *Manager) setStatus(status, msg string) {
	m.mu.Lock()
	m.status = status
	m.message = msg
	m.mu.Unlock()
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
	m.addLog("error", "❌ エラー: "+errMsg)
}

// runWorker はキャプチャ〜PDF結合の一連の処理を行う。Start からgoroutineとして起動される。
// 前半（権限確認・Kindle 検出・開始待機）は実機の Kindle に依存するためここに置き、
// 撮影以降は Kindle 操作を差し替え可能な runCapture に任せる（ユニットテストの対象）。
func (m *Manager) runWorker(s Settings) {
	defer func() {
		m.mu.Lock()
		m.isRunning = false
		m.mu.Unlock()
	}()

	m.addLog("info", fmt.Sprintf("「%s」のキャプチャを開始しました", s.BookName))
	m.addLog("info", "Kindle は前面に出しません。取り込み中もメインディスプレイでそのまま作業できます")

	// アクセシビリティ権限が無いと CGEventPostToPid のキーは黙って捨てられ、
	// 「ページが進まない（一時停止）」と区別がつかなくなるので、最初に確認する。
	if err := capture.CheckAccessibility(); err != nil {
		m.setErrorState(err.Error())
		return
	}

	// 出力ディレクトリ作成（同名フォルダに既存ファイルがあれば連番で別フォルダ）
	outDir, err := output.CreateUniqueDir(m.outRoot, s.BookName)
	if err != nil {
		m.setErrorState(err.Error())
		return
	}
	m.mu.Lock()
	m.outputDir = outDir
	m.mu.Unlock()

	// Kindle のウィンドウを自動検出する。前面化は一切しない（前面化はキーボード
	// フォーカスを奪い、「メインで作業しながら取り込む」という目的そのものを壊す）。
	kindle := &capture.Kindle{}
	kindle.SetOnRediscover(func(before, after string) {
		m.addLog("info", fmt.Sprintf("⚠️ Kindleウィンドウを再発見しました（本の開き直し等でIDが変化）: %s → %s", before, after))
	})
	discoverErr := kindle.Discover()
	if discoverErr == nil {
		m.addLog("info", "Kindle を検出しました: "+kindle.Describe())
	}

	// 開始待機（1秒ごとにカウントダウンを表示し、停止要求にも即応する）。
	// このカウントダウンは「検出結果を確認して止める猶予」。検出に失敗している間は
	// 毎秒 Discover を再試行し、最後まで失敗したらエラー終了する。
	for remaining := int(DefaultStartDelay.Seconds()); remaining > 0; remaining-- {
		if m.interrupted() {
			break
		}
		if discoverErr == nil {
			m.setMessage(fmt.Sprintf("%s を検出しました。%d秒後に開始します（そのままメインで作業できます）...", kindle.Describe(), remaining))
		} else {
			m.setMessage(fmt.Sprintf("⚠️ Kindleが見つかりません。%d秒以内に Kindle で本を開いてください...", remaining))
		}
		time.Sleep(time.Second)
		if discoverErr != nil {
			if discoverErr = kindle.Discover(); discoverErr == nil {
				m.addLog("info", "Kindle を検出しました: "+kindle.Describe())
			}
		}
	}

	if !m.interrupted() && discoverErr != nil {
		m.setErrorState(discoverErr.Error())
		return
	}

	if m.interrupted() {
		m.setStatus(StatusIdle, "中断されました")
		m.addLog("info", "中断されました")
		return
	}

	m.runCapture(kindle, outDir, s)
}

// runCapture は撮影ループを回し、終わり方（完了・終了要求・一時停止の時間切れ・中断・
// エラー）に応じて PDF 作成と状態の更新を行う。
//
// 撮れたページが1枚以上あれば、中断やエラーで終わった場合も PDF を作る
// （数百ページ撮った後の事故で成果物がゼロになるのを避ける）。
func (m *Manager) runCapture(drv kindleDriver, outDir string, s Settings) {
	m.setMessage("スクリーンショット取得中...")
	captured, end, loopErr := m.captureLoop(drv, outDir, s.MaxPages, s.Direction)

	m.mu.Lock()
	m.totalPages = captured
	stopped := m.shouldStop && !m.shouldFinish // 両方押されたら「終了してPDF作成」を優先する
	// 一時停止中に「終了してPDF作成」「停止」が押された、または一時停止が時間切れになった
	// 場合、captureLoop は status を StatusPaused のまま返す。このまま finalize（余白検出・
	// PDF結合。数百ページなら1分近くかかる）に入ると、その間 UI が「一時停止」バッジと
	// 「終了してPDF作成」ボタンを出し続け、要求が効いていないように見える。
	// 撮影は終わっているので、PDF 作成の間は実行中に戻す（エラーの経路は直後の
	// setErrorState が上書きする）。
	if m.status == StatusPaused {
		m.status = StatusRunning
	}
	m.mu.Unlock()

	switch {
	case loopErr != nil:
		m.setErrorState(loopErr.Error())
		if captured == 0 {
			return
		}
		summary, err := m.finalize(outDir, s)
		if err != nil {
			m.setMessage(fmt.Sprintf("エラー: %s（それまでの %d ページの PDF 作成にも失敗しました: %v）", loopErr, captured, err))
			m.addLog("error", "❌ PDF作成に失敗: "+err.Error())
			return
		}
		m.setMessage(fmt.Sprintf("エラー: %s（それまでの %d ページで%s）", loopErr, captured, summary))
		m.addLog("info", fmt.Sprintf("それまでの %d ページで%s: %s", captured, summary, outDir))

	case end == endInterrupted && stopped:
		if captured == 0 {
			m.setStatus(StatusIdle, "中断されました")
			m.addLog("info", "中断されました")
			return
		}
		summary, err := m.finalize(outDir, s)
		if err != nil {
			m.setErrorState(fmt.Sprintf("中断後のPDF作成に失敗しました（%dページまで保存）: %v", captured, err))
			return
		}
		msg := fmt.Sprintf("中断されました（%dページまで保存）。%s", captured, summary)
		m.setStatus(StatusIdle, msg)
		m.addLog("info", msg)

	default: // 最大ページ数に到達 / 「終了してPDF作成」 / 一時停止の時間切れ
		if captured == 0 {
			m.setErrorState("スクリーンショットが取得できませんでした")
			return
		}
		summary, err := m.finalize(outDir, s)
		if err != nil {
			m.setErrorState(err.Error())
			return
		}
		msg := "完了！" + summary
		if end == endPauseTimeout {
			msg = fmt.Sprintf("最終ページか確認できないまま終了しました（%d ページ）。%s", captured, summary)
		}
		m.setStatus(StatusDone, msg)
		m.addLog("success", "✅ 完了: "+outDir)
	}
}

// finalize は outDir の page_*.png から PDF を作り（設定に応じて余白をトリミングし）、
// 設定に応じて PNG を削除する。成功時は結果の要約（例: 「2個のPDFを作成しました」）を返す。
func (m *Manager) finalize(outDir string, s Settings) (string, error) {
	m.setMessage("PDF結合中...")
	opts := pdf.Options{Crop: s.AutoCrop}
	if s.AutoCrop {
		opts.Progress = func(done, total int) {
			if done%10 == 0 || done == total {
				m.setMessage(fmt.Sprintf("余白を検出中...（%d / %d ページ）", done, total))
			}
		}
	}
	res, err := pdf.CreateFromDir(outDir, s.BookName, s.PDFPagesPerFile, opts)
	if err != nil {
		return "", err
	}
	if len(res.Files) == 0 {
		return "", errors.New("PDF作成に失敗しました")
	}

	if s.AutoCrop {
		if res.Cropped {
			m.addLog("info", fmt.Sprintf("余白をトリミングしました: %dx%d → %dx%dpx",
				res.Bounds.Dx(), res.Bounds.Dy(), res.CropRect.Dx(), res.CropRect.Dy()))
			if res.CropNote != "" {
				m.addLog("info", res.CropNote)
			}
		} else if res.CropNote != "" {
			m.addLog("info", "⚠️ "+res.CropNote)
		}
	}

	summary := fmt.Sprintf("%d個のPDFを作成しました", len(res.Files))
	if s.AutoDeletePNG {
		m.setMessage("PNGファイル削除中...")
		deletedCount, _ := pdf.DeletePNGs(outDir)
		summary = fmt.Sprintf("%d個のPDFを作成し、%d個のPNGを削除しました", len(res.Files), deletedCount)
	}
	return summary, nil
}
