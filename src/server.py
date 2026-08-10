"""
Flask Webサーバー
Web UI版のメインサーバー
"""

from flask import Flask, render_template, request, jsonify, send_from_directory, abort
import threading
import time
import re
import subprocess
from pathlib import Path
from typing import Optional, Dict
import os

from src import config
from src.screenshot import capture_all_pages, activate_kindle_app, CaptureCancelled
from src.pdf_generator import create_pdfs_from_directory, delete_png_files
from src.utils import create_unique_output_dir

# プロジェクトルートのパスを取得（src/server.pyから見て1つ上のディレクトリ）
BASE_DIR = Path(__file__).parent.parent
TEMPLATE_DIR = BASE_DIR / 'templates'
STATIC_DIR = BASE_DIR / 'static'

app = Flask(__name__, 
            template_folder=str(TEMPLATE_DIR), 
            static_folder=str(STATIC_DIR))

# グローバル状態管理
class CaptureState:
    """キャプチャ処理の状態管理"""
    def __init__(self):
        self.is_running = False
        self.should_stop = False
        self.current_page = 0
        self.total_pages = 0
        self.status = "待機中"  # 待機中 / 実行中 / 完了 / エラー
        self.message = ""
        self.output_dir: Optional[Path] = None
        self.book_name = ""
        self.error: Optional[str] = None

state = CaptureState()


def capture_worker(book_name: str, max_pages: int, pdf_pages_per_file: int, auto_delete_png: bool, direction: str):
    """
    バックグラウンドでキャプチャ処理を実行する

    Args:
        book_name: 本の名前
        max_pages: 最大ページ数
        pdf_pages_per_file: PDF結合枚数
        auto_delete_png: PNG自動削除フラグ
        direction: ページめくり方向 "left" or "right"
    """
    global state
    
    try:
        state.is_running = True
        state.should_stop = False
        state.current_page = 0
        state.total_pages = 0
        state.status = "実行中"
        state.message = "準備中..."
        state.error = None

        # 出力ディレクトリ作成（同名フォルダに既存ファイルがあれば連番で別フォルダ）
        state.output_dir = create_unique_output_dir(book_name)
        state.book_name = book_name

        # Kindle を自動で前面に出す
        kindle_activated = activate_kindle_app()

        # 開始待機（1秒ごとにカウントダウンを表示し、停止要求にも即応する）
        for remaining in range(config.START_DELAY, 0, -1):
            if state.should_stop:
                break
            if kindle_activated:
                state.message = f"Kindleを前面に表示しました。{remaining}秒後に開始します（フルスクリーン推奨）..."
            else:
                state.message = f"⚠️ Kindleを自動で前面に出せませんでした。{remaining}秒以内に手動で切り替えてください..."
            time.sleep(1)

        if state.should_stop:
            state.status = "待機中"
            state.message = "中断されました"
            state.is_running = False
            return
        
        # 進捗コールバック
        def on_progress(page_num: int, filepath: str):
            state.current_page = page_num
            state.message = f"ページ {page_num} を保存中..."
        
        # スクリーンショット取得
        state.message = "スクリーンショット取得中..."
        try:
            captured_pages = capture_all_pages(
                output_dir=state.output_dir,
                max_pages=max_pages,
                on_progress=on_progress,
                should_stop=lambda: state.should_stop,
                direction=direction,
            )
        except CaptureCancelled as e:
            # 右上ホットコーナーによるキャンセル：PDF化せず終了
            state.status = "待機中"
            state.message = f"🖱️ 右上ホットコーナーでキャンセル: {e}"
            state.is_running = False
            return

        state.total_pages = captured_pages

        if state.should_stop:
            state.status = "待機中"
            state.message = f"中断されました（{captured_pages}ページまで保存）"
            state.is_running = False
            return

        if captured_pages == 0:
            state.status = "エラー"
            state.error = "スクリーンショットが取得できませんでした"
            state.is_running = False
            return
        
        # PDF結合
        state.message = "PDF結合中..."
        created_pdfs = create_pdfs_from_directory(
            directory=state.output_dir,
            book_name=book_name,
            pages_per_pdf=pdf_pages_per_file
        )
        
        if not created_pdfs:
            state.status = "エラー"
            state.error = "PDF作成に失敗しました"
            state.is_running = False
            return
        
        # PNG削除
        if auto_delete_png:
            state.message = "PNGファイル削除中..."
            deleted_count = delete_png_files(state.output_dir)
            state.message = f"完了！{len(created_pdfs)}個のPDFを作成し、{deleted_count}個のPNGを削除しました"
        else:
            state.message = f"完了！{len(created_pdfs)}個のPDFを作成しました"
        
        state.status = "完了"
        
    except Exception as e:
        state.status = "エラー"
        state.error = str(e)
        state.message = f"エラー: {e}"
    finally:
        state.is_running = False


@app.route('/')
def index():
    """メインページ"""
    return render_template('index.html')


@app.route('/api/start', methods=['POST'])
def start_capture():
    """キャプチャ処理を開始"""
    global state
    
    if state.is_running:
        return jsonify({"success": False, "message": "既に実行中です"}), 400
    
    data = request.json
    book_name = data.get('book_name', '').strip()
    max_pages = int(data.get('max_pages', 0) or 0)
    pdf_pages_per_file = int(data.get('pdf_pages_per_file', config.PDF_PAGES_PER_FILE))
    auto_delete_png = data.get('auto_delete_png', False)
    direction = data.get('direction', config.PAGE_DIRECTION)
    if direction not in ("left", "right"):
        direction = config.PAGE_DIRECTION

    if not book_name:
        return jsonify({"success": False, "message": "本の名前を入力してください"}), 400

    # バックグラウンドスレッドで実行
    thread = threading.Thread(
        target=capture_worker,
        args=(book_name, max_pages, pdf_pages_per_file, auto_delete_png, direction),
        daemon=True
    )
    thread.start()
    
    return jsonify({"success": True, "message": "処理を開始しました"})


@app.route('/api/stop', methods=['POST'])
def stop_capture():
    """キャプチャ処理を停止"""
    global state
    
    if not state.is_running:
        return jsonify({"success": False, "message": "実行中ではありません"}), 400
    
    state.should_stop = True
    return jsonify({"success": True, "message": "停止要求を送信しました"})


@app.route('/api/status', methods=['GET'])
def get_status():
    """現在の状態を取得（ポーリング用）"""
    global state
    
    return jsonify({
        "is_running": state.is_running,
        "current_page": state.current_page,
        "total_pages": state.total_pages,
        "status": state.status,
        "message": state.message,
        "error": state.error,
        "output_dir": str(state.output_dir) if state.output_dir else None
    })


@app.route('/api/open-folder', methods=['POST'])
def open_folder():
    """出力フォルダをFinderで開く"""
    if state.output_dir and Path(state.output_dir).exists():
        subprocess.run(["open", str(state.output_dir)], check=False)
        return jsonify({"success": True})
    return jsonify({"success": False, "message": "出力フォルダがまだ作成されていません"}), 400


@app.route('/api/config', methods=['GET'])
def get_config():
    """設定値を取得"""
    return jsonify({
        "page_wait_time": config.PAGE_WAIT_TIME,
        "start_delay": config.START_DELAY,
        "pdf_pages_per_file": config.PDF_PAGES_PER_FILE,
        "max_pages": config.MAX_PAGES,
        "page_direction": config.PAGE_DIRECTION,
    })


# ==========================================================================
# 読書ビューア（PDFリーダー）関連ルート
# 表示専用。既存のキャプチャ機能・ルートには一切影響しない。
# ==========================================================================

_PART_NUM_RE = re.compile(r'part[_\-]?(\d+)', re.IGNORECASE)


def _part_sort_key(filename: str):
    """part 番号を抜き出して数値ソート用キーを作る（part10 が part2 の後に来ないように）"""
    m = _PART_NUM_RE.search(filename)
    if m:
        return (0, int(m.group(1)), filename)
    # part 番号が無いPDFはファイル名順で末尾に
    return (1, 0, filename)


@app.route('/reader')
def reader():
    """読書ビューア（PDFリーダー）ページ"""
    return render_template('reader.html')


@app.route('/api/books', methods=['GET'])
def get_books():
    """output/ 直下をスキャンし、PDFを含むフォルダを本として返す"""
    output_dir = BASE_DIR / config.OUTPUT_DIR
    books = []

    if output_dir.is_dir():
        for entry in output_dir.iterdir():
            if not entry.is_dir():
                continue

            pdf_files = [f for f in entry.iterdir() if f.is_file() and f.suffix.lower() == '.pdf']
            if not pdf_files:
                continue

            pdf_files.sort(key=lambda f: _part_sort_key(f.name))

            parts = []
            total_size = 0
            for f in pdf_files:
                size = f.stat().st_size
                total_size += size
                parts.append({
                    "filename": f.name,
                    "pages": None,
                    "size": size,
                })

            books.append({
                "name": entry.name,
                "parts": parts,
                "total_size": total_size,
                "mtime": entry.stat().st_mtime,
            })

    books.sort(key=lambda b: b["mtime"], reverse=True)

    return jsonify({"books": books})


@app.route('/pdfs/<book>/<filename>')
def get_pdf(book, filename):
    """本のPDFファイルを配信（パストラバーサル対策済み、Range対応）"""
    if '..' in book or '/' in book or '\\' in book:
        abort(404)
    if '..' in filename or '/' in filename or '\\' in filename:
        abort(404)
    if not filename.lower().endswith('.pdf'):
        abort(404)

    book_dir = BASE_DIR / config.OUTPUT_DIR / book
    return send_from_directory(book_dir, filename, conditional=True)


# エントリーポイントはルートの server.py から実行
# if __name__ == '__main__':
#     app.run(debug=True, host='127.0.0.1', port=5000)

