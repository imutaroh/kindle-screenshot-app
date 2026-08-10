package session

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/imutaakihiro/kindle-screenshot-go/internal/capture"
	"github.com/imutaakihiro/kindle-screenshot-go/internal/dedupe"
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
		// 停止チェック（Web UI からの停止ボタン）
		if m.shouldStopNow() {
			return page - 1, "", false, nil
		}

		// ホットコーナー判定（左上=PDF化終了 / 右上=キャンセル）
		switch checkCornerAction() {
		case "finish":
			return page - 1, "", false, nil
		case "cancel":
			return page - 1, fmt.Sprintf("ユーザーがキャンセルしました（%dページまで保存済み）", page-1), true, nil
		}

		// 最大ページ数チェック
		if maxPages > 0 && page > maxPages {
			return page - 1, "", false, nil
		}

		// ページキャプチャ
		path := pagePath(outDir, page)
		if serr := capture.Screenshot(path); serr != nil {
			return page - 1, "", false, serr
		}

		h, herr := dedupe.HashFile(path)
		if herr != nil {
			return page - 1, "", false, herr
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

		// 進捗通知（web/static/script.js が /^ページ \d+ を保存中/ で判別するため書式厳守）
		m.mu.Lock()
		m.currentPage = page
		m.message = fmt.Sprintf("ページ %d を保存中...", page)
		m.mu.Unlock()

		// 次のページへ
		if terr := kindle.TurnPage(direction); terr != nil {
			return page, "", false, terr
		}

		// 最初の数ページは Kindle の読み込みが遅いので長めに待つ
		wait := time.Duration(DefaultNormalPageWaitTime)
		if page <= DefaultInitialPagesCount {
			wait = DefaultInitialPageWaitTime
		}
		time.Sleep(wait)
	}
}

// checkCornerAction はマウス位置をチェックして対応するホットコーナーアクションを返す。
//
//	"finish": 左上角 → 取得済み分でPDF化して正常終了
//	"cancel": 右上角 → キャンセル（PDF化しない）
//	""      : それ以外、または座標取得に失敗した場合（安全側に倒して無視する。Python版の
//	          check_corner_action が例外を握りつぶして None を返すのと同じ挙動）
//
// capture.MousePosition / capture.ScreenSize は NSScreen.mainScreen（システム環境設定上の
// 「メインディスプレイ」）を基準にした座標を返す。マルチディスプレイ環境で Kindle を
// 副ディスプレイ側に出している場合、実際にカーソルがある物理ディスプレイと mainScreen が
// 一致しないことがあり、角判定がずれる既知の限界がある（internal/capture のコメント参照）。
func checkCornerAction() string {
	x, y, err := capture.MousePosition()
	if err != nil {
		return ""
	}
	// h（画面高さ）は右上・左上どちらの判定にも不要（Python版 check_corner_action も同様）だが、
	// 将来の下端コーナー対応に備えて ScreenSize の戻り値はそのまま受け取っておく。
	w, _, err := capture.ScreenSize()
	if err != nil {
		return ""
	}
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
