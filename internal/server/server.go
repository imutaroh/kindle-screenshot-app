// Package server は Web UI 版の HTTP サーバーを提供する。
// Python 版 src/server.py（Flask）の net/http 移植で、フレームワークは使わない。
//
// 主画面（キャプチャ設定・進捗表示）は html/template + htmx によるサーバーサイド
// レンダリング。読書ビューア（/reader, reader.js）はテンプレートエンジンを介さず
// 埋め込みHTMLをそのまま返す、従来通りの実装のまま変更していない。
package server

import (
	"html/template"
	"io/fs"
	"net/http"

	"github.com/imutaroh/kindle-screenshot-app/internal/session"
	"github.com/imutaroh/kindle-screenshot-app/web"
)

// Server は Web UI のルーティングと状態（session.Manager / 出力先ルート / テンプレート）を束ねる。
type Server struct {
	mgr     *session.Manager
	outRoot string
	tmpl    *template.Template
}

// New はキャプチャ処理を管理する mgr と、本の出力先ルートフォルダ outRoot を紐付けた Server を作る。
// index.html のテンプレートはここで一度だけパースし、以降のリクエストで使い回す。
func New(mgr *session.Manager, outRoot string) *Server {
	tmpl := template.Must(template.ParseFS(web.FS, "templates/index.html"))
	return &Server{mgr: mgr, outRoot: outRoot, tmpl: tmpl}
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

	mux.HandleFunc("POST /ui/start", s.handleUIStart)
	mux.HandleFunc("GET /ui/status", s.handleUIStatus)
	mux.HandleFunc("POST /ui/stop", s.handleUIStop)
	mux.HandleFunc("POST /ui/open-folder", s.handleUIOpenFolder)

	mux.HandleFunc("GET /api/books", s.handleBooks)
	mux.HandleFunc("GET /pdfs/{book}/{filename}", s.handlePDF)

	return mux
}
