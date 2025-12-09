"""
スクリーンショット取得・ページ送り処理モジュール
"""

import pyautogui
import time
from pathlib import Path
from typing import Optional, Callable
import subprocess
import hashlib

from src import config

# pyautoguiの安全設定
pyautogui.FAILSAFE = True  # マウスを左上角に移動すると緊急停止


def take_screenshot() -> "pyautogui.Image":
    """
    メインディスプレイのスクリーンショットを取得する
    （2画面環境でもメインディスプレイのみを取得）
    
    Returns:
        PIL.Image: スクリーンショット画像
    """
    # メインディスプレイのサイズを取得
    screen_width, screen_height = pyautogui.size()
    
    # メインディスプレイの領域のみをスクリーンショット
    # (0, 0)からメインディスプレイのサイズまで
    return pyautogui.screenshot(region=(0, 0, screen_width, screen_height))


def save_screenshot(image: "pyautogui.Image", filepath: Path) -> None:
    """
    スクリーンショットをファイルに保存する
    
    Args:
        image: 保存する画像
        filepath: 保存先パス
    """
    image.save(str(filepath))


def activate_kindle_app() -> None:
    """
    Kindleアプリをアクティブにする（macOS用）
    """
    try:
        # AppleScriptでKindleアプリをアクティブにする
        script = '''
        tell application "Kindle"
            activate
        end tell
        '''
        subprocess.run(['osascript', '-e', script], check=False, capture_output=True)
        time.sleep(0.5)  # アプリがアクティブになるまで少し待つ
    except Exception as e:
        print(f"警告: Kindleアプリのアクティブ化に失敗しました: {e}")
        # 失敗しても続行


def press_right_arrow() -> None:
    """
    右矢印キーを押してページを送る
    Kindleアプリをアクティブにしてからキーを送信
    """
    # Kindleアプリをアクティブにする
    activate_kindle_app()
    
    # より確実なキー送信方法を使用
    # keyDownとkeyUpを明示的に使用
    pyautogui.keyDown('right')
    time.sleep(0.1)  # キーが確実に押されるように短い待機
    pyautogui.keyUp('right')


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


def get_image_hash(image: "pyautogui.Image") -> str:
    """
    画像のハッシュ値を取得する（同じページかどうかの判定用）
    
    Args:
        image: 画像オブジェクト
    
    Returns:
        str: 画像のハッシュ値
    """
    # 画像をバイト列に変換してハッシュ化
    import io
    img_bytes = io.BytesIO()
    image.save(img_bytes, format='PNG')
    img_bytes.seek(0)
    return hashlib.md5(img_bytes.read()).hexdigest()


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
    should_stop: Optional[Callable[[], bool]] = None
) -> int:
    """
    全ページをキャプチャする
    
    Args:
        output_dir: 保存先ディレクトリ
        max_pages: 最大ページ数（0 = 無制限）
        on_progress: 進捗コールバック関数 (page_number, filepath) -> None
        should_stop: 停止判定コールバック関数 () -> bool
    
    Returns:
        int: キャプチャしたページ数
    """
    page_number = 1
    max_pages = max_pages or config.MAX_PAGES
    previous_hash = None
    duplicate_count = 0
    
    while True:
        # 停止チェック
        if should_stop and should_stop():
            print(f"\n停止が要求されました。{page_number - 1}ページまで保存しました。")
            break
        
        # 最大ページ数チェック
        if max_pages > 0 and page_number > max_pages:
            print(f"\n最大ページ数 {max_pages} に到達しました。")
            break
        
        # ページキャプチャ
        filepath, image_hash = capture_page(output_dir, page_number)
        
        # 同じページかどうかをチェック（自動停止機能）
        if config.AUTO_STOP_ON_DUPLICATE and previous_hash is not None:
            if image_hash == previous_hash:
                duplicate_count += 1
                if duplicate_count >= config.DUPLICATE_CHECK_COUNT:
                    print(f"\n同じページが{duplicate_count}回連続で保存されました。")
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
        press_right_arrow()
        # ページ番号を渡して適切な待機時間を使用
        wait_for_page_load(page_number=page_number)
        
        page_number += 1
    
    return page_number - 1

