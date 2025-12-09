"""
PDF結合・PNG削除処理モジュール
"""

import img2pdf
from pathlib import Path
from typing import List, Optional, Callable
import os

from src import config


def get_png_files(directory: Path) -> List[Path]:
    """
    ディレクトリ内のPNGファイルを連番順で取得する
    
    Args:
        directory: 検索対象ディレクトリ
    
    Returns:
        List[Path]: PNGファイルのパスリスト（ソート済み）
    """
    png_files = list(directory.glob(f"{config.IMAGE_PREFIX}*{config.IMAGE_EXTENSION}"))
    # ファイル名でソート（連番順になる）
    png_files.sort(key=lambda x: x.name)
    return png_files


def create_pdf_from_images(
    image_paths: List[Path],
    output_path: Path
) -> bool:
    """
    画像ファイルからPDFを生成する
    
    Args:
        image_paths: 画像ファイルのパスリスト
        output_path: 出力PDFのパス
    
    Returns:
        bool: 成功したかどうか
    """
    try:
        # img2pdfで画像をPDFに変換
        with open(output_path, "wb") as f:
            f.write(img2pdf.convert([str(p) for p in image_paths]))
        return True
    except Exception as e:
        print(f"PDF生成エラー: {e}")
        return False


def generate_pdf_filename(book_name: str, part_number: int) -> str:
    """
    PDFファイル名を生成する
    
    Args:
        book_name: 本の名前
        part_number: パート番号（1から開始）
    
    Returns:
        str: PDFファイル名（例: bookname_part1.pdf）
    """
    # ファイル名に使えない文字を置換
    safe_name = book_name.replace("/", "_").replace("\\", "_").replace(":", "_")
    return f"{safe_name}_part{part_number}.pdf"


def create_pdfs_from_directory(
    directory: Path,
    book_name: str,
    pages_per_pdf: int = None,
    on_progress: Optional[Callable[[int, str], None]] = None
) -> List[Path]:
    """
    ディレクトリ内の画像を指定枚数ごとにPDFに結合する
    
    Args:
        directory: 画像が保存されているディレクトリ
        book_name: 本の名前（PDFファイル名に使用）
        pages_per_pdf: 1つのPDFに含める画像枚数（Noneの場合はconfig値を使用）
        on_progress: 進捗コールバック関数 (part_number, filepath) -> None
    
    Returns:
        List[Path]: 生成されたPDFファイルのパスリスト
    """
    if pages_per_pdf is None:
        pages_per_pdf = config.PDF_PAGES_PER_FILE
    
    # PNGファイル取得
    png_files = get_png_files(directory)
    
    if not png_files:
        print("PNGファイルが見つかりません。")
        return []
    
    print(f"\n{len(png_files)}枚の画像をPDFに結合します...")
    
    created_pdfs = []
    part_number = 1
    
    # 指定枚数ごとに分割してPDF生成
    for i in range(0, len(png_files), pages_per_pdf):
        batch = png_files[i:i + pages_per_pdf]
        
        # PDFファイル名生成
        pdf_filename = generate_pdf_filename(book_name, part_number)
        pdf_path = directory / pdf_filename
        
        # PDF生成
        success = create_pdf_from_images(batch, pdf_path)
        
        if success:
            created_pdfs.append(pdf_path)
            
            if on_progress:
                on_progress(part_number, str(pdf_path))
            else:
                print(f"[Part {part_number}] PDF作成完了: {pdf_path} ({len(batch)}ページ)")
        else:
            print(f"[Part {part_number}] PDF作成失敗")
        
        part_number += 1
    
    return created_pdfs


def delete_png_files(
    directory: Path,
    on_progress: Optional[Callable[[str], None]] = None
) -> int:
    """
    ディレクトリ内のPNGファイルを削除する
    
    Args:
        directory: 対象ディレクトリ
        on_progress: 進捗コールバック関数 (filepath) -> None
    
    Returns:
        int: 削除したファイル数
    """
    png_files = get_png_files(directory)
    deleted_count = 0
    
    for filepath in png_files:
        try:
            os.remove(filepath)
            deleted_count += 1
            
            if on_progress:
                on_progress(str(filepath))
        except Exception as e:
            print(f"削除エラー: {filepath} - {e}")
    
    return deleted_count

