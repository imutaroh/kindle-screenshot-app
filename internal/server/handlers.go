package server

import (
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

// handleIndex は主画面をレンダリングする。<main> の中身は丸ごと
// "status-panel" フラグメントに委譲し、Manager の現在の状態（待機中/実行中/
// 結果あり）をそのまま反映する（リロードで実行中/完了/エラー状態が復元される）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Status: buildStatusArea(s.mgr, false, "", nil),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

// renderStatusPanel は #statusArea 断片（+ hx-swap-oob 付きのステータスバッジ）を
// status に応じたHTTPステータスで書き出す。POST /ui/start・GET /ui/status・
// POST /ui/stop・GET /ui/reset はすべてこのヘルパー経由でレスポンスを返す。
func (s *Server) renderStatusPanel(w http.ResponseWriter, status int, sa statusAreaData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.tmpl.ExecuteTemplate(w, "status-panel", sa)
}

// parseStartForm は POST /ui/start のフォーム値を解釈する（本の名前以外）。max_pages /
// pdf_pages_per_file が数値でない場合や direction が left/right 以外の場合は
// 既定値にフォールバックする（Python版時代からブラウザ標準バリデーションに
// 頼らずサーバ側で寛容に扱う挙動を踏襲）。チェックボックスは未チェックだと
// 送信されないので、値の有無だけで ON/OFF を判定する。
func parseStartForm(r *http.Request) formValues {
	f := formValues{
		MaxPages:        session.DefaultMaxPages,
		PDFPagesPerFile: session.DefaultPDFPagesPerFile,
		Direction:       string(session.DefaultDirection),
		AutoDeletePNG:   r.FormValue("auto_delete_png") != "",
		AutoCrop:        r.FormValue("auto_crop") != "",
	}
	if v, err := strconv.Atoi(r.FormValue("max_pages")); err == nil {
		f.MaxPages = v
	}
	if v, err := strconv.Atoi(r.FormValue("pdf_pages_per_file")); err == nil {
		f.PDFPagesPerFile = v
	}
	if v := r.FormValue("direction"); v == string(capture.Left) || v == string(capture.Right) {
		f.Direction = v
	}
	return f
}

// handleUIStart はキャプチャ処理を開始する。本の名前が空の場合は 422 を返し、
// status-panel（フォームを含む）をそのまま再描画する。フォームは
// #statusArea の一部として丸ごとスワップされるため、入力し直した他の項目の
// 値もそのまま埋め戻して返す（消えない）。
func (s *Server) handleUIStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	form := parseStartForm(r)

	bookName := strings.TrimSpace(r.FormValue("book_name"))
	if bookName == "" {
		sa := buildStatusArea(s.mgr, true, "本の名前を入力してください", &form)
		s.renderStatusPanel(w, http.StatusUnprocessableEntity, sa)
		return
	}

	// ErrAlreadyRunning はここでは無視する。既に実行中断片をそのまま返せば、
	// htmx が #statusArea を現状の実行中表示に置き換えるだけで実害がない。
	_ = s.mgr.Start(session.Settings{
		BookName:        bookName,
		MaxPages:        form.MaxPages,
		PDFPagesPerFile: form.PDFPagesPerFile,
		AutoDeletePNG:   form.AutoDeletePNG,
		AutoCrop:        form.AutoCrop,
		Direction:       capture.Direction(form.Direction),
	})

	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, "", nil))
}

// handleUIStatus は現在の状態の #statusArea 断片を返す。実行中の画面が
// hx-trigger="every 1s" でポーリングする先。
func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, "", nil))
}

// handleUIStop は実行中のキャプチャ処理に停止要求を送る。未実行時のエラーは
// 無視し（既に止まっている状態の断片をそのまま返せば十分）、現在の断片を返す。
func (s *Server) handleUIStop(w http.ResponseWriter, r *http.Request) {
	_ = s.mgr.Stop()
	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, "", nil))
}

// handleUIFinish は「終了してPDF作成」ボタン用（主に一時停止中に表示される）。
// 撮影を止めてそこまでのページで PDF を作る要求を送る。handleUIStop と同じく
// 未実行時のエラーは無視し、現在の断片を返す。
func (s *Server) handleUIFinish(w http.ResponseWriter, r *http.Request) {
	_ = s.mgr.Finish()
	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, "", nil))
}

// handleUIReset は「新しい本を撮る」ボタン用。Manager の状態（完了/エラー/
// 中断の結果）は一切変更せず、表示だけを待機中のフォーム状態に戻す。
// PDF結合枚数・めくり方向・PNG自動削除・余白トリミングは前回の入力値を引き継ぎ、本の名前と
// 最大ページ数は新しい本のキャプチャに向けて初期値に戻す。
func (s *Server) handleUIReset(w http.ResponseWriter, r *http.Request) {
	st := s.mgr.Snapshot()
	form := &formValues{
		MaxPages:        session.DefaultMaxPages,
		PDFPagesPerFile: st.PDFPagesPerFile,
		Direction:       st.Direction,
		AutoDeletePNG:   st.AutoDeletePNG,
		AutoCrop:        st.AutoCrop,
	}
	sa := statusAreaData{
		OOB:             true,
		BadgeLabel:      "待機中",
		BadgeClass:      "",
		ShowForm:        true,
		ShowStartButton: true,
		MaxPages:        form.MaxPages,
		PDFPagesPerFile: form.PDFPagesPerFile,
		Direction:       form.Direction,
		AutoDeletePNG:   form.AutoDeletePNG,
		AutoCrop:        form.AutoCrop,
	}
	s.renderStatusPanel(w, http.StatusOK, sa)
}

// handleUIOpenFolder は出力フォルダを Finder で開く。hx-swap="none" 前提のため
// レスポンスボディは持たず、常に 204 を返す。
func (s *Server) handleUIOpenFolder(w http.ResponseWriter, r *http.Request) {
	outDir := s.mgr.Snapshot().OutputDir
	if outDir != "" {
		if _, err := os.Stat(outDir); err == nil {
			exec.Command("open", outDir).Run() // エラーは無視（Python版も check=False）
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
