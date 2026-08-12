// Package output は出力フォルダの作成・命名を担当する。
// Python 版の src/utils.py と同じ仕様（連番フォルダで上書き防止）。
package output

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SanitizeBookName は本の名前をフォルダ名に使える形へ変換する。
func SanitizeBookName(name string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	return r.Replace(name)
}

// CreateUniqueDir は root/本の名前 のフォルダを作成して返す。
// 同名フォルダに既存ファイルがある場合は 本の名前_2, _3... と連番を付け、
// 上書き・混在を防ぐ。
func CreateUniqueDir(root, bookName string) (string, error) {
	safe := SanitizeBookName(bookName)
	dir := filepath.Join(root, safe)

	for n := 2; hasFiles(dir); n++ {
		dir = filepath.Join(root, fmt.Sprintf("%s_%d", safe, n))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("出力フォルダの作成に失敗しました: %w", err)
	}
	return dir, nil
}

// hasFiles はディレクトリが存在し、かつ中身が1つ以上あるかを返す。
func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false // 存在しない・読めない場合は「中身なし」扱い
	}
	return len(entries) > 0
}
