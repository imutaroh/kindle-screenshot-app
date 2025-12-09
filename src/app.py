#!/usr/bin/env python3
"""
Kindle 全ページスクリーンショット自動取得システム
ターミナル版メインスクリプト
"""

import sys
import time
from pathlib import Path

from src import config
from src.screenshot import capture_all_pages
from src.pdf_generator import create_pdfs_from_directory, delete_png_files


def get_book_name() -> str:
    """
    本の名前をユーザーから取得する
    
    Returns:
        str: 本の名前
    """
    print("\n" + "=" * 50)
    print("📚 Kindle 自動スクショアプリ")
    print("=" * 50)
    
    while True:
        book_name = input("\nフォルダの名前はどうしますか？（本の名前）: ").strip()
        if book_name:
            return book_name
        print("❌ 名前を入力してください。")


def create_output_directory(book_name: str) -> Path:
    """
    出力ディレクトリを作成する
    
    Args:
        book_name: 本の名前
    
    Returns:
        Path: 作成したディレクトリのパス
    """
    # ファイル名に使えない文字を置換
    safe_name = book_name.replace("/", "_").replace("\\", "_").replace(":", "_")
    
    output_dir = Path(config.OUTPUT_DIR) / safe_name
    output_dir.mkdir(parents=True, exist_ok=True)
    
    print(f"\n📁 保存先: {output_dir.absolute()}")
    return output_dir


def get_settings() -> dict:
    """
    設定値を取得する（変更可能）
    
    Returns:
        dict: 設定値
    """
    print("\n" + "-" * 50)
    print("⚙️  現在の設定:")
    print(f"  - ページ待機時間: {config.PAGE_WAIT_TIME}秒")
    print(f"  - 開始待機時間: {config.START_DELAY}秒")
    print(f"  - PDF結合枚数: {config.PDF_PAGES_PER_FILE}枚/ファイル")
    print(f"  - 最大ページ数: {'無制限' if config.MAX_PAGES == 0 else config.MAX_PAGES}")
    print("-" * 50)
    
    # 最大ページ数の入力
    max_pages_input = input("\n最大ページ数を入力（Enterで無制限）: ").strip()
    max_pages = int(max_pages_input) if max_pages_input.isdigit() else 0
    
    # PDF結合枚数の入力
    pdf_pages_input = input(f"PDF結合枚数を入力（Enterで{config.PDF_PAGES_PER_FILE}枚）: ").strip()
    pdf_pages = int(pdf_pages_input) if pdf_pages_input.isdigit() else config.PDF_PAGES_PER_FILE
    
    return {
        "max_pages": max_pages,
        "pdf_pages_per_file": pdf_pages,
    }


def countdown(seconds: int) -> None:
    """
    カウントダウンを表示する
    
    Args:
        seconds: カウントダウン秒数
    """
    print(f"\n⏱️  {seconds}秒後に開始します。Kindleをフルスクリーンで表示してください...")
    
    for i in range(seconds, 0, -1):
        print(f"  {i}...", end=" ", flush=True)
        time.sleep(1)
    
    print("\n🚀 開始！\n")


def main():
    """
    メイン処理
    """
    try:
        # 1. 本の名前を取得
        book_name = get_book_name()
        
        # 2. 出力ディレクトリを作成
        output_dir = create_output_directory(book_name)
        
        # 3. 設定を取得
        settings = get_settings()
        
        # 4. カウントダウン
        countdown(config.START_DELAY)
        
        # 5. スクリーンショット取得開始
        print("📸 スクリーンショット取得中...")
        print("-" * 50)
        
        captured_pages = capture_all_pages(
            output_dir=output_dir,
            max_pages=settings["max_pages"]
        )
        
        print("-" * 50)
        print(f"✅ {captured_pages}ページのスクリーンショットを保存しました。")
        
        if captured_pages == 0:
            print("スクリーンショットが取得できませんでした。")
            return
        
        # 6. PDF結合
        print("\n📄 PDF結合中...")
        created_pdfs = create_pdfs_from_directory(
            directory=output_dir,
            book_name=book_name,
            pages_per_pdf=settings["pdf_pages_per_file"]
        )
        
        if not created_pdfs:
            print("❌ PDF作成に失敗しました。PNGファイルは保持されます。")
            return
        
        print(f"✅ {len(created_pdfs)}個のPDFファイルを作成しました。")
        
        # 7. PNG削除確認
        print("\n" + "-" * 50)
        delete_confirm = input("PNGファイルを削除しますか？ (y/N): ").strip().lower()
        
        if delete_confirm == 'y':
            deleted_count = delete_png_files(output_dir)
            print(f"🗑️  {deleted_count}個のPNGファイルを削除しました。")
        else:
            print("PNGファイルは保持されます。")
        
        # 8. 完了
        print("\n" + "=" * 50)
        print("🎉 処理完了！")
        print(f"📁 出力先: {output_dir.absolute()}")
        print("=" * 50)
        
    except KeyboardInterrupt:
        print("\n\n⚠️  中断されました。")
        sys.exit(1)
    except Exception as e:
        print(f"\n❌ エラーが発生しました: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()

