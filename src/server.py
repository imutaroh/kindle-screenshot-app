"""
Flask Webサーバー
Web UI版のメインサーバー
"""

from flask import Flask, render_template, request, jsonify
import threading
import time
from pathlib import Path
from typing import Optional, Dict
import os

from src import config
from src.screenshot import capture_all_pages
from src.pdf_generator import create_pdfs_from_directory, delete_png_files

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


def create_output_directory(book_name: str) -> Path:
    """
    出力ディレクトリを作成する
    
    Args:
        book_name: 本の名前
    
    Returns:
        Path: 作成したディレクトリのパス
    """
    safe_name = book_name.replace("/", "_").replace("\\", "_").replace(":", "_")
    output_dir = Path(config.OUTPUT_DIR) / safe_name
    output_dir.mkdir(parents=True, exist_ok=True)
    return output_dir


def capture_worker(book_name: str, max_pages: int, pdf_pages_per_file: int, auto_delete_png: bool):
    """
    バックグラウンドでキャプチャ処理を実行する
    
    Args:
        book_name: 本の名前
        max_pages: 最大ページ数
        pdf_pages_per_file: PDF結合枚数
        auto_delete_png: PNG自動削除フラグ
    """
    global state
    
    try:
        state.is_running = True
        state.should_stop = False
        state.current_page = 0
        state.status = "実行中"
        state.message = "準備中..."
        state.error = None
        
        # 出力ディレクトリ作成
        state.output_dir = create_output_directory(book_name)
        state.book_name = book_name
        state.message = f"保存先: {state.output_dir}"
        
        # 開始待機
        time.sleep(config.START_DELAY)
        
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
        captured_pages = capture_all_pages(
            output_dir=state.output_dir,
            max_pages=max_pages,
            on_progress=on_progress,
            should_stop=lambda: state.should_stop
        )
        
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
    
    if not book_name:
        return jsonify({"success": False, "message": "本の名前を入力してください"}), 400
    
    # バックグラウンドスレッドで実行
    thread = threading.Thread(
        target=capture_worker,
        args=(book_name, max_pages, pdf_pages_per_file, auto_delete_png),
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


@app.route('/api/config', methods=['GET'])
def get_config():
    """設定値を取得"""
    return jsonify({
        "page_wait_time": config.PAGE_WAIT_TIME,
        "start_delay": config.START_DELAY,
        "pdf_pages_per_file": config.PDF_PAGES_PER_FILE,
        "max_pages": config.MAX_PAGES
    })


# エントリーポイントはルートの server.py から実行
# if __name__ == '__main__':
#     app.run(debug=True, host='127.0.0.1', port=5000)

