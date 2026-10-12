package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/ocr"
)

// minMacOSMajor は対応する macOS のメジャーバージョンの下限。
// OCR ヘルパー（Vision の縦書き日本語認識）が macOS 26 の API を前提にしている。
const minMacOSMajor = 26

// launchWait は doctor --launch で Kindle を起動してからウィンドウが現れるのを待つ上限。
const launchWait = 30 * time.Second

// check は doctor の1項目の結果。fix は失敗時にユーザー（Claude）が取るべき対処で、成功時は空。
type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// doctorOutput は doctor の出力。
type doctorOutput struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`
}

// doctorEnv は doctor が使う外部とのやりとり。ユニットテストで偽物に差し替えて、
// 実機の Kindle や権限に触れずに判定の組み立てだけを確かめるためにまとめている。
type doctorEnv struct {
	macOSVersion  func() (string, error)
	kindleRunning func() (bool, error)
	launch        func() error
	discover      func() (capture.WindowInfo, error)
	testCapture   func() error // Kindle のウィンドウを一時ファイルに撮って捨てる
	accessibility func() error
	ocr           func() error
	sleep         func(time.Duration)
	launchWait    time.Duration
}

// realDoctorEnv は本物の macOS・Kindle を相手にする doctorEnv。
func realDoctorEnv() doctorEnv {
	return doctorEnv{
		macOSVersion:  macOSVersion,
		kindleRunning: capture.IsKindleRunning,
		launch:        capture.Launch,
		discover: func() (capture.WindowInfo, error) {
			k := &capture.Kindle{}
			if err := k.Discover(); err != nil {
				return capture.WindowInfo{}, err
			}
			return k.Info(), nil
		},
		testCapture:   testCapture,
		accessibility: capture.CheckAccessibility,
		ocr:           checkOCR,
		sleep:         time.Sleep,
		launchWait:    launchWait,
	}
}

// macOSVersion は sw_vers で macOS のバージョン（例: "26.6.2"）を返す。
func macOSVersion() (string, error) {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "", fmt.Errorf("sw_vers に失敗しました: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// testCapture は Kindle のウィンドウを一時ファイルに撮り、すぐ消す。
// 画面収録の権限が無いと screencapture -l が失敗するので、権限の有無を実際に撮って確かめる
// （権限を直接問い合わせる公開 API はダイアログを出してしまうため使わない）。
func testCapture() error {
	f, err := os.CreateTemp("", "kindlesnap-doctor-*.png")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	_, err = capture.Peek(path)
	return err
}

// parseMajor は "26.6.2" のようなバージョン文字列からメジャー番号を取り出す。
func parseMajor(v string) (int, error) {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0, fmt.Errorf("バージョンを解釈できません: %q", v)
	}
	return n, nil
}

// runDoctor は全項目を調べて結果を返す。launch が true なら、Kindle が起動していなければ
// バックグラウンドで起動し、ウィンドウが現れるまで最大 env.launchWait 待つ。
//
// 項目は互いに独立に見せたいので、前の項目が失敗しても続ける（まとめて直せるように）。
// ただし Kindle のウィンドウが無いと撮影テストはできないので、その場合は「未確認」で失敗にする。
func runDoctor(env doctorEnv, launch bool) []check {
	var checks []check

	// 1. macOS のバージョン
	if v, err := env.macOSVersion(); err != nil {
		checks = append(checks, check{Name: "macos", Detail: err.Error(), Fix: "sw_vers が動く macOS で実行してください"})
	} else if major, perr := parseMajor(v); perr != nil {
		checks = append(checks, check{Name: "macos", Detail: perr.Error(), Fix: fmt.Sprintf("macOS %d 以降で実行してください", minMacOSMajor)})
	} else if major < minMacOSMajor {
		checks = append(checks, check{Name: "macos", Detail: "macOS " + v, Fix: fmt.Sprintf("macOS %d 以降にアップデートしてください（OCR が使う Vision の機能が必要）", minMacOSMajor)})
	} else {
		checks = append(checks, check{Name: "macos", OK: true, Detail: "macOS " + v})
	}

	// 2. Kindle の起動（--launch なら起動する）
	running, err := env.kindleRunning()
	launched := false
	switch {
	case err != nil:
		checks = append(checks, check{Name: "kindle_running", Detail: err.Error(), Fix: "pgrep が使えるか確認してください"})
	case running:
		checks = append(checks, check{Name: "kindle_running", OK: true, Detail: "Kindle は起動しています"})
	case launch:
		if lerr := env.launch(); lerr != nil {
			checks = append(checks, check{Name: "kindle_running", Detail: lerr.Error(), Fix: "Kindle for Mac がインストールされているか確認してください"})
		} else {
			launched = true
			checks = append(checks, check{Name: "kindle_running", OK: true, Detail: "Kindle をバックグラウンドで起動しました"})
		}
	default:
		checks = append(checks, check{Name: "kindle_running", Detail: "Kindle が起動していません", Fix: "Kindle for Mac を起動して本を開いてください（kindlesnap doctor --launch でも起動できます）"})
	}

	// 3. Kindle のウィンドウ（起動した直後はウィンドウが出るまで待つ）
	win, derr := env.discover()
	if launched {
		for waited := time.Duration(0); derr != nil && waited < env.launchWait; waited += time.Second {
			env.sleep(time.Second)
			win, derr = env.discover()
		}
	}
	windowFound := derr == nil
	if windowFound {
		checks = append(checks, check{Name: "kindle_window", OK: true,
			Detail: fmt.Sprintf("pid=%d window=%d %gx%g @(%g,%g)", win.PID, win.WindowID, win.Width, win.Height, win.X, win.Y)})
	} else {
		checks = append(checks, check{Name: "kindle_window", Detail: derr.Error(), Fix: "Kindle for Mac で本を開いてください（ウィンドウを閉じていないか確認）"})
	}

	// 4〜5. 画面上の表示と撮影テスト（画面収録の権限）。
	// 別 Space の通常ウィンドウは撮れるので、画面外であること自体は失敗にしない。
	// 撮影に失敗したときだけ、画面外なら「最小化・別 Space のフルスクリーン」を、
	// 画面上なら「画面収録の権限」を原因として案内する。
	if !windowFound {
		checks = append(checks,
			check{Name: "window_onscreen", Detail: "Kindle のウィンドウが無いため未確認", Fix: "先に kindle_window を解決してください"},
			check{Name: "screen_recording", Detail: "Kindle のウィンドウが無いため未確認", Fix: "先に kindle_window を解決してください"})
	} else {
		capErr := env.testCapture()
		switch {
		case capErr == nil && win.OnScreen:
			checks = append(checks,
				check{Name: "window_onscreen", OK: true, Detail: "画面に表示されています"},
				check{Name: "screen_recording", OK: true, Detail: "Kindle のウィンドウを撮影できました"})
		case capErr == nil:
			checks = append(checks,
				check{Name: "window_onscreen", OK: true, Detail: "画面外（別 Space）ですが撮影できます"},
				check{Name: "screen_recording", OK: true, Detail: "Kindle のウィンドウを撮影できました"})
		case !win.OnScreen:
			checks = append(checks,
				check{Name: "window_onscreen", Detail: "画面外のウィンドウを撮影できません（最小化、または別 Space でフルスクリーン表示の可能性）",
					Fix: "Kindle の最小化・フルスクリーンを解除して、通常のウィンドウ表示にしてください"},
				check{Name: "screen_recording", Detail: "撮影に失敗しました（ウィンドウが画面外のため権限の有無は判定できません）: " + capErr.Error(),
					Fix: "window_onscreen を解決してから再確認してください"})
		default:
			checks = append(checks,
				check{Name: "window_onscreen", OK: true, Detail: "画面に表示されています"},
				check{Name: "screen_recording", Detail: "撮影に失敗しました: " + capErr.Error(),
					Fix: "システム設定 > プライバシーとセキュリティ > 画面収録 で、kindlesnap を起動したターミナルアプリを許可してから、ターミナルを再起動してください"})
		}
	}

	// 6. アクセシビリティ（ページ送りのキーとクリックに必要）
	if err := env.accessibility(); err != nil {
		checks = append(checks, check{Name: "accessibility", Detail: err.Error(),
			Fix: "システム設定 > プライバシーとセキュリティ > アクセシビリティ で、kindlesnap を起動したターミナルアプリを許可してから、ターミナルを再起動してください"})
	} else {
		checks = append(checks, check{Name: "accessibility", OK: true, Detail: "アクセシビリティ権限があります"})
	}

	// 7. OCR（Markdown 変換に必要）
	if err := env.ocr(); err != nil {
		// 準備不足（swiftc が無い・macOS が古い・ビルド失敗）なら、internal/ocr が
		// 原因に合った対処を SetupError.Fix に入れている。無ければ一般的な対処を出す
		fix := "Xcode Command Line Tools を入れてください（xcode-select --install）"
		var se *ocr.SetupError
		if errors.As(err, &se) && se.Fix != "" {
			fix = se.Fix
		}
		checks = append(checks, check{Name: "ocr", Detail: err.Error(),
			Fix: fix + "。Markdown が不要なら capture --no-md で撮影だけできます"})
	} else {
		checks = append(checks, check{Name: "ocr", OK: true, Detail: "OCR ヘルパーを使えます"})
	}

	return checks
}

// allOK はすべての項目が成功したかを返す。
func allOK(checks []check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// cmdDoctor は kindlesnap doctor。全項目が成功なら exitOK、そうでなければ exitPreflight。
func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor", stderr)
	launch := fs.Bool("launch", false, "Kindle が起動していなければバックグラウンドで起動し、ウィンドウが現れるまで最大30秒待つ")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}

	checks := runDoctor(realDoctorEnv(), *launch)
	ok := allOK(checks)
	writeJSON(stdout, doctorOutput{OK: ok, Checks: checks})
	if !ok {
		return exitPreflight
	}
	return exitOK
}
