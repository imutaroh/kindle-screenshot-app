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
// Python 版 src/screenshot.py の capture_all_pages の移植だが、以下の3点により
// cmd/kindlesnap/main.go の captureAllPages とはあえて共通化していない:
//   - Manager の状態（進捗・停止要求）を毎ページ更新する
//   - ホットコーナー判定（左上=取得済み分でPDF化して正常終了 / 右上=キャンセル）が入る
//   - 停止判定が Manager のフラグ（Web UIの停止ボタン）経由
//
// 戻り値:
//
//	captured : 保存できたページ数
//	cancelMsg: 右上ホットコーナーでキャンセルされた場合の理由文（cancelled=true のときのみ意味を持つ）
//	cancelled: 右上ホットコーナーでキャンセルされたか（true の場合 err は常に nil）
//	err      : 回復不能なエラー
func (m *Manager) captureLoop(kindle *capture.Kindle, outDir string, maxPages int, direction capture.Direction) (captured int, cancelMsg string, cancelled bool, err error) {
	var prev dedupe.Hash
	dupRun := 0 // 直前ページと同一と判定された連続回数

	for page := 1; ; page++ {
		// 停止要求・ホットコーナー判定は captureSettled がポーリングごとに毎回チェックするため、
		// ここでの重複チェックはしない（captureSettled の stopped/corner 戻り値をそのまま扱う）。

		// 最大ページ数チェック（captureSettled の関知しない captureLoop 固有の終了条件）
		if maxPages > 0 && page > maxPages {
			return page - 1, "", false, nil
		}

		// ページキャプチャ（適応待機：画面の変化を検知してから撮る。詳細は captureSettled を参照）
		settleTimeout := DefaultSettleTimeout
		if page <= DefaultInitialPagesCount {
			settleTimeout = DefaultInitialSettleTimeout
		}
		path := pagePath(outDir, page)
		h, stopped, corner, serr := m.captureSettled(path, prev, settleTimeout)
		if serr != nil {
			return page - 1, "", false, serr
		}
		if stopped {
			return page - 1, "", false, nil
		}
		switch corner {
		case "finish":
			return page - 1, "", false, nil
		case "cancel":
			return page - 1, fmt.Sprintf("ユーザーがキャンセルしました（%dページまで保存済み）", page-1), true, nil
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
					return page - dupRun, "", false, nil
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
			return page, "", false, terr
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
// ループの毎回の反復で停止要求（m.shouldStopNow）とホットコーナー判定（checkCornerAction）を
// チェックするため、settleTimeout の間ずっと無反応になることはない
// （最悪でも DefaultPollInterval + スクショ1回分の時間で反応する）。
func (m *Manager) captureSettled(path string, prevPageHash dedupe.Hash, settleTimeout time.Duration) (h dedupe.Hash, stopped bool, corner string, err error) {
	deadline := time.Now().Add(settleTimeout)
	var lastPoll dedupe.Hash

	for {
		if m.shouldStopNow() {
			return dedupe.Hash{}, true, "", nil
		}
		if action := checkCornerAction(); action != "" {
			return dedupe.Hash{}, false, action, nil
		}

		if serr := capture.Screenshot(path); serr != nil {
			return dedupe.Hash{}, false, "", serr
		}
		current, herr := dedupe.HashFile(path)
		if herr != nil {
			return dedupe.Hash{}, false, "", herr
		}

		if shouldAccept(current, prevPageHash, lastPoll, DefaultDuplicateThreshold, DefaultStableThreshold) {
			return current, false, "", nil
		}
		lastPoll = current

		if time.Now().After(deadline) {
			return current, false, "", nil
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

// checkCornerAction はマウス位置をチェックして対応するホットコーナーアクションを返す。
//
//	"finish": 左上角 → 取得済み分でPDF化して正常終了
//	"cancel": 右上角 → キャンセル（PDF化しない）
//	""      : それ以外、または座標取得に失敗した場合（安全側に倒して無視する。Python版の
//	          check_corner_action が例外を握りつぶして None を返すのと同じ挙動）
//
// capture.CornerState は NSScreen.mainScreen（システム環境設定上の「メインディスプレイ」）を
// 基準にした座標を返す。マルチディスプレイ環境で Kindle を副ディスプレイ側に出している場合、
// 実際にカーソルがある物理ディスプレイと mainScreen が一致しないことがあり、角判定がずれる
// 既知の限界がある（internal/capture のコメント参照）。
//
// captureSettled のポーリングごとに毎回呼ばれるため、マウス座標・画面サイズを osascript
// 2回に分けて取得せず、1回の呼び出しでまとめて取得する CornerState を使う
// （osascript は1回あたり実測0.11〜0.12秒かかり、2回だと約0.23秒に膨らむ）。
func checkCornerAction() string {
	x, y, w, _, err := capture.CornerState()
	if err != nil {
		return ""
	}
	// h（画面高さ）は右上・左上どちらの判定にも不要（Python版 check_corner_action も同様）だが、
	// 将来の下端コーナー対応に備えて CornerState の戻り値はそのまま受け取っておく。
	if x <= cornerThresholdPx && y <= cornerThresholdPx {
		return "finish"
	}
	if x >= w-cornerThresholdPx && y <= cornerThresholdPx {
		return "cancel"
	}
	return ""
}

// pagePath はページ番号から連番ファイルパスを作る（例: output/本/page_0001.png）。
func pagePath(outDir string, page int) string {
	return filepath.Join(outDir, fmt.Sprintf("page_%04d.png", page))
}
