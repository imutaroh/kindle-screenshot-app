// Package server は Web UI 版の HTTP サーバーを提供する。
// Python 版 src/server.py（Flask）の net/http 移植で、フレームワークは使わない。
package server

import (
	"io/fs"
	"net/http"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/session"
	"github.com/imutaakihiro/kindle-screenshot-go/web"
)

// Server は Web UI のルーティングと状態（session.Manager / 出力先ルート）を束ねる。
type Server struct {
	mgr     *session.Manager
	outRoot string
}

// New はキャプチャ処理を管理する mgr と、本の出力先ルートフォルダ outRoot を紐付けた Server を作る。
func New(mgr *session.Manager, outRoot string) *Server {
	return &Server{mgr: mgr, outRoot: outRoot}
}

// Handler はこのサーバーの全ルートを登録した http.Handler を返す。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /reader", s.handleReader)

	staticFS, err := fs.Sub(web.FS, "static")
	if err != nil {
		// web.FS は go:embed で静的に埋め込まれているため、ここに到達するのはビルド構成の誤りのみ。
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("POST /api/start", s.handleStart)
	mux.HandleFunc("POST /api/stop", s.handleStop)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/open-folder", s.handleOpenFolder)
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/books", s.handleBooks)

	mux.HandleFunc("GET /pdfs/{book}/{filename}", s.handlePDF)

	return mux
}
