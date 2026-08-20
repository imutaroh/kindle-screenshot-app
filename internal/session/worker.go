package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/imutaroh/kindle-screenshot-app/internal/capture"
	"github.com/imutaroh/kindle-screenshot-app/internal/dedupe"
)

// captureLoop はスクショ→pHash重複判定→ページ送りを繰り返す。
// Python 版 src/screenshot.py の capture_all_pages の移植（旧 CLI 版 kindlesnap の
// captureAllPages と同系だが、以下の2点により共通化していなかった経緯がある）:
//   - Manager の状態（進捗・停止要求）を毎ページ更新する
//   - 停止判定が Manager のフラグ（Web UIの停止ボタン）経由
//
// 戻り値:
//
//	captured : 保存できたページ数
//	err      : 回復不能なエラー
func (m *Manager) captureLoop(kindle *capture.Kindle, outDir string, maxPages int, direction capture.Direction) (captured int, err error) {
	var prev dedupe.Hash
	dupRun := 0 // 直前ページと同一と判定された連続回数

	for page := 1; ; page++ {
		// 停止要求は captureSettled がポーリングごとに毎回チェックするため、
		// ここでの重複チェックはしない（captureSettled の stopped 戻り値をそのまま扱う）。

		// 最大ページ数チェック（captureSettled の関知しない captureLoop 固有の終了条件）
		if maxPages > 0 && page > maxPages {
			return page - 1, nil
		}

		// ページキャプチャ（適応待機：画面の変化を検知してから撮る。詳細は captureSettled を参照）
		settleTimeout := DefaultSettleTimeout
		if page <= DefaultInitialPagesCount {
			settleTimeout = DefaultInitialSettleTimeout
		}
		path := pagePath(outDir, page)
		h, stopped, serr := m.captureSettled(path, prev, settleTimeout)
		if serr != nil {
			return page - 1, serr
		}
		if stopped {
			return page - 1, nil
		}

		// 同じページかどうかをチェック（pHash ハミング距離による類似度判定）
		if !prev.IsZero() {
			dist, derr := h.Distance(prev)
			if derr == nil && dist <= DefaultDuplicateThreshold {
				dupRun++
				if dupRun >= DefaultDuplicateCheckCount {
					// 重複したページを削除
					for i := 0; i < dupRun; i++ {
						os.Remove(pagePath(outDir, page-i))
					}
					return page - dupRun, nil
				}
			} else {
				dupRun = 0 // ページが変わったのでリセット
			}
		}
		prev = h

		// 進捗通知
		m.mu.Lock()
		m.currentPage = page
		m.message = fmt.Sprintf("ページ %d を保存中...", page)
		m.mu.Unlock()
		m.addLog("info", fmt.Sprintf("ページ %d を保存", page))

		// 次のページへ（描画待機は次ループの captureSettled が適応的に行う。固定sleepはしない）
		if terr := kindle.TurnPage(direction); terr != nil {
			return page, terr
		}
	}
}

// captureSettled は「前ページと変わった」かつ「直前のポーリングと同じ（＝描画が止まって
// 安定した）」と判定できるまでスクショを撮り続け、採用した1枚のハッシュを返す（適応待機）。
// 保存先は path 固定で、撮るたびに上書きするため、最終的に採用された1枚だけが path に残る。
//
// settleTimeout に達すると、その時点の最新の1枚をそのまま採用として返す。描画が安定しない
// まま（＝最終ページに到達してページ送りができなくなった等の理由で）タイムアウトした場合、
// 返り値の h は prevPageHash と同一になりうるが、これは呼び出し元 captureLoop の重複判定
// （dupRun）が検知して自動停止する設計なので、ここでは何もせずタイムアウト時点の1枚を返す。
//
// ループの毎回の反復で停止要求（m.shouldStopNow）をチェックするため、settleTimeout の間
// ずっと無反応になることはない（最悪でも DefaultPollInterval + スクショ1回分の時間で反応する）。
func (m *Manager) captureSettled(path string, prevPageHash dedupe.Hash, settleTimeout time.Duration) (h dedupe.Hash, stopped bool, err error) {
	deadline := time.Now().Add(settleTimeout)
	var lastPoll dedupe.Hash

	for {
		if m.shouldStopNow() {
			return dedupe.Hash{}, true, nil
		}

		if serr := capture.Screenshot(path); serr != nil {
			return dedupe.Hash{}, false, serr
		}
		current, herr := dedupe.HashFile(path)
		if herr != nil {
			return dedupe.Hash{}, false, herr
		}

		if shouldAccept(current, prevPageHash, lastPoll, DefaultDuplicateThreshold, DefaultStableThreshold) {
			return current, false, nil
		}
		lastPoll = current

		if time.Now().After(deadline) {
			return current, false, nil
		}
		time.Sleep(DefaultPollInterval)
	}
}

// shouldAccept は適応待機の採用判定ロジック（Kindle操作やスクショ撮影に依存しない純粋関数。
// ユニットテストは internal/session/worker_test.go）。
//
//   - current        : 直近のポーリングで撮ったスクショのハッシュ
//   - prevPageHash   : 前ページとして採用済みのハッシュ（1ページ目は未計算＝IsZero）
//   - lastPoll       : このページに対する直前のポーリングのハッシュ（1回目のポーリングでは未計算＝IsZero）
//   - dupThreshold   : このハミング距離を超えたら「前ページと変わった」とみなす閾値
//   - stableThreshold: このハミング距離以下なら「直前のポーリングと同じ＝描画が安定した」とみなす閾値
//
// 「前ページと変わった」かつ「直前のポーリングと同じ」の両方を満たしたときだけ true を返す。
// 1ページ目など prevPageHash が未計算の場合は「変わった」を無条件 true として扱い、安定判定
// だけで採否を決める。lastPoll が未計算（このページの1回目のポーリング）の場合は、比較対象が
// ないため必ず false（不採用）を返す。
func shouldAccept(current, prevPageHash, lastPoll dedupe.Hash, dupThreshold, stableThreshold int) bool {
	changed := prevPageHash.IsZero()
	if !changed {
		if dist, derr := current.Distance(prevPageHash); derr == nil {
			changed = dist > dupThreshold
		}
	}
	if !changed {
		return false
	}

	if lastPoll.IsZero() {
		return false
	}
	dist, derr := current.Distance(lastPoll)
	return derr == nil && dist <= stableThreshold
}

// pagePath はページ番号から連番ファイルパスを作る（例: output/本/page_0001.png）。
func pagePath(outDir string, page int) string {
	return filepath.Join(outDir, fmt.Sprintf("page_%04d.png", page))
}
