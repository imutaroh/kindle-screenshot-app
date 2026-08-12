// Package web は Web UI の静的資産（HTML/CSS/JS）をビルド時にバイナリへ同梱する。
// Python 版の templates/ と static/ をほぼそのまま移植したもの。
package web

import "embed"

//go:embed static templates
var FS embed.FS
