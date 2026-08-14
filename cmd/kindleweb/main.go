// Command kindleweb は PageSnap の Web UI 版サーバー。
// Python 版 server.py（Flask）の net/http 移植。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/imutaroh/kindle-screenshot-app/internal/server"
	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

func defaultPort() int {
	// macOS の AirPlay Receiver が 5000 を使うことがあるため、デフォルトは 5001。
	// 環境変数 PORT があればそれを既定値にし、-port フラグの明示指定があればそちらを優先する。
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return 5001
}

func main() {
	port := flag.Int("port", defaultPort(), "listen port")
	outRoot := flag.String("out", "output", "output root directory")
	flag.Parse()

	mgr := session.NewManager(*outRoot)
	srv := server.New(mgr, *outRoot)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	fmt.Println("==================================================")
	fmt.Println("📚 PageSnap - Web UI版 (Go)")
	fmt.Println("==================================================")
	fmt.Printf("\n🌐 ブラウザで以下のURLにアクセスしてください:\n")
	fmt.Printf("   http://localhost:%d\n", *port)
	fmt.Println("\n⚠️  Kindleをフルスクリーンで表示してから実行してください。")
	fmt.Println("==================================================")

	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
