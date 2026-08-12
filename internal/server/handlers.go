package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/session"
	"github.com/imutaroh/kindle-screenshot-app/web"
)

// writeJSON は GET /api/books（読書ビューアが使う唯一の残存JSON API）が使う。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeHTMLFile は web.FS に埋め込まれた HTML ファイルをそのまま返す。
// /reader は Jinja2/html-template のようなテンプレート変数を使わないため、
// このまま埋め込みファイルを素通しするだけで足りる（主画面のみ html/template 化）。
func writeHTMLFile(w http.ResponseWriter, path string) {
	data, err := web.FS.ReadFile(path)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleReader(w http.ResponseWriter, r *http.Request) {
	writeHTMLFile(w, "templates/reader.html")
}

// handleIndex は主画面をレンダリングする。設定フォームの初期値は
// session パッケージの Default* 定数から埋め込み、進捗/ログは現在の
// Manager の状態をそのまま反映する（リロードで実行中/完了/エラー状態が復元される）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		BookName:        "",
		MaxPages:        session.DefaultMaxPages,
		PDFPagesPerFile: session.DefaultPDFPagesPerFile,
		Direction:       string(session.DefaultDirection),
		AutoDeletePNG:   session.DefaultAutoDeletePNG,
		Status:          buildStatusArea(s.mgr, false, ""),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

// renderStatusPanel は #statusArea 断片（+ hx-swap-oob 付きのステータスバッジ）を
// status に応じたHTTPステータスで書き出す。POST /ui/start・GET /ui/status・
// POST /ui/stop はすべてこのヘルパー経由でレスポンスを返す。
func (s *Server) renderStatusPanel(w http.ResponseWriter, status int, sa statusAreaData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.tmpl.ExecuteTemplate(w, "status-panel", sa)
}

// handleUIStart はキャプチャ処理を開始する。本の名前が空の場合は 422 を返し、
// #statusArea 断片にエラーメッセージを含め、加えて実フォーム内の本名入力欄を
// hx-swap-oob で .input-error 付きに差し替える。
func (s *Server) handleUIStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	bookName := strings.TrimSpace(r.FormValue("book_name"))
	if bookName == "" {
		sa := buildStatusArea(s.mgr, true, "本の名前を入力してください")
		s.renderStatusPanel(w, http.StatusUnprocessableEntity, sa)
		s.tmpl.ExecuteTemplate(w, "bookname-error-oob", nil)
		return
	}

	maxPages := session.DefaultMaxPages
	if v, err := strconv.Atoi(r.FormValue("max_pages")); err == nil {
		maxPages = v
	}

	pdfPagesPerFile := session.DefaultPDFPagesPerFile
	if v, err := strconv.Atoi(r.FormValue("pdf_pages_per_file")); err == nil {
		pdfPagesPerFile = v
	}

	autoDeletePNG := r.FormValue("auto_delete_png") != ""

	direction := session.DefaultDirection
	if v := r.FormValue("direction"); v == string(capture.Left) || v == string(capture.Right) {
		direction = capture.Direction(v)
	}

	// ErrAlreadyRunning はここでは無視する。既に実行中断片をそのまま返せば、
	// htmx が #statusArea を現状の実行中表示に置き換えるだけで実害がない。
	_ = s.mgr.Start(bookName, maxPages, pdfPagesPerFile, autoDeletePNG, direction)

	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, ""))
}

// handleUIStatus は現在の状態の #statusArea 断片を返す。実行中の画面が
// hx-trigger="every 1s" でポーリングする先。
func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, ""))
}

// handleUIStop は実行中のキャプチャ処理に停止要求を送る。未実行時のエラーは
// 無視し（既に止まっている状態の断片をそのまま返せば十分）、現在の断片を返す。
func (s *Server) handleUIStop(w http.ResponseWriter, r *http.Request) {
	_ = s.mgr.Stop()
	s.renderStatusPanel(w, http.StatusOK, buildStatusArea(s.mgr, true, ""))
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
