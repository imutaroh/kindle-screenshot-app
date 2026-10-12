package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// statusOutput は kindlesnap status の出力。
// status は <bookDir>/status.json の中身そのまま（まだ書かれていなければ null）。
type statusOutput struct {
	Running bool            `json:"running"`
	PID     int             `json:"pid,omitempty"`
	BookDir string          `json:"book_dir,omitempty"`
	Status  json.RawMessage `json:"status"`
}

// cmdStatus は kindlesnap status。実行中の capture があれば、その本のフォルダの
// status.json を読んで返す。実行中でなければ {"running":false,"status":null}。
// 終わった取り込みの結果は <bookDir>/status.json（result 付き）を直接読むこと。
func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status", stderr)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	info, running, err := runningCapture()
	if err != nil {
		return fail(stdout, err)
	}
	out := statusOutput{Running: running, Status: json.RawMessage("null")}
	if running {
		out.PID, out.BookDir = info.PID, info.BookDir
		if info.BookDir != "" {
			data, err := os.ReadFile(filepath.Join(info.BookDir, "status.json"))
			switch {
			case err == nil && json.Valid(data):
				out.Status = json.RawMessage(data)
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return fail(stdout, fmt.Errorf("status.json を読めません: %w", err))
			}
		}
	}
	writeJSON(stdout, out)
	return exitOK
}

// signalOutput は kindlesnap finish / stop の出力。
type signalOutput struct {
	OK      bool   `json:"ok"`
	PID     int    `json:"pid"`
	Signal  string `json:"signal"`
	BookDir string `json:"book_dir"`
}

// cmdSignal は kindlesnap finish / stop。実行中の capture のプロセスにシグナルを送る。
// 送るだけで、相手が終わるのは待たない（PDF・Markdown の作成に時間がかかるため）。
// 終わったかは kindlesnap status か <bookDir>/status.json の result で確かめる。
func cmdSignal(name string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet(name, stderr)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	sig, sigName := syscall.SIGUSR1, "SIGUSR1" // finish: そこまでのページで完了
	if name == "stop" {
		sig, sigName = syscall.SIGINT, "SIGINT" // stop: 中断（撮れたページの PDF は作る）
	}

	info, running, err := runningCapture()
	if err != nil {
		return fail(stdout, err)
	}
	if !running {
		return fail(stdout, errors.New("実行中の kindlesnap capture がありません"))
	}
	if err := syscall.Kill(info.PID, sig); err != nil {
		return fail(stdout, fmt.Errorf("pid %d に %s を送れませんでした: %w", info.PID, sigName, err))
	}
	writeJSON(stdout, signalOutput{OK: true, PID: info.PID, Signal: sigName, BookDir: info.BookDir})
	return exitOK
}
