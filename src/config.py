"""
設定値管理モジュール
"""

# ページ送り後の待機秒数（Kindleのアニメーション完了を待つ）
PAGE_WAIT_TIME = 0.5

# 最初のNページの待機秒数（初期読み込み用）
INITIAL_PAGES_COUNT = 5  # 最初の5ページ
INITIAL_PAGE_WAIT_TIME = 2.0  # 最初のページの待機秒数

# 通常のページ送り後の待機秒数（初期ページ以降）
NORMAL_PAGE_WAIT_TIME = 0.5  # 通常のページの待機秒数（0.5秒）

# 実行開始前の待機秒数（Kindleに切り替える時間）
START_DELAY = 5

# 1つのPDFに含める画像枚数
PDF_PAGES_PER_FILE = 50

# ページめくり方向（"left" = 日本語の本（右綴じ・縦書き） / "right" = 英語の本（左綴じ・横書き））
PAGE_DIRECTION = "left"

# 最大ページ数（0 = 無制限）
# 通常は 0（無制限）にして、ホットコーナーまたは pHash 自動停止に任せる。
# 念のため上限を設けたい場合のみ数値を入れる。
MAX_PAGES = 0

# 出力先ディレクトリ（本ごとのフォルダがこの下に作成される）
OUTPUT_DIR = "output"

# 画像ファイル名のプレフィックス
IMAGE_PREFIX = "page_"

# 画像ファイルの拡張子
IMAGE_EXTENSION = ".png"

# 自動停止設定
# 同じページが連続して保存された場合の自動停止（ページが変わらなくなったら停止）
AUTO_STOP_ON_DUPLICATE = True  # True: 同じページが連続したら自動停止
DUPLICATE_CHECK_COUNT = 2  # 何回連続で同じページが保存されたら停止するか

# pHash（知覚ハッシュ）のハミング距離の閾値
# 小さいほど厳格（0=完全一致）。時計やマウスカーソル等の微差を吸収するため5前後が妥当。
DUPLICATE_HASH_THRESHOLD = 5

# ホットコーナーによる手動制御
# 左上角にマウス移動 → 取得済みPNGでPDF化して終了
# 右上角にマウス移動 → キャンセル（PDF化せず終了）
CORNER_ACTION_ENABLED = True
CORNER_THRESHOLD_PX = 10  # 角からの距離（px）

