package ocr

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// swiftSource はヘルパーの Swift ソース。go:embed でバイナリに埋め込むので、
// kindlesnap 単体を配っても初回実行時にその場でビルドできる。
//
//go:embed visionocr.swift
var swiftSource []byte

// minMacOSMajor は RecognizeDocumentsRequest が使える macOS のメジャーバージョン。
const minMacOSMajor = 26

// SetupError は OCR を使う準備ができていないことを表すエラー。
// Fix に利用者が実行すべき対処（コマンドなど）を入れ、doctor がそのまま表示できるようにする。
type SetupError struct {
	Msg string
	Fix string
}

func (e *SetupError) Error() string {
	if e.Fix == "" {
		return e.Msg
	}
	return e.Msg + "（対処: " + e.Fix + "）"
}

// helperName はキャッシュに置く実行ファイル名。ソースの sha256 の先頭12桁を付けるので、
// ソースを変えた新しい kindlesnap は自動的に別名で再ビルドし、古いものを使い回さない。
func helperName() string {
	sum := sha256.Sum256(swiftSource)
	return "visionocr-" + hex.EncodeToString(sum[:])[:12]
}

// cacheDir はヘルパーを置くディレクトリ（~/Library/Caches/kindlesnap）。
func cacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "kindlesnap"), nil
}

// buildMu は同じプロセス内で ensureBuilt が同時に呼ばれたときにビルドを1回に絞る。
// 別プロセス同士の競合は、一時ファイルにビルドしてから rename する方式で安全にしている。
var (
	buildMu   sync.Mutex
	builtPath string
)

// ensureBuilt はヘルパーの実行ファイルのパスを返す。キャッシュに無ければビルドする。
func ensureBuilt(ctx context.Context) (string, error) {
	buildMu.Lock()
	defer buildMu.Unlock()
	if builtPath != "" {
		return builtPath, nil
	}

	dir, err := cacheDir()
	if err != nil {
		return "", fmt.Errorf("キャッシュフォルダが分かりません: %w", err)
	}
	bin := filepath.Join(dir, helperName())
	if info, err := os.Stat(bin); err == nil && info.Mode().IsRegular() {
		builtPath = bin
		return bin, nil
	}

	if err := checkSwiftc(ctx); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("キャッシュフォルダを作れません: %w", err)
	}

	// 一時フォルダにソースと出力を置き、成功したら最終パスへ rename する。
	// rename は同じファイルシステム内なら原子的なので、別の kindlesnap が同時に
	// ビルドしていても、他方が書きかけのバイナリを実行することはない（後勝ちで上書きされるだけ）。
	tmp, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return "", fmt.Errorf("ビルド用の一時フォルダを作れません: %w", err)
	}
	defer os.RemoveAll(tmp)

	src := filepath.Join(tmp, "visionocr.swift")
	if err := os.WriteFile(src, swiftSource, 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(tmp, "visionocr")
	cmd := exec.CommandContext(ctx, "swiftc", "-O", "-parse-as-library",
		"-target", swiftTarget(), "-o", out, src)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", &SetupError{
			Msg: fmt.Sprintf("OCR ヘルパーのビルドに失敗しました: %v: %s", err, tail(string(msg), 800)),
			Fix: "xcode-select --install で Command Line Tools を入れ直し、macOS 26 以降であることを確認する",
		}
	}
	if err := os.Rename(out, bin); err != nil {
		return "", fmt.Errorf("OCR ヘルパーを配置できません: %w", err)
	}
	builtPath = bin
	return bin, nil
}

// swiftTarget は swiftc の -target。Apple Silicon と Intel の両方で動くよう実行中の
// アーキテクチャに合わせる（Go 側と同じアーキテクチャのヘルパーを作る）。
func swiftTarget() string {
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "x86_64"
	}
	return fmt.Sprintf("%s-apple-macos%d.0", arch, minMacOSMajor)
}

// checkSwiftc は swiftc が実際に動くかを確かめる。
// Command Line Tools が無くても /usr/bin/swiftc はスタブとして存在し、LookPath は
// 成功してしまう。そのため実際に -version を実行して終了コードで判定する。
func checkSwiftc(ctx context.Context) error {
	notFound := &SetupError{
		Msg: "swiftc が使えません（OCR ヘルパーのビルドに必要）",
		Fix: "xcode-select --install",
	}
	if _, err := exec.LookPath("swiftc"); err != nil {
		return notFound
	}
	if out, err := exec.CommandContext(ctx, "swiftc", "-version").CombinedOutput(); err != nil {
		notFound.Msg += ": " + tail(string(out), 300)
		return notFound
	}
	return nil
}

// Check は OCR を使えるかを確かめる（doctor 用）。
// macOS のバージョン → swiftc → ヘルパーのビルド（未ビルドなら数秒かかる）の順に調べ、
// 最初に見つかった問題を返す。問題が無ければ nil。
// 準備不足が原因のエラーは *SetupError で、Fix に対処が入っている（errors.As で取り出せる）。
func Check() error {
	return CheckContext(context.Background())
}

// CheckContext は Check と同じだが、ctx のキャンセルでビルドなどを中断できる。
func CheckContext(ctx context.Context) error {
	ver, err := macOSVersion(ctx)
	if err != nil {
		return err
	}
	major, err := majorVersion(ver)
	if err != nil {
		return err
	}
	if major < minMacOSMajor {
		return &SetupError{
			Msg: fmt.Sprintf("macOS %s では OCR が使えません（macOS %d 以降が必要）", ver, minMacOSMajor),
			Fix: fmt.Sprintf("macOS %d 以降にアップデートする", minMacOSMajor),
		}
	}
	_, err = ensureBuilt(ctx)
	return err
}

// macOSVersion は sw_vers -productVersion の出力（例: "26.6.2"）を返す。
func macOSVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
	if err != nil {
		return "", fmt.Errorf("macOS のバージョンを取得できません: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// majorVersion は "26.6.2" のようなバージョン文字列からメジャー番号（26）を取り出す。
func majorVersion(v string) (int, error) {
	head, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, errors.New("macOS のバージョンを解釈できません: " + v)
	}
	return n, nil
}
