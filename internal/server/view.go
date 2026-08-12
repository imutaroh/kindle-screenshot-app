package server

import (
	"fmt"
	"math"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/session"
)

// pageData is the view-model for the full index page template ("page").
type pageData struct {
	BookName        string
	MaxPages        int
	PDFPagesPerFile int
	Direction       string
	AutoDeletePNG   bool
	Status          statusAreaData
}

// statusAreaData is the view-model shared by the "status-panel" fragment
// (#statusArea) and the "status-badge" fragment that travels alongside it.
//
// OOB is true only when this is rendered as a standalone ajax fragment
// response (POST /ui/start, GET /ui/status, POST /ui/stop). In that case the
// badge is marked hx-swap-oob="true" so it updates in place inside <header>
// even though the primary hx-swap target is #statusArea. On the full page
// render (GET /) OOB is false: the badge is emitted once, in its normal
// <header> position, and "status-panel" skips emitting a second copy.
type statusAreaData struct {
	OOB bool

	IsRunning  bool
	BadgeLabel string
	BadgeClass string

	ShowProgress    bool
	Indeterminate   bool
	ProgressPct     int
	ProgressPctText string
	ProgressText    string

	Message string

	ShowStartButton      bool
	ShowStopButton       bool
	ShowOpenFolderButton bool

	// ValidationError is set only for the 422 response to POST /ui/start
	// when the book name is empty. It is shown inside the same #statusArea
	// fragment (message box), per spec.
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
func buildStatusArea(mgr *session.Manager, oob bool, validationError string) statusAreaData {
	st := mgr.Snapshot()

	sa := statusAreaData{
		OOB:             oob,
		IsRunning:       st.IsRunning,
		Message:         st.Message,
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

	// 進捗カード（+ログ）は、一度でも実行された（実行中/完了/エラー）か、
	// バリデーションエラーを表示する必要がある間だけ表示する。
	// Python版時代からの挙動（実行を開始したら最後まで表示され続ける）を維持する。
	sa.ShowProgress = validationError != "" || st.IsRunning || st.Status == session.StatusDone || st.Status == session.StatusError

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

	sa.ShowStartButton = !st.IsRunning
	sa.ShowStopButton = st.IsRunning
	sa.ShowOpenFolderButton = !st.IsRunning && st.OutputDir != ""

	for _, l := range mgr.Logs() {
		sa.Logs = append(sa.Logs, logEntryView{
			Time:  l.Time.Format("15:04:05"),
			Level: l.Level,
			Text:  l.Text,
		})
	}

	return sa
}
