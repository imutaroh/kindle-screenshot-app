package server

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/capture"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/session"
	"github.com/imutaakihiro/kindle-screenshot-go/web"
)

// writeHTMLFile は web.FS に埋め込まれた HTML ファイルをそのまま返す。
// Python 版は Jinja2 テンプレートだが、テンプレート変数を使っていないためエンジン不要。
func writeHTMLFile(w http.ResponseWriter, path string) {
	data, err := web.FS.ReadFile(path)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	writeHTMLFile(w, "templates/index.html")
}

func (s *Server) handleReader(w http.ResponseWriter, r *http.Request) {
	writeHTMLFile(w, "templates/reader.html")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type startRequest struct {
	BookName        *string `json:"book_name"`
	MaxPages        *int    `json:"max_pages"`
	PDFPagesPerFile *int    `json:"pdf_pages_per_file"`
	AutoDeletePNG   *bool   `json:"auto_delete_png"`
	Direction       *string `json:"direction"`
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if s.mgr.IsRunning() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "既に実行中です"})
		return
	}

	var req startRequest
	// リクエストボディが空/不正でも落ちないよう、デコードエラーは無視して既定値のまま進める
	// （Python 版 request.json も `data.get(...)` で欠損キーを許容する挙動に合わせる）。
	json.NewDecoder(r.Body).Decode(&req)

	bookName := ""
	if req.BookName != nil {
		bookName = strings.TrimSpace(*req.BookName)
	}
	if bookName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "本の名前を入力してください"})
		return
	}

	maxPages := 0
	if req.MaxPages != nil {
		maxPages = *req.MaxPages
	}

	pdfPagesPerFile := session.DefaultPDFPagesPerFile
	if req.PDFPagesPerFile != nil {
		pdfPagesPerFile = *req.PDFPagesPerFile
	}

	autoDeletePNG := false
	if req.AutoDeletePNG != nil {
		autoDeletePNG = *req.AutoDeletePNG
	}

	direction := session.DefaultDirection
	if req.Direction != nil && (*req.Direction == string(capture.Left) || *req.Direction == string(capture.Right)) {
		direction = capture.Direction(*req.Direction)
	}

	if err := s.mgr.Start(bookName, maxPages, pdfPagesPerFile, autoDeletePNG, direction); err != nil {
		if err == session.ErrAlreadyRunning {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "既に実行中です"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "処理を開始しました"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Stop(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "実行中ではありません"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "停止要求を送信しました"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.mgr.Snapshot()

	var errVal, outDirVal any
	if st.Error != "" {
		errVal = st.Error
	}
	if st.OutputDir != "" {
		outDirVal = st.OutputDir
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"is_running":   st.IsRunning,
		"current_page": st.CurrentPage,
		"total_pages":  st.TotalPages,
		"status":       st.Status,
		"message":      st.Message,
		"error":        errVal,
		"output_dir":   outDirVal,
	})
}

func (s *Server) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	outDir := s.mgr.Snapshot().OutputDir
	if outDir != "" {
		if _, err := os.Stat(outDir); err == nil {
			exec.Command("open", outDir).Run() // エラーは無視（Python版も check=False）
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return
		}
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "出力フォルダがまだ作成されていません"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"page_wait_time":     session.DefaultPageWaitTime.Seconds(),
		"start_delay":        int(session.DefaultStartDelay.Seconds()),
		"pdf_pages_per_file": session.DefaultPDFPagesPerFile,
		"max_pages":          session.DefaultMaxPages,
		"page_direction":     string(session.DefaultDirection),
	})
}
