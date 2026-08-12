package server

import (
	"fmt"
	"math"

	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

// pageData is the view-model for the full index page template ("page").
// The <main> content is now entirely delegated to the "status-panel"
// fragment (see statusAreaData below); pageData only carries what wraps it.
type pageData struct {
	Status statusAreaData
}

// statusAreaData is the view-model shared by the "status-panel" fragment
// (#statusArea) and the "status-badge" fragment that travels alongside it.
//
// OOB is true only when this is rendered as a standalone ajax fragment
// response (POST /ui/start, GET /ui/status, POST /ui/stop, GET /ui/reset). In
// that case the badge is marked hx-swap-oob="true" so it updates in place
// inside <header> even though the primary hx-swap target is #statusArea. On
// the full page render (GET /) OOB is false: the badge is emitted once, in
// its normal <header> position, and "status-panel" skips emitting a second
// copy.
//
// "status-panel" now renders the entire <main> content and branches on
// exactly one of ShowForm / IsRunning / ShowResult (mutually exclusive,
// exhaustive) so that only the elements needed for the current state exist
// in the DOM. This is what lets the running state's 1s polling never risk
// touching form input values: the form simply isn't present while running.
type statusAreaData struct {
	OOB bool

	IsRunning  bool
	BadgeLabel string
	BadgeClass string

	// ShowForm: 待機中（実行中でなく結果もない）、またはバリデーションエラー時。
	ShowForm bool
	// ShowResult: 完了/エラー/中断など、実行済みの結果がある状態。
	ShowResult bool

	// BookName: 実行中・結果表示で「対象の本」を表示するための本の名前。
	// ShowForm 側では常に空（新しい本を撮る前提のため）。
	BookName string

	// フォームの初期値（ShowForm のときだけテンプレートで参照される）。
	MaxPages        int
	PDFPagesPerFile int
	Direction       string
	AutoDeletePNG   bool

	Indeterminate   bool
	ProgressPct     int
	ProgressPctText string
	ProgressText    string

	Message   string
	OutputDir string

	ShowStartButton bool
	ShowStopButton  bool
	ShowResetButton bool

	// ValidationError is set only for the 422 response to POST /ui/start
	// when the book name is empty. It is shown inline next to the book name
	// field within the (still-rendered, since ShowForm is forced true) form.
	ValidationError string

	Logs []logEntryView
}

type logEntryView struct {
	Time  string
	Level string
	Text  string
}

// buildStatusArea reads the current Manager snapshot + log history and
// produces the view-model for the "status-panel"/"status-badge" templates.
//
// form, when non-nil, overrides the form default fields (used only for the
// POST /ui/start validation-error path, so the values the user just typed
// are echoed back instead of the Manager's persisted defaults). When nil,
// the form fields are derived from the Manager's persisted settings.
func buildStatusArea(mgr *session.Manager, oob bool, validationError string, form *formValues) statusAreaData {
	st := mgr.Snapshot()

	sa := statusAreaData{
		OOB:             oob,
		IsRunning:       st.IsRunning,
		Message:         st.Message,
		OutputDir:       st.OutputDir,
		BookName:        st.BookName,
		ValidationError: validationError,
	}

	switch {
	case st.IsRunning:
		sa.BadgeLabel, sa.BadgeClass = "実行中", "running"
	case st.Status == session.StatusDone:
		sa.BadgeLabel, sa.BadgeClass = "完了", "completed"
	case st.Status == session.StatusError:
		sa.BadgeLabel, sa.BadgeClass = "エラー", "error"
	default:
		sa.BadgeLabel, sa.BadgeClass = "待機中", ""
	}

	// 3状態は排他的かつ網羅的: 実行中 → 結果あり（完了/エラー/中断） → それ以外はフォーム。
	// 中断（停止ボタン・右上ホットコーナー）は runWorker が status を StatusIdle に戻すため、
	// StatusDone/StatusError では判定できない。OutputDir と Message の両方が
	// 埋まっていることを「一度でも実行された」印として使う。
	sa.ShowResult = validationError == "" && !st.IsRunning && st.OutputDir != "" && st.Message != ""
	sa.ShowForm = validationError != "" || (!st.IsRunning && !sa.ShowResult)

	if sa.ShowForm {
		sa.BookName = ""
		if form != nil {
			sa.MaxPages = form.MaxPages
			sa.PDFPagesPerFile = form.PDFPagesPerFile
			sa.Direction = form.Direction
			sa.AutoDeletePNG = form.AutoDeletePNG
		} else {
			sa.MaxPages = session.DefaultMaxPages
			sa.PDFPagesPerFile = st.PDFPagesPerFile
			sa.Direction = st.Direction
			sa.AutoDeletePNG = st.AutoDeletePNG
		}
	}

	switch {
	case st.TotalPages > 0:
		pct := int(math.Round(float64(st.CurrentPage) / float64(st.TotalPages) * 100))
		if pct > 100 {
			pct = 100
		}
		sa.ProgressPct = pct
		sa.ProgressPctText = fmt.Sprintf("%d%%", pct)
		sa.ProgressText = fmt.Sprintf("%d / %d ページ", st.CurrentPage, st.TotalPages)
	case st.IsRunning:
		// 総ページ数は最後まで分からないため、実行中は流れるバーで動作中を示す。
		sa.Indeterminate = true
		sa.ProgressPctText = "—"
		if st.CurrentPage > 0 {
			sa.ProgressText = fmt.Sprintf("%d ページ取得済み", st.CurrentPage)
		} else {
			sa.ProgressText = "準備中..."
		}
	default:
		sa.ProgressPctText = "—"
		sa.ProgressText = fmt.Sprintf("%d ページ", st.CurrentPage)
	}

	sa.ShowStartButton = sa.ShowForm
	sa.ShowStopButton = st.IsRunning
	sa.ShowResetButton = sa.ShowResult

	if sa.IsRunning || sa.ShowResult {
		for _, l := range mgr.Logs() {
			sa.Logs = append(sa.Logs, logEntryView{
				Time:  l.Time.Format("15:04:05"),
				Level: l.Level,
				Text:  l.Text,
			})
		}
	}

	return sa
}

// formValues holds the raw form field values submitted to POST /ui/start,
// used by buildStatusArea to echo them back on a validation error (422)
// instead of falling back to the Manager's persisted defaults.
type formValues struct {
	MaxPages        int
	PDFPagesPerFile int
	Direction       string
	AutoDeletePNG   bool
}
