package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockInfo はロックファイルの中身。status / finish / stop が「どのプロセスに
// シグナルを送るか」「どの本のフォルダの status.json を読むか」を知るために使う。
type lockInfo struct {
	PID     int    `json:"pid"`
	BookDir string `json:"book_dir"` // 出力フォルダが決まる前は空文字
}

// lockPath はロックファイルの場所。テストで一時ディレクトリに差し替えるため変数にしている。
var lockPath = func() string {
	return filepath.Join(os.TempDir(), "kindlesnap.lock")
}

// errLocked は別の kindlesnap capture が実行中でロックを取れなかったことを表す。
type errLocked struct {
	holder lockInfo
}

func (e *errLocked) Error() string {
	if e.holder.PID == 0 {
		return "別の kindlesnap capture が実行中です"
	}
	msg := fmt.Sprintf("別の kindlesnap capture が実行中です（pid=%d", e.holder.PID)
	if e.holder.BookDir != "" {
		msg += ", " + e.holder.BookDir
	}
	return msg + "）。kindlesnap status で状態を確認できます"
}

// fileLock は取得済みのロック。capture の実行中ずっと保持する。
//
// 排他には flock(2) を使い、ファイルの中身（pid と本のフォルダ）は情報としてだけ使う。
// flock はプロセスが落ちるとカーネルが自動で解放するので、kill -9 やクラッシュで
// ロックファイルが残っても次の capture は普通に始められる（「pid が生きているか」を
// 見て古いロックを消す、という競合しやすい処理が要らない）。
type fileLock struct {
	f    *os.File
	info lockInfo
}

// acquireLock はロックを取り、中身に pid を書く。取れなければ *errLocked を返す。
func acquireLock(path string, pid int) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("ロックファイルを開けません: %w", err)
	}
	// LOCK_NB: 取れなければ待たずに EWOULDBLOCK で返る（2つ目の capture は即座に断る）
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _, _ := readLock(path)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &errLocked{holder: holder}
		}
		return nil, fmt.Errorf("ロックの取得に失敗しました: %w", err)
	}
	l := &fileLock{f: f, info: lockInfo{PID: pid}}
	if err := l.write(); err != nil {
		l.release()
		return nil, err
	}
	return l, nil
}

// setBookDir は出力フォルダが決まったときに中身を書き換える。
func (l *fileLock) setBookDir(dir string) error {
	l.info.BookDir = dir
	return l.write()
}

// write は中身を JSON で上書きする。読み手（status）が書き換えの途中を読むと空や
// 壊れた JSON に見えることがあるが、readLock はそれを「情報なし」として扱う。
func (l *fileLock) write() error {
	data, err := json.Marshal(l.info)
	if err != nil {
		return err
	}
	if err := l.f.Truncate(0); err != nil {
		return fmt.Errorf("ロックファイルの書き込みに失敗しました: %w", err)
	}
	if _, err := l.f.WriteAt(data, 0); err != nil {
		return fmt.Errorf("ロックファイルの書き込みに失敗しました: %w", err)
	}
	return nil
}

// release はロックを手放す。ファイルは消さずに中身を空にする。
// 消すと、消す直前にファイルを開いていた別プロセスが「もう誰も見ないファイル」のロックを
// 取ってしまい、さらに3つ目のプロセスが新しいファイルでロックを取れて二重起動になりうるため。
func (l *fileLock) release() {
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// readLock はロックファイルの中身を読む。ファイルが無い・空・壊れている場合は ok=false。
func readLock(path string) (info lockInfo, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return lockInfo{}, false, nil
	}
	if err != nil {
		return lockInfo{}, false, fmt.Errorf("ロックファイルを読めません: %w", err)
	}
	if len(data) == 0 || json.Unmarshal(data, &info) != nil || info.PID <= 0 {
		return lockInfo{}, false, nil
	}
	return info, true, nil
}

// processAlive は pid のプロセスが生きているかを返す（シグナル 0 は届くかの確認だけで何もしない）。
// EPERM は「存在するが権限が無い」なので生きている扱いにする。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// lockHeld は誰かが path の flock を保持しているかを調べる。
// 共有ロックを待たずに取りにいき、取れたらすぐ手放す（取れた＝誰も保持していない）。
//
// pid が生きているかだけで判断しないのは、クラッシュで中身が残ったあとに同じ pid が
// 別のプロセスに再利用されると、finish がそのプロセスに SIGUSR1 を送ってしまうため
// （SIGUSR1 の既定動作はプロセスの終了）。flock の保持は capture 本人にしかできない。
//
// 調べている一瞬だけ共有ロックを持つので、ちょうどその瞬間に始まった capture が
// 「実行中」と誤って断られる可能性はある（マイクロ秒の窓なので実用上は無視する）。
func lockHeld(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ロックファイルを開けません: %w", err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("ロックの確認に失敗しました: %w", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// runningCapture は実行中の capture の情報を返す。ロックが保持されていて、中身に
// 生きている pid が書かれていれば実行中とみなす。
func runningCapture() (lockInfo, bool, error) {
	path := lockPath()
	held, err := lockHeld(path)
	if err != nil || !held {
		return lockInfo{}, false, err
	}
	info, ok, err := readLock(path)
	if err != nil || !ok || !processAlive(info.PID) {
		// 保持されているのに中身が読めないのは、書き換えの途中を読んだ場合
		return lockInfo{}, false, err
	}
	return info, true, nil
}
