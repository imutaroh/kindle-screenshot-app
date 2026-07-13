// Package dedupe は pHash（知覚ハッシュ）で「ほぼ同じページか」を判定する。
//
// 最終ページに到達するとページ送りが効かなくなり同じ画面が続くので、
// これを検出して自動停止に使う。完全一致（MD5等）ではなく pHash を使うのは、
// 時計やメニューバー等の微差を吸収するため。
package dedupe

import (
	"errors"
	"fmt"
	"image/png"
	"os"

	"github.com/corona10/goimagehash"
)

// Hash は画像1枚分の pHash を包むゼロ値安全な型。
// var h Hash のままなら「未計算」を表す。
type Hash struct {
	h *goimagehash.ImageHash
}

// IsZero はハッシュが未計算かどうかを返す。
func (a Hash) IsZero() bool { return a.h == nil }

// HashFile は PNG ファイルを読み込んで pHash を計算する。
func HashFile(path string) (Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return Hash{}, err
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		return Hash{}, fmt.Errorf("PNGデコード失敗 %s: %w", path, err)
	}

	h, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return Hash{}, err
	}
	return Hash{h: h}, nil
}

// Distance は2つのハッシュのハミング距離を返す（小さいほど似ている。0=ほぼ同一）。
func (a Hash) Distance(b Hash) (int, error) {
	if a.IsZero() || b.IsZero() {
		return 0, errors.New("未計算のハッシュ同士は比較できません")
	}
	return a.h.Distance(b.h)
}
