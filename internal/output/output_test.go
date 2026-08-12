package output

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeBookName(t *testing.T) {
	// テーブル駆動テスト: 入力と期待値のペアを並べて一気に検証する Go の定番スタイル
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"そのまま", "普通の本", "普通の本"},
		{"スラッシュ", "a/b", "a_b"},
		{"バックスラッシュ", `a\b`, "a_b"},
		{"コロン", "a:b", "a_b"},
		{"複合", `a/b\c:d`, "a_b_c_d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeBookName(tt.in); got != tt.want {
				t.Errorf("SanitizeBookName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCreateUniqueDir(t *testing.T) {
	root := t.TempDir() // テスト終了時に自動削除される一時フォルダ

	// 1回目: そのままの名前
	d1, err := CreateUniqueDir(root, "テスト本")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "テスト本"); d1 != want {
		t.Errorf("1回目 = %q, want %q", d1, want)
	}

	// 空フォルダのままなら再利用される
	d2, err := CreateUniqueDir(root, "テスト本")
	if err != nil {
		t.Fatal(err)
	}
	if d2 != d1 {
		t.Errorf("空フォルダは再利用されるべき: got %q, want %q", d2, d1)
	}

	// ファイルを置くと連番フォルダになる
	if err := os.WriteFile(filepath.Join(d1, "page_0001.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d3, err := CreateUniqueDir(root, "テスト本")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "テスト本_2"); d3 != want {
		t.Errorf("連番 = %q, want %q", d3, want)
	}
}
