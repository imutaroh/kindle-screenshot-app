package session

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/frame"
)

// pollFileName は適応待機のポーリング用の撮影先（outDir 直下）。
// 撮影は毎回ここに上書きし、採用したときだけ page_%04d.png へ os.Rename する。
// こうしておくと、停止やエラーで抜けたときに「採用前の（描画途中かもしれない）フレーム」が
// page_*.png として残らず、PDF に紛れ込まない。
//
// ドットファイル（.poll.png）にしてはいけない。screencapture は先頭がドットのファイル名への
// 書き込みを「cannot write file to intended destination」で拒否し、しかも exit 0 を返す
// （実機で確認。captureWindow の保存検証で毎回失敗になり、撮影が一切できなくなる）。
const pollFileName = "poll.png"

// pausedMessage は一時停止中に画面に出す案内文。
const pausedMessage = "ページが進みません。Kindle の本文を1回クリックしてください（キー受付の復旧）。本の最後なら『終了してPDF作成』を押してください"

// kindleDriver は撮影ループが使う Kindle 操作。*capture.Kindle が実装する。
// ユニットテストで偽物（ページ送りに反応しない Kindle など）に差し替えるためのインタフェース。
type kindleDriver interface {
	Screenshot(path string) error
	TurnPage(dir capture.Direction) error
	WindowWidth() float64
}

// settleResult は captureSettled の結果。
type settleResult int

const (
	// settleAccepted は前ページから変化し、描画も安定した（採用する）。
	settleAccepted settleResult = iota
	// settleUnstable は変化したが、延長しても描画が安定しなかった。最後の1枚を採用し、警告を出す。
	settleUnstable
	// settleUnchanged はタイムアウトまで前ページから変化しなかった（ページ送りが効いていない）。
	settleUnchanged
	// settleStopped は停止または終了の要求が来た。
	settleStopped
)

// loopEnd は captureLoop がエラー以外で終わった理由。
type loopEnd int

const (
	// endMaxPages は設定の最大ページ数に達した。
	endMaxPages loopEnd = iota
	// endInterrupted は停止または終了の要求で抜けた（どちらかは Manager のフラグで判別する）。
	endInterrupted
	// endPauseTimeout は一時停止が DefaultPauseLimit 続いた。
	endPauseTimeout
)

// captureLoop はスクショ→変化判定→ページ送りを繰り返す。
// Python 版 src/screenshot.py の capture_all_pages の移植がもとだが、終了条件を大きく変えている。
//
// 旧実装は「前ページと同じ画面が2回続いたら最終ページ」として自動終了していた。しかし
// Kindle は起動直後やマウスが乗った後にバックグラウンドのキーを受け付けなくなることがあり、
// その状態も「同じ画面が続く」ため、本の途中で正常終了してしまっていた（途中停止の主因）。
// 画面だけでは最終ページとキー不感を区別できないので、ページが進まないときは自動終了せず
// 再送 → 一時停止（ユーザーに本文クリックか「終了してPDF作成」を求める）に進む
// （recoverUnchanged）。最終ページの判断はユーザーに委ねる。
//
// 戻り値:
//
//	captured : 保存できたページ数
//	end      : エラー以外で終わった理由（err != nil のときは意味を持たない）
//	err      : 回復不能なエラー
func (m *Manager) captureLoop(drv kindleDriver, outDir string, maxPages int, direction capture.Direction) (captured int, end loopEnd, err error) {
	poll := filepath.Join(outDir, pollFileName)
	defer os.Remove(poll)

	var prev frame.Frame // 前ページとして採用済みのフレーム（1ページ目は未計算）
	rewound := false     // このページで暴走からの巻き戻しを済ませたか（1ページにつき1回まで）

	for page := 1; ; page++ {
		// ページキャプチャ（適応待機：画面の変化を検知してから撮る。詳細は captureSettled を参照）。
		// 最初の数ページは Kindle の初期読み込みが遅く、描画が段階的に進むことがあるので、
		// タイムアウトを長くし、安定の確認も2回連続を求める。
		settleTimeout, stableNeeded := m.timing.settle, 1
		if page <= DefaultInitialPagesCount {
			settleTimeout, stableNeeded = m.timing.initialSettle, 2
		}
		res, cur, serr := m.captureSettled(drv, poll, prev, settleTimeout, stableNeeded)
		if serr != nil {
			return page - 1, 0, serr
		}

		if res == settleUnchanged {
			var timedOut bool
			res, cur, timedOut, serr = m.recoverUnchanged(drv, poll, prev, page, direction)
			if serr != nil {
				return page - 1, 0, serr
			}
			if timedOut {
				m.addLog("info", fmt.Sprintf("⚠️ 一時停止が%v続いたため、%dページまでで終了します", m.timing.pauseLimit, page-1))
				return page - 1, endPauseTimeout, nil
			}
		}

		switch res {
		case settleStopped:
			return page - 1, endInterrupted, nil
		case settleUnstable:
			// 画面が動き続ける＝キーの押しっぱなし扱いでページ送りが暴走している疑い。
			// そのまま採用すると暴走で通過したページがすべて抜けるので、止めて前ページまで
			// 巻き戻し、このページを撮り直す。本当に描画が重いだけのページなら巻き戻しは
			// 1枚で済み、撮り直しでまた不安定なら（2回目は巻き戻さず）従来どおり採用する。
			if !prev.IsZero() && !rewound {
				rewound = true
				if rerr := m.rewindRunaway(drv, poll, prev, page, direction); rerr != nil {
					return page - 1, 0, rerr
				}
				page-- // 同じページをもう一度撮る（for の page++ で元に戻る）
				continue
			}
			m.addLog("info", fmt.Sprintf("⚠️ ページ %d: 描画が安定しなかった可能性があります（待ち時間を延長しても画面が動き続けたため、最後の画面を採用）", page))
		}
		rewound = false

		// 採用したフレームだけを連番ファイルにする
		path := pagePath(outDir, page)
		if rerr := os.Rename(poll, path); rerr != nil {
			return page - 1, 0, fmt.Errorf("ページ %d の保存に失敗しました: %w", page, rerr)
		}
		prev = cur

		// 進捗通知
		m.mu.Lock()
		m.currentPage = page
		m.message = fmt.Sprintf("ページ %d を保存中...", page)
		m.mu.Unlock()
		m.addLog("info", fmt.Sprintf("ページ %d を保存", page))

		if page == 1 {
			m.reportResolution(drv, path)
		}

		// 最大ページ数チェック（captureSettled の関知しない captureLoop 固有の終了条件）。
		// 最後のページの後にはページ送りしない（Kindle の読書位置を撮った最後のページに残す）
		if maxPages > 0 && page >= maxPages {
			return page, endMaxPages, nil
		}

		// 次のページへ（描画待機は次ループの captureSettled が適応的に行う。固定sleepはしない）
		if terr := drv.TurnPage(direction); terr != nil {
			return page, 0, terr
		}
	}
}

// captureSettled は「前ページと変わった」かつ「直前のポーリングと同じ（＝描画が止まって
// 安定した）」と判定できるまでスクショを撮り続け、結果と最後に撮ったフレームを返す（適応待機）。
// 撮影先は poll 固定で、撮るたびに上書きするため、返したフレームの画像は poll に残っている
// （採用するなら呼び出し側が rename する）。
//
//   - prev         : 前ページとして採用済みのフレーム。未計算（1ページ目）なら「変わった」を無条件に満たす
//   - timeout      : 安定待ちのタイムアウト
//   - stableNeeded : 「直前のポーリングと同じ」が何回連続したら安定とみなすか
//
// 結果:
//   - settleAccepted : 変化して安定した
//   - settleUnstable : 変化したが安定しないままタイムアウト → 1回だけ同じ長さ延長しても
//     安定しなかった。最後の1枚を採用してよいが、描画途中の可能性がある
//   - settleUnchanged: タイムアウトまで（最後の1枚も）前ページから変化しなかった
//   - settleStopped  : 停止/終了の要求が来た（フレームは返さない）
//
// ループの毎回の反復で停止要求をチェックするため、timeout の間ずっと無反応になることはない
// （最悪でも ポーリング間隔 + スクショ1回分の時間で反応する）。
//
// スクショの失敗（ページめくりアニメーション中にウィンドウが一時的にキャプチャ不能に
// なるなど、実機で3連続失敗を観測済み）は1回では判断しない。DefaultCaptureFailWindow の
// 間かつ minCaptureFailures 回以上連続で失敗したときだけ、恒常的な失敗としてエラーを返す。
// タイムアウトとは独立に数えるので、撮影できない間にタイムアウトしても「変化なし」とは扱わない。
func (m *Manager) captureSettled(drv kindleDriver, poll string, prev frame.Frame, timeout time.Duration, stableNeeded int) (settleResult, frame.Frame, error) {
	deadline := time.Now().Add(timeout)
	extended := false
	changedSeen := false // このページで一度でも「前ページから変わった」画面を見たか
	stableRun := 0       // 「変化済みかつ直前のポーリングと同じ」の連続回数
	var last frame.Frame // このページに対する直前のポーリング

	failures := 0
	var failSince time.Time

	for {
		if m.interrupted() {
			return settleStopped, frame.Frame{}, nil
		}

		cur, cerr := shoot(drv, poll)
		if cerr != nil {
			if failures == 0 {
				failSince = time.Now()
			}
			failures++
			if failures >= minCaptureFailures && time.Since(failSince) >= m.timing.captureFailWindow {
				return 0, frame.Frame{}, fmt.Errorf("Kindle のウィンドウを撮影できません（%d回・約%.0f秒連続で失敗）。"+
					"Kindle が最小化されている・画面がロックされている、またはウィンドウが閉じられた可能性があります。"+
					"Kindle のウィンドウを通常表示に戻してからやり直してください: %w",
					failures, time.Since(failSince).Seconds(), cerr)
			}
			time.Sleep(m.timing.poll)
			continue
		}
		failures = 0

		changed, stable := classify(cur, prev, last)
		if changed {
			changedSeen = true
		}
		if changed && stable {
			stableRun++
		} else {
			stableRun = 0
		}
		if changed && stableRun >= stableNeeded {
			return settleAccepted, cur, nil
		}
		last = cur

		if time.Now().After(deadline) {
			switch {
			case !changed && !changedSeen:
				return settleUnchanged, cur, nil
			case !extended && changedSeen:
				// 変化はしたが描画が終わらない（重いページ・図版の読み込み中など）。1回だけ待ち直す
				extended = true
				deadline = time.Now().Add(timeout)
			case changed:
				return settleUnstable, cur, nil
			default:
				// 延長後の最後の1枚が前ページと同じ（変化した画面が元に戻った）。採用すると
				// 重複ページになるので「変化なし」として扱い、再送・一時停止の段階に任せる
				return settleUnchanged, cur, nil
			}
		}
		time.Sleep(m.timing.poll)
	}
}

// maxRewindSteps は暴走からの巻き戻しで逆方向へ送る最大枚数。実機の暴走は最大で
// 約190見開きだったので、余裕を持たせる（1枚あたり1〜2秒なので最悪でも十数分）。
const maxRewindSteps = 400

// rewindRunaway はページ送りの暴走（Kindle が矢印キーを押しっぱなしと解釈し、ページを
// 自動で送り続ける状態）を止め、前ページとして採用済みの画面まで逆方向へ1枚ずつ戻したうえで、
// 正方向へ1枚だけ送る。呼び出し側はその後、同じページの撮影をやり直す。
//
// 暴走の停止には同じ方向のキーをもう1回送る（新しい down/up で「押しっぱなし」が解除される。
// 実機で、暴走は次のキーで止まっていた）。そのキーで1枚余分に進んでも、巻き戻しで吸収する。
// 巻き戻しは1枚ごとに画面の静止を待ってから前ページと比べるので、逆方向でも暴走したら
// （静止しないまま通過するので）見つからずに上限で止まり、エラーになる。
func (m *Manager) rewindRunaway(drv kindleDriver, poll string, prev frame.Frame, page int, direction capture.Direction) error {
	m.addLog("error", fmt.Sprintf("⚠️ ページ %d: 画面が動き続けています。ページ送りが暴走した可能性があるため、止めて前のページまで巻き戻します", page))
	m.setMessage(fmt.Sprintf("ページ送りの暴走を検知しました。ページ %d まで巻き戻しています...", page-1))

	if err := drv.TurnPage(direction); err != nil {
		return err
	}
	back := opposite(direction)
	for step := 0; step <= maxRewindSteps; step++ {
		cur, stopped, err := m.waitStill(drv, poll)
		if err != nil {
			return err
		}
		if stopped {
			return nil // 停止要求。呼び出し側の captureSettled が settleStopped を返す
		}
		if d, derr := cur.Diff(prev); derr == nil && d <= frame.ChangedFrac {
			m.addLog("success", fmt.Sprintf("↩️ ページ %d まで巻き戻しました（%d枚戻りました）。撮影を続けます", page-1, step))
			return drv.TurnPage(direction)
		}
		if step == maxRewindSteps {
			break
		}
		if err := drv.TurnPage(back); err != nil {
			return err
		}
	}
	return fmt.Errorf("ページ送りの暴走から巻き戻せませんでした（%d枚戻してもページ %d が見つかりません）。"+
		"Kindle でページ %d の次を開いてから、残りを取り込み直してください", maxRewindSteps, page-1, page-1)
}

// waitStill は画面が静止する（直前のポーリングと StableFrac 以下になる）まで撮り続け、
// 静止した画面のフレームを返す。前ページとの比較はしない（巻き戻し中は「どこにいるか」を
// 知りたいだけなので）。settle の2倍待っても静止しなければ、最後の1枚を返す。
func (m *Manager) waitStill(drv kindleDriver, poll string) (frame.Frame, bool, error) {
	deadline := time.Now().Add(2 * m.timing.settle)
	var last frame.Frame
	failures := 0
	for {
		if m.interrupted() {
			return frame.Frame{}, true, nil
		}
		cur, err := shoot(drv, poll)
		if err != nil {
			failures++
			if failures >= minCaptureFailures && time.Now().After(deadline) {
				return frame.Frame{}, false, fmt.Errorf("巻き戻し中に Kindle のウィンドウを撮影できません: %w", err)
			}
			time.Sleep(m.timing.poll)
			continue
		}
		failures = 0
		if !last.IsZero() {
			if d, derr := cur.Diff(last); derr == nil && d <= frame.StableFrac {
				return cur, false, nil
			}
		}
		if time.Now().After(deadline) {
			return cur, false, nil
		}
		last = cur
		time.Sleep(m.timing.poll)
	}
}

// opposite はページ送りの逆方向を返す。
func opposite(d capture.Direction) capture.Direction {
	if d == capture.Left {
		return capture.Right
	}
	return capture.Left
}

// classify は適応待機の判定ロジック（Kindle操作やスクショ撮影に依存しない純粋関数。
// ユニットテストは internal/session/worker_test.go）。
//
//   - cur  : 直近のポーリングで撮ったフレーム
//   - prev : 前ページとして採用済みのフレーム（1ページ目は未計算＝IsZero）
//   - last : このページに対する直前のポーリングのフレーム（1回目のポーリングでは未計算＝IsZero）
//
// changed は「前ページから変わった（変化画素率が frame.ChangedFrac 超）」。prev が未計算なら
// 比較対象がないので無条件に true。stable は「直前のポーリングと同じ（変化画素率が
// frame.StableFrac 以下）」。last が未計算なら比較対象がないので必ず false。
//
// 安定判定に frame.Same（完全一致）ではなく Diff を使うのは、同じページを撮り直しても
// 数画素だけ値が揺れる例が実画像にあったため（Same=false だが Diff=0）。
func classify(cur, prev, last frame.Frame) (changed, stable bool) {
	changed = prev.IsZero()
	if !changed {
		if d, err := cur.Diff(prev); err == nil {
			changed = d > frame.ChangedFrac
		}
	}
	if !last.IsZero() {
		if d, err := cur.Diff(last); err == nil {
			stable = d <= frame.StableFrac
		}
	}
	return changed, stable
}

// recoverUnchanged は「ページ送りしたのに画面が変わらない」ときの段階的な対応。
// 画面が変わったらその結果（新しいページとして採用できるフレーム）を返す。
//
//  1. 数秒あけてキーを再送する（最大 DefaultResendCount 回、毎回 captureSettled で確認）
//  2. それでも変わらなければ一時停止に入り（pauseUntilChanged）、ユーザーの復旧を待つ
//
// キーは「1回送る → 変化を確認」を1回ずつ繰り返し、変化したらそれ以上は送らない。
// まとめて送ると、キー受付が復旧した瞬間に複数ページ進んで取り逃すため。
// さらに各送信の前に、送らずに1枚撮って変化を確かめる（peekChanged）。遅れて効いた
// ページ送りやユーザーのクリックで既にページが進んでいたら、送らずにそれを採用する。
//
// 復旧後のページは、Kindle が描画をやり直している最中かもしれないので、安定を2回連続で求める。
func (m *Manager) recoverUnchanged(drv kindleDriver, poll string, prev frame.Frame, page int, direction capture.Direction) (res settleResult, cur frame.Frame, timedOut bool, err error) {
	for i := 1; i <= DefaultResendCount; i++ {
		m.addLog("info", fmt.Sprintf("⚠️ ページ %d: ページが進みません。キーを再送します（%d/%d）", page, i, DefaultResendCount))
		if !m.sleepInterruptible(m.timing.resendDelay) {
			return settleStopped, frame.Frame{}, false, nil
		}
		res, cur, err = m.sendAndSettle(drv, poll, prev, direction)
		if err != nil || res != settleUnchanged {
			return res, cur, false, err
		}
	}
	return m.pauseUntilChanged(drv, poll, prev, page, direction)
}

// pauseUntilChanged は一時停止状態に入り、DefaultPausedKeyInterval ごとにキーを1回送って
// 画面の変化を監視する。変化したら（＝ユーザーが本文をクリックしてキー受付が復旧した）
// 自動で撮影を再開し、その画面を次のページとして返す。
//
// 停止/終了の要求が来たら settleStopped を、DefaultPauseLimit 経っても変化しなければ
// timedOut=true を返す。
func (m *Manager) pauseUntilChanged(drv kindleDriver, poll string, prev frame.Frame, page int, direction capture.Direction) (res settleResult, cur frame.Frame, timedOut bool, err error) {
	m.setStatus(StatusPaused, pausedMessage)
	m.addLog("error", fmt.Sprintf("⏸️ 一時停止: ページ %d に進めません（%dページまで保存済み）。Kindle の本文を1回クリックすると自動で再開します。本の最後なら『終了してPDF作成』を押してください", page, page-1))
	m.notify("PageSnap", "ページが進みません。Kindle の本文を1回クリックしてください。本の最後なら「終了してPDF作成」を押してください")

	pausedAt := time.Now()
	lastKey := time.Now() // 直前の再送からの間隔も DefaultPausedKeyInterval 空ける
	for {
		if time.Since(pausedAt) >= m.timing.pauseLimit {
			return settleUnchanged, frame.Frame{}, true, nil
		}
		wait := min(m.timing.pausedKeyInterval-time.Since(lastKey), m.timing.pauseLimit-time.Since(pausedAt))
		if !m.sleepInterruptible(wait) {
			return settleStopped, frame.Frame{}, false, nil
		}
		if time.Since(pausedAt) >= m.timing.pauseLimit {
			return settleUnchanged, frame.Frame{}, true, nil
		}

		lastKey = time.Now()
		res, cur, err = m.sendAndSettle(drv, poll, prev, direction)
		if err != nil {
			return 0, frame.Frame{}, false, err
		}
		switch res {
		case settleUnchanged:
			continue
		case settleStopped:
			return res, cur, false, nil
		default:
			m.setStatus(StatusRunning, fmt.Sprintf("撮影を再開しました（ページ %d）", page))
			m.addLog("success", fmt.Sprintf("▶️ 再開しました（ページ %d）", page))
			return res, cur, false, nil
		}
	}
}

// sendAndSettle は、まだページが進んでいなければキーを1回送り、変化を待つ。
// 送る前に1枚撮って既に変化していたら（遅れて効いたページ送り・ユーザーのクリック）、
// キーは送らずにその変化を待つ。
func (m *Manager) sendAndSettle(drv kindleDriver, poll string, prev frame.Frame, direction capture.Direction) (settleResult, frame.Frame, error) {
	if !peekChanged(drv, poll, prev) {
		if err := drv.TurnPage(direction); err != nil {
			return 0, frame.Frame{}, err
		}
	}
	return m.captureSettled(drv, poll, prev, m.timing.settle, 2)
}

// peekChanged は1枚撮って、前ページから変わっているかだけを返す。
// 撮影に失敗したら false（恒常的な失敗の判定は直後の captureSettled に任せる）。
func peekChanged(drv kindleDriver, poll string, prev frame.Frame) bool {
	cur, err := shoot(drv, poll)
	if err != nil {
		return false
	}
	changed, _ := classify(cur, prev, frame.Frame{})
	return changed
}

// shoot はスクショを1枚 poll に撮り、比較用のフレームを作る。
func shoot(drv kindleDriver, poll string) (frame.Frame, error) {
	if err := drv.Screenshot(poll); err != nil {
		return frame.Frame{}, err
	}
	return frame.Load(poll, drv.WindowWidth())
}

// lowResScale はこれ未満のバッキングスケールを「非Retina（1x）」とみなす境界。
// 1x と 2x の間（HiDPI のスケーリング設定で 1.5x 等になりうる）の中間より低めに置く。
const lowResScale = 1.25

// reportResolution は1ページ目の PNG の画素数とウィンドウのポイント幅から
// バッキングスケール（Retina 倍率）を求め、状態とログに出す。1x なら画質の注意を出す。
// 画素数自体はコードでは増やせない（Kindle を Retina/HiDPI のディスプレイに置くしかない）ので、
// 取り込みの早い段階でユーザーに知らせることが目的。
func (m *Manager) reportResolution(drv kindleDriver, path string) {
	w, h, err := pngSize(path)
	if err != nil {
		return // 解像度表示は補助情報なので、読めなくても撮影は続ける
	}
	res := formatResolution(w, h, drv.WindowWidth())

	m.mu.Lock()
	m.resolution = res
	m.mu.Unlock()
	m.addLog("info", "解像度: "+res)

	if pt := drv.WindowWidth(); pt > 0 && float64(w)/pt < lowResScale {
		m.addLog("info", "⚠️ 画質注意: 非Retina(1x)ディスプレイ上で撮影中です。MacBook 内蔵 Retina や HiDPI ディスプレイに Kindle を移すと約1.3〜2倍の解像度になります")
	}
}

// formatResolution は "1720x1410px（1x）" 形式の文字列を作る。
// ポイント幅が不明（0）なら倍率は付けない。倍率は小数第1位に丸める（1.5x など）。
func formatResolution(w, h int, pointWidth float64) string {
	s := fmt.Sprintf("%dx%dpx", w, h)
	if pointWidth <= 0 {
		return s
	}
	scale := math.Round(float64(w)/pointWidth*10) / 10
	return s + "（" + strconv.FormatFloat(scale, 'f', -1, 64) + "x）"
}

// pngSize は PNG のヘッダだけを読んで画素数を返す（全体はデコードしない）。
func pngSize(path string) (w, h int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("PNGヘッダの読み込みに失敗しました: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// pagePath はページ番号から連番ファイルパスを作る（例: output/本/page_0001.png）。
func pagePath(outDir string, page int) string {
	return filepath.Join(outDir, fmt.Sprintf("page_%04d.png", page))
}
