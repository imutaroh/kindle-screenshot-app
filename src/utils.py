"""
共通ユーティリティ
"""

from pathlib import Path

from src import config


def sanitize_book_name(book_name: str) -> str:
    """
    本の名前をファイル名に使える形に変換する

    Args:
        book_name: 本の名前

    Returns:
        str: ファイル名に使える文字列
    """
    return book_name.replace("/", "_").replace("\\", "_").replace(":", "_")


def _has_files(directory: Path) -> bool:
    """ディレクトリが存在し、かつ中身があるか"""
    return directory.exists() and any(directory.iterdir())


def create_unique_output_dir(book_name: str) -> Path:
    """
    出力ディレクトリを作成する。
    同名フォルダに既存ファイルがある場合は _2, _3... と連番を付けて
    別フォルダにする（上書き・混在を防ぐ）。

    Args:
        book_name: 本の名前

    Returns:
        Path: 作成したディレクトリのパス
    """
    safe_name = sanitize_book_name(book_name)
    output_dir = Path(config.OUTPUT_DIR) / safe_name

    n = 2
    while _has_files(output_dir):
        output_dir = Path(config.OUTPUT_DIR) / f"{safe_name}_{n}"
        n += 1

    output_dir.mkdir(parents=True, exist_ok=True)
    return output_dir
