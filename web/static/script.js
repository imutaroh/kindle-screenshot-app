// Kindle 自動スクショアプリ - フロントエンドロジック

const POLL_INTERVAL_MS = 1000;
const STOP_CONFIRM_TIMEOUT_MS = 3000;
const TOAST_DURATION_MS = 4000;
const TOAST_FADE_MS = 300;
let pollTimer = null;
let lastLoggedMessage = "";
let lastLoggedPage = 0;
let stopConfirmPending = false;
let stopConfirmTimer = null;

// ----- DOM 要素 -----
const $ = (id) => document.getElementById(id);

const els = {
    statusBadge: $("statusBadge"),
    bookName: $("bookName"),
    maxPages: $("maxPages"),
    pdfPagesPerFile: $("pdfPagesPerFile"),
    autoDeletePng: $("autoDeletePng"),
    startButton: $("startButton"),
    stopButton: $("stopButton"),
    openFolderButton: $("openFolderButton"),
    progressSection: $("progressSection"),
    progressBar: $("progressBar"),
    progressText: $("progressText"),
    progressPercent: $("progressPercent"),
    messageText: $("messageText"),
    logContainer: $("logContainer"),
    toastContainer: $("toastContainer"),
};

// ----- ユーティリティ -----
function setStatus(label, kind) {
    els.statusBadge.textContent = label;
    els.statusBadge.classList.remove("running", "completed", "error");
    if (kind) els.statusBadge.classList.add(kind);
}

function appendLog(text, kind = "info") {
    const p = document.createElement("p");
    p.className = `log-entry ${kind}`;
    const time = new Date().toLocaleTimeString("ja-JP");
    p.textContent = `[${time}] ${text}`;
    // 「待機中...」プレースホルダーを最初の追加で消す
    if (els.logContainer.children.length === 1) {
        const first = els.logContainer.firstElementChild;
        if (first && first.textContent.trim() === "待機中...") {
            els.logContainer.innerHTML = "";
        }
    }
    els.logContainer.appendChild(p);
    els.logContainer.scrollTop = els.logContainer.scrollHeight;
}

function setRunningUI(isRunning) {
    els.startButton.style.display = isRunning ? "none" : "";
    els.stopButton.style.display = isRunning ? "" : "none";
    els.bookName.disabled = isRunning;
    els.maxPages.disabled = isRunning;
    els.pdfPagesPerFile.disabled = isRunning;
    els.autoDeletePng.disabled = isRunning;
    document.querySelectorAll('input[name="direction"]').forEach((r) => (r.disabled = isRunning));
    if (isRunning) {
        els.progressSection.style.display = "";
        els.openFolderButton.style.display = "none";
    }
    resetStopConfirmState();
}

// ----- トースト通知 -----
function showToast(message, kind = "info") {
    if (!els.toastContainer) return;
    const toast = document.createElement("div");
    toast.className = `toast ${kind}`;
    toast.textContent = message;
    els.toastContainer.appendChild(toast);

    // 追加直後に show を付けることでスライドイン+フェードインさせる
    requestAnimationFrame(() => {
        requestAnimationFrame(() => toast.classList.add("show"));
    });

    setTimeout(() => {
        toast.classList.remove("show");
        toast.classList.add("hide");
        setTimeout(() => toast.remove(), TOAST_FADE_MS);
    }, TOAST_DURATION_MS);
}

// ----- 停止ボタンの2段階確認 -----
function resetStopConfirmState() {
    stopConfirmPending = false;
    if (stopConfirmTimer) {
        clearTimeout(stopConfirmTimer);
        stopConfirmTimer = null;
    }
    if (els.stopButton) {
        els.stopButton.textContent = "⏹️ 停止";
        els.stopButton.classList.remove("btn-confirm");
    }
}

function resetProgressUI() {
    lastLoggedMessage = "";
    lastLoggedPage = 0;
    els.progressBar.style.width = "0%";
    els.progressBar.classList.remove("indeterminate");
    els.progressText.textContent = "0 ページ";
    els.progressPercent.textContent = "—";
}

// ----- API 呼び出し -----
async function startCapture() {
    const bookName = els.bookName.value.trim();
    if (!bookName) {
        showToast("本の名前を入力してください", "error");
        els.bookName.classList.add("input-error");
        els.bookName.focus();
        return;
    }

    const directionEl = document.querySelector('input[name="direction"]:checked');
    const direction = directionEl ? directionEl.value : "left";

    const payload = {
        book_name: bookName,
        max_pages: parseInt(els.maxPages.value || "0", 10),
        pdf_pages_per_file: parseInt(els.pdfPagesPerFile.value || "50", 10),
        auto_delete_png: els.autoDeletePng.checked,
        direction: direction,
    };

    try {
        const res = await fetch("/api/start", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
        });
        const data = await res.json();
        if (!res.ok || !data.success) {
            showToast(data.message || "開始に失敗しました", "error");
            return;
        }
        resetProgressUI();
        appendLog(`「${bookName}」のキャプチャを開始しました`, "info");
        appendLog("Kindle を自動で前面に表示します（フルスクリーン推奨）", "info");
        setRunningUI(true);
        startPolling();
    } catch (e) {
        showToast("通信エラー: " + e.message, "error");
    }
}

async function stopCapture() {
    if (!stopConfirmPending) {
        stopConfirmPending = true;
        els.stopButton.textContent = "⚠️ もう一度押すと停止";
        els.stopButton.classList.add("btn-confirm");
        stopConfirmTimer = setTimeout(resetStopConfirmState, STOP_CONFIRM_TIMEOUT_MS);
        return;
    }

    resetStopConfirmState();

    try {
        const res = await fetch("/api/stop", { method: "POST" });
        const data = await res.json();
        if (data.success) {
            appendLog("停止要求を送信しました", "info");
        } else {
            showToast(data.message || "停止に失敗しました", "error");
        }
    } catch (e) {
        showToast("通信エラー: " + e.message, "error");
    }
}

async function openFolder() {
    try {
        const res = await fetch("/api/open-folder", { method: "POST" });
        const data = await res.json();
        if (!data.success) {
            showToast(data.message || "フォルダを開けませんでした", "error");
        }
    } catch (e) {
        showToast("通信エラー: " + e.message, "error");
    }
}

async function fetchStatus() {
    try {
        const res = await fetch("/api/status");
        if (!res.ok) return;
        return await res.json();
    } catch {
        return null;
    }
}

// ----- ポーリング -----
function startPolling() {
    if (pollTimer) return;
    pollTimer = setInterval(updateStatus, POLL_INTERVAL_MS);
    updateStatus();
}

function stopPolling() {
    if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
    }
}

async function updateStatus() {
    const s = await fetchStatus();
    if (!s) return;

    // ステータスバッジ
    if (s.is_running) {
        setStatus("実行中", "running");
    } else if (s.status === "完了") {
        setStatus("完了", "completed");
    } else if (s.status === "エラー") {
        setStatus("エラー", "error");
    } else {
        setStatus("待機中", null);
    }

    // 進捗表示
    const current = s.current_page || 0;
    const total = s.total_pages || 0;
    if (total > 0) {
        const pct = Math.min(100, Math.round((current / total) * 100));
        els.progressBar.classList.remove("indeterminate");
        els.progressBar.style.width = pct + "%";
        els.progressText.textContent = `${current} / ${total} ページ`;
        els.progressPercent.textContent = pct + "%";
    } else if (s.is_running) {
        // 総ページ数は最後まで分からないため、実行中は流れるバーで動作中を示す
        els.progressBar.classList.add("indeterminate");
        els.progressBar.style.width = "100%";
        els.progressText.textContent = current > 0 ? `${current} ページ取得済み` : "準備中...";
        els.progressPercent.textContent = "—";
    }

    // メッセージボックス
    if (s.message) {
        els.messageText.textContent = s.message;
    }

    // ログ：ページ番号やメッセージが変わったら追加
    if (current > lastLoggedPage) {
        appendLog(`ページ ${current} を保存`, "info");
        lastLoggedPage = current;
    }
    if (s.message && s.message !== lastLoggedMessage) {
        // ページ進捗メッセージは個別ログで出してるので除外
        if (!/^ページ \d+ を保存中/.test(s.message)) {
            appendLog(s.message, "info");
        }
        lastLoggedMessage = s.message;
    }

    // 終了処理
    if (!s.is_running && (s.status === "完了" || s.status === "エラー" || s.status === "待機中")) {
        if (s.status === "完了") {
            appendLog(`✅ 完了: ${s.output_dir || ""}`, "success");
        } else if (s.status === "エラー") {
            appendLog(`❌ エラー: ${s.error || "不明なエラー"}`, "error");
        }
        setRunningUI(false);
        els.progressBar.classList.remove("indeterminate");
        // 出力フォルダが出来ていれば「保存先を開く」ボタンを表示
        if (s.output_dir) {
            els.openFolderButton.style.display = "";
        }
        stopPolling();
    }
}

// ----- 初期化 -----
async function init() {
    // 設定値をサーバから取得してデフォルトに反映
    try {
        const res = await fetch("/api/config");
        if (res.ok) {
            const cfg = await res.json();
            if (cfg.pdf_pages_per_file) {
                els.pdfPagesPerFile.value = cfg.pdf_pages_per_file;
            }
            if (typeof cfg.max_pages === "number") {
                els.maxPages.value = cfg.max_pages;
            }
            if (cfg.page_direction) {
                const target = document.querySelector(`input[name="direction"][value="${cfg.page_direction}"]`);
                if (target) target.checked = true;
            }
        }
    } catch {
        // ignore
    }

    // 起動時に一度ステータス確認（既に実行中ならUIを合わせる）
    const s = await fetchStatus();
    if (s && s.is_running) {
        setRunningUI(true);
        startPolling();
    }
}

els.bookName.addEventListener("input", () => {
    els.bookName.classList.remove("input-error");
});

document.addEventListener("DOMContentLoaded", init);

// HTMLの onclick から呼ばれるためグローバルに公開
window.startCapture = startCapture;
window.stopCapture = stopCapture;
window.openFolder = openFolder;
