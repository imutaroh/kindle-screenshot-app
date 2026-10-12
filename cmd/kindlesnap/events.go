package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/session"
)

// CLI が出すステータス文字列。session の日本語ステータス（人が読む表示用）とは別に、
// 機械が読みやすい英語の固定値にする。
const (
	statusRunning     = "running"
	statusPaused      = "paused"
	statusDone        = "done"
	statusError       = "error"
	statusInterrupted = "interrupted"
)

// logEvent は {"type":"log",...}。session のログ1件に対応する。
type logEvent struct {
	Type  string    `json:"type"` // 常に "log"
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // "info" | "success" | "error"
	Text  string    `json:"text"`
}

// stateEvent は {"type":"state",...}。状態（ステータス・ページ・メッセージ）が変わるたびに出す。
type stateEvent struct {
	Type    string `json:"type"` // 常に "state"
	Status  string `json:"status"`
	Page    int    `json:"page"`
	Message string `json:"message"`
}

// resultEvent は {"type":"result",...}。capture / md の最後に1回だけ出す。
// Markdown と Error は「無い」を null で表したいのでポインタにしている。
type resultEvent struct {
	Type        string   `json:"type"` // 常に "result"
	OK          bool     `json:"ok"`
	Status      string   `json:"status"`
	BookDir     string   `json:"book_dir"`
	PDFs        []string `json:"pdfs"`
	Markdown    *string  `json:"markdown"`
	Pages       int      `json:"pages"`
	ReviewPages []int    `json:"review_pages"`
	Error       *string  `json:"error"`
}

// statusFile は <bookDir>/status.json の中身。最後の状態と（終わっていれば）結果を持つ。
// kindlesnap status がこれを読んで返すので、capture を別プロセスで動かしていても
// 進み具合を確かめられる。
type statusFile struct {
	PID       int          `json:"pid"`
	Book      string       `json:"book"`
	BookDir   string       `json:"book_dir"`
	Status    string       `json:"status"`
	Page      int          `json:"page"`
	Message   string       `json:"message"`
	UpdatedAt time.Time    `json:"updated_at"`
	Result    *resultEvent `json:"result"` // 終わるまでは null
}

func strPtr(s string) *string { return &s }

// cliStatus は session.State を CLI のステータス文字列に変換する。
// 終わった後（IsRunning=false）は EndReason で、実行中は Status で判断する
// （Status だけでは中断が「待機中」に畳まれて区別できないため）。
//
// ただし実行中でも、中断・エラーで終わることが決まった（EndReason が付いた）後は
// EndReason を使う。session はワーカーを終える前に「中断されました…」の状態を出すので、
// Status で判断すると {status:"running", message:"中断されました…"} という食い違った
// 状態が run.jsonl・status.json に出てしまう。中断・エラーは CLI の最終状態も同じ
// interrupted / error なので、途中で出しても最終結果と矛盾しない。
// 完了（done・一時停止の時間切れ）は、この後 CLI が Markdown を作ってから自分で done を
// 出すので、それまでは running のままにする（先に done を出すと終わったと誤解される）。
func cliStatus(st session.State) string {
	switch {
	case !st.IsRunning && st.EndReason != session.EndNone:
		return endStatus(st.EndReason)
	case st.EndReason == session.EndInterrupted || st.EndReason == session.EndError:
		return endStatus(st.EndReason)
	}
	switch st.Status {
	case session.StatusPaused:
		return statusPaused
	case session.StatusError:
		return statusError
	default:
		return statusRunning
	}
}

// endStatus は終わり方を CLI のステータス文字列に変換する。
// 一時停止の時間切れは「そこまでのページで完了」なので done にする（ログで区別できる）。
func endStatus(r session.EndReason) string {
	switch r {
	case session.EndDone, session.EndPauseTimeout:
		return statusDone
	case session.EndInterrupted:
		return statusInterrupted
	default:
		return statusError
	}
}

// eventSink はイベントを標準出力と <bookDir>/run.jsonl の両方に書き、
// <bookDir>/status.json を最新の状態に保つ。
//
// 出力フォルダは session が撮影開始時に作るので、最初のいくつかのイベント（開始ログなど）は
// フォルダが決まる前に起きる。その間は run.jsonl 用にメモリに溜めておき、setBookDir で
// フォルダが分かった時点でまとめて書き出す。先にフォルダを作ってしまうと、session の
// output.CreateUniqueDir が「既にファイルがある」と判断して「本_2」を作ってしまうため。
//
// session のフックはワーカー goroutine から、シグナル処理などは別の goroutine から
// 呼ばれうるので、ミューテックスで1行ずつ書き込みを直列化する。
type eventSink struct {
	mu      sync.Mutex
	out     io.Writer
	file    *os.File // run.jsonl（フォルダが決まるまでは nil）
	pending [][]byte // フォルダが決まる前のイベント（run.jsonl に後で書く）
	status  statusFile
	lastSt  stateEvent // 直前に出した状態（同じ状態を連続で出さないため）
	hasSt   bool
	now     func() time.Time
}

func newEventSink(out io.Writer, pid int, book string) *eventSink {
	return &eventSink{
		out:    out,
		status: statusFile{PID: pid, Book: book},
		now:    time.Now,
	}
}

// bookDir は run.jsonl と status.json の置き場所（未確定なら空文字）。
func (s *eventSink) bookDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status.BookDir
}

// setBookDir は出力フォルダを確定させ、run.jsonl を開いて溜めていたイベントを書き出す。
// 2回目以降の呼び出しは何もしない。
func (s *eventSink) setBookDir(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.BookDir != "" {
		return nil
	}
	s.status.BookDir = dir
	f, err := os.OpenFile(filepath.Join(dir, "run.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("run.jsonl を開けません: %w", err)
	}
	s.file = f
	for _, line := range s.pending {
		_, _ = f.Write(line)
	}
	s.pending = nil
	return s.writeStatusLocked()
}

// emitLocked は1イベントを1行の JSON にして書く。s.mu を取ったうえで呼ぶこと。
func (s *eventSink) emitLocked(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return // 自前の構造体なので起きない
	}
	line := append(data, '\n')
	_, _ = s.out.Write(line)
	if s.file != nil {
		_, _ = s.file.Write(line)
	} else {
		s.pending = append(s.pending, line)
	}
}

// log は {"type":"log"} を出す。
func (s *eventSink) log(t time.Time, level, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(logEvent{Type: "log", Time: t, Level: level, Text: text})
}

// logNow は現在時刻で {"type":"log"} を出す（session 以外の CLI 自身のログ用）。
func (s *eventSink) logNow(level, text string) {
	s.log(s.now(), level, text)
}

// state は {"type":"state"} を出し、status.json を更新する。直前と全く同じ状態なら出さない。
func (s *eventSink) state(status string, page int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := stateEvent{Type: "state", Status: status, Page: page, Message: message}
	if s.hasSt && ev == s.lastSt {
		return
	}
	s.lastSt, s.hasSt = ev, true
	s.emitLocked(ev)
	s.status.Status, s.status.Page, s.status.Message = status, page, message
	_ = s.writeStatusLocked() // status.json は補助情報。書けなくても取り込みは続ける
}

// result は {"type":"result"} を出し、status.json に結果を載せる。
func (s *eventSink) result(r resultEvent) {
	r.Type = "result"
	if r.PDFs == nil {
		r.PDFs = []string{} // JSON で null ではなく [] にする
	}
	if r.ReviewPages == nil {
		r.ReviewPages = []int{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(r)
	s.status.Status = r.Status
	s.status.Result = &r
	_ = s.writeStatusLocked()
}

// close は run.jsonl を閉じる。
func (s *eventSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
	}
}

// writeStatusLocked は status.json を書き換える。フォルダ未確定なら何もしない。
func (s *eventSink) writeStatusLocked() error {
	if s.status.BookDir == "" {
		return nil
	}
	s.status.UpdatedAt = s.now()
	data, err := json.MarshalIndent(s.status, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.status.BookDir, "status.json"), append(data, '\n'))
}

// writeFileAtomic は同じフォルダの一時ファイルに書いてから rename で置き換える。
// rename は同じファイルシステム内なら原子的なので、読み手（kindlesnap status）が
// 書きかけの壊れた JSON を読むことがない。
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// CreateTemp は 0600 で作る。他のツールからも読めるよう通常のファイルと同じにする
	if err = os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// resolveOutRoot は capture の出力先ルートを決める。優先順位は
// --out の指定 > 環境変数 KINDLESNAP_OUT > ~/Documents/KindleBooks。
// 結果は絶対パスにする（出力の book_dir を、どこから実行しても使えるパスにするため）。
func resolveOutRoot(flagValue, envValue string, home func() (string, error)) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = envValue
	}
	if dir == "" {
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("ホームディレクトリが分かりません（--out で出力先を指定してください）: %w", err)
		}
		if h == "" {
			return "", errors.New("ホームディレクトリが分かりません（--out で出力先を指定してください）")
		}
		dir = filepath.Join(h, "Documents", "KindleBooks")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("出力先のパスを解決できません: %w", err)
	}
	return abs, nil
}
