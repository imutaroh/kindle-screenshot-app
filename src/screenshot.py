"""
スクリーンショット取得・ページ送り処理モジュール
"""

import pyautogui
import imagehash
import time
import os
import tempfile
from pathlib import Path
from typing import Optional, Callable
import subprocess

from PIL import Image

from src import config

# pyautoguiの FAILSAFE は無効化（左上角は「PDF化終了」として自前実装するため）
pyautogui.FAILSAFE = False


class CaptureCancelled(Exception):
    """ユーザーがキャプチャをキャンセルしたことを示す例外（右上ホットコーナー）"""
    pass


def take_screenshot() -> "Image.Image":
    """
    メインディスプレイのスクリーンショットを Retina ネイティブ解像度で取得する
    （2画面環境でもメインディスプレイのみを取得）

    pyautogui.screenshot() は論理解像度（Retina実解像度の約1/2）でしか取得できず
    OCR・AI読解の精度を落とすため、macOS 標準の screencapture を直接使う。

    Returns:
        PIL.Image: スクリーンショット画像
    """
    fd, tmp_path = tempfile.mkstemp(suffix=".png")
    os.close(fd)
    try:
        # -x: シャッター音なし / -m: メインディスプレイのみ
        subprocess.run(
            ["screencapture", "-x", "-m", tmp_path],
            check=True, timeout=10,
        )
        img = Image.open(tmp_path)
        img.load()  # ファイル削除前に全データをメモリへ読み込む
        if img.mode != "RGB":
            # screencapture はRGBAを返すが、img2pdf はアルファチャンネルを拒否する
            img = img.convert("RGB")
        return img
    finally:
        Path(tmp_path).unlink(missing_ok=True)


def save_screenshot(image: "pyautogui.Image", filepath: Path) -> None:
    """
    スクリーンショットをファイルに保存する
    
    Args:
        image: 保存する画像
        filepath: 保存先パス
    """
    image.save(str(filepath))


# アクティブ化に成功したKindleアプリ名のキャッシュ（毎ページ呼ばれるため）
_kindle_app_name: Optional[str] = None


def activate_kindle_app() -> bool:
    """
    Kindleアプリをアクティブにする（macOS用）。
    アプリ名は世代によって "Kindle" / "Amazon Kindle" があるため両方試す。

    Returns:
        bool: アクティブ化に成功したかどうか
    """
    global _kindle_app_name
    candidates = [_kindle_app_name] if _kindle_app_name else ["Kindle", "Amazon Kindle"]

    for app_name in candidates:
        try:
            script = f'tell application "{app_name}" to activate'
            result = subprocess.run(
                ['osascript', '-e', script],
                check=False, capture_output=True, timeout=5,
            )
            if result.returncode == 0:
                _kindle_app_name = app_name
                time.sleep(0.5)  # アプリがアクティブになるまで少し待つ
                return True
        except Exception as e:
            print(f"警告: Kindleアプリのアクティブ化に失敗しました: {e}")

    return False


def press_page_arrow(direction: str = "left") -> None:
    """
    指定方向の矢印キーを押してページを送る
    Kindleアプリをアクティブにしてからキーを送信

    Args:
        direction: "left"（日本語の本・右綴じ） or "right"（英語の本・左綴じ）
    """
    # Kindleアプリをアクティブにする
    activate_kindle_app()

    key = "left" if direction == "left" else "right"

    # より確実なキー送信方法を使用
    # keyDownとkeyUpを明示的に使用
    pyautogui.keyDown(key)
    time.sleep(0.1)  # キーが確実に押されるように短い待機
    pyautogui.keyUp(key)


def wait_for_page_load(page_number: int = None, seconds: float = None) -> None:
    """
    ページ読み込み完了を待つ
    
    Args:
        page_number: 現在のページ番号（Noneの場合は通常の待機時間を使用）
        seconds: 待機秒数（指定された場合はこの値を使用、最優先）
    """
    if seconds is not None:
        # 明示的に指定された場合はその値を使用
        time.sleep(seconds)
    elif page_number is not None and page_number <= config.INITIAL_PAGES_COUNT:
        # 最初のNページは長めに待機
        time.sleep(config.INITIAL_PAGE_WAIT_TIME)
    elif page_number is not None:
        # 通常のページは短めに待機
        time.sleep(config.NORMAL_PAGE_WAIT_TIME)
    else:
        # ページ番号が指定されていない場合は従来の動作
        time.sleep(config.PAGE_WAIT_TIME)


def generate_filename(page_number: int) -> str:
    """
    ページ番号から連番ファイル名を生成する
    
    Args:
        page_number: ページ番号（1から開始）
    
    Returns:
        str: ファイル名（例: page_0001.png）
    """
    return f"{config.IMAGE_PREFIX}{page_number:04d}{config.IMAGE_EXTENSION}"


def get_image_hash(image: "pyautogui.Image") -> imagehash.ImageHash:
    """
    画像の知覚ハッシュ（pHash）を取得する。
    完全一致のMD5と違い、時計・マウスカーソル等の微差を吸収して
    「ほぼ同じページか」を判定できる。

    Args:
        image: 画像オブジェクト

    Returns:
        imagehash.ImageHash: pHash オブジェクト（引き算でハミング距離が取れる）
    """
    return imagehash.phash(image)


def check_corner_action() -> Optional[str]:
    """
    マウスがスクリーン四隅にあるかチェックして、対応するアクションを返す。

    Returns:
        "finish": 左上角 → PDF化して正常終了
        "cancel": 右上角 → キャンセル（PDF化しない）
        None: それ以外
    """
    if not config.CORNER_ACTION_ENABLED:
        return None

    try:
        x, y = pyautogui.position()
        w, h = pyautogui.size()
    except Exception:
        return None

    threshold = config.CORNER_THRESHOLD_PX
    if x <= threshold and y <= threshold:
        return "finish"
    if x >= w - threshold and y <= threshold:
        return "cancel"
    return None


def capture_page(output_dir: Path, page_number: int) -> tuple[Path, str]:
    """
    1ページをキャプチャして保存する
    
    Args:
        output_dir: 保存先ディレクトリ
        page_number: ページ番号
    
    Returns:
        tuple[Path, str]: (保存したファイルのパス, 画像のハッシュ値)
    """
    # スクリーンショット取得
    image = take_screenshot()
    
    # 画像のハッシュ値を取得
    image_hash = get_image_hash(image)
    
    # ファイル名生成
    filename = generate_filename(page_number)
    filepath = output_dir / filename
    
    # 保存
    save_screenshot(image, filepath)
    
    return filepath, image_hash


def capture_all_pages(
    output_dir: Path,
    max_pages: int = 0,
    on_progress: Optional[Callable[[int, str], None]] = None,
    should_stop: Optional[Callable[[], bool]] = None,
    direction: Optional[str] = None,
) -> int:
    """
    全ページをキャプチャする

    Args:
        output_dir: 保存先ディレクトリ
        max_pages: 最大ページ数（0 = 無制限）
        on_progress: 進捗コールバック関数 (page_number, filepath) -> None
        should_stop: 停止判定コールバック関数 () -> bool
        direction: ページめくり方向 "left" or "right"（None なら config.PAGE_DIRECTION を使用）

    Returns:
        int: キャプチャしたページ数
    """
    page_number = 1
    max_pages = max_pages or config.MAX_PAGES
    page_direction = direction or config.PAGE_DIRECTION
    previous_hash = None
    duplicate_count = 0
    
    while True:
        # 停止チェック（Web UI からの停止ボタン）
        if should_stop and should_stop():
            print(f"\n停止が要求されました。{page_number - 1}ページまで保存しました。")
            break

        # ホットコーナー判定（左上=PDF化終了 / 右上=キャンセル）
        corner = check_corner_action()
        if corner == "finish":
            print(f"\n🖱️  左上ホットコーナー検出 → {page_number - 1}ページでPDF化終了します。")
            break
        if corner == "cancel":
            print(f"\n🖱️  右上ホットコーナー検出 → キャンセル（PDF化せず終了）")
            raise CaptureCancelled(f"ユーザーがキャンセルしました（{page_number - 1}ページまで保存済み）")

        # 最大ページ数チェック
        if max_pages > 0 and page_number > max_pages:
            print(f"\n最大ページ数 {max_pages} に到達しました。")
            break

        # ページキャプチャ
        filepath, image_hash = capture_page(output_dir, page_number)

        # 同じページかどうかをチェック（pHash ハミング距離による類似度判定）
        if config.AUTO_STOP_ON_DUPLICATE and previous_hash is not None:
            distance = image_hash - previous_hash
            if distance <= config.DUPLICATE_HASH_THRESHOLD:
                duplicate_count += 1
                if duplicate_count >= config.DUPLICATE_CHECK_COUNT:
                    print(f"\n同じページが{duplicate_count}回連続で保存されました（pHash距離≦{config.DUPLICATE_HASH_THRESHOLD}）。")
                    print(f"最後のページに到達したと判断して停止します。")
                    print(f"合計 {page_number - duplicate_count} ページを保存しました。")
                    # 重複したページを削除
                    for i in range(duplicate_count):
                        dup_file = output_dir / generate_filename(page_number - i)
                        if dup_file.exists():
                            dup_file.unlink()
                    return page_number - duplicate_count
            else:
                duplicate_count = 0  # ページが変わったのでリセット

        previous_hash = image_hash
        
        # 進捗通知
        if on_progress:
            on_progress(page_number, str(filepath))
        else:
            print(f"[{page_number}] 保存完了: {filepath}")
        
        # 次のページへ
        press_page_arrow(direction=page_direction)
        # ページ番号を渡して適切な待機時間を使用
        wait_for_page_load(page_number=page_number)
        
        page_number += 1
    
    return page_number - 1

