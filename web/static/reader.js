// 読書ビューア（PDFリーダー） - フロントエンドロジック
// 表示専用。編集・注釈・読書位置の永続化は行わない。

import * as pdfjsLib from "/static/vendor/pdf.min.mjs";

pdfjsLib.GlobalWorkerOptions.workerSrc = "/static/vendor/pdf.worker.min.mjs";

const SPREAD_MIN_WIDTH = 900;
const ZOOM_MIN = 0.5;
const ZOOM_MAX = 3.0;
const ZOOM_STEP = 0.25;
const STAGE_PADDING = 24; // .page-surface の padding と対応
const PAGE_GAP = 4; // .page-surface の gap と対応

// ----- 余白カット検出パラメータ -----
const MARGIN_DETECT_WIDTH = 300; // 検出用オフスクリーンレンダリングの幅(px)
const MARGIN_COLOR_TOLERANCE = 30; // 余白色とみなすRGB各チャンネルの許容差
const MARGIN_ROW_COL_THRESHOLD = 0.95; // 行/列の何割が余白色ならその行/列を余白とみなすか
const MARGIN_MIN_BBOX_AREA_RATIO = 0.2; // bboxがページ面積のこの比率未満なら検出失敗扱い
const MARGIN_BBOX_PADDING = 4; // bboxに付与するパディング(base viewport座標系のpx)

// ----- DOM 要素 -----
const $ = (id) => document.getElementById(id);

const els = {
    readerTitle: $("readerTitle"),
    shelfView: $("shelfView"),
    shelfLoading: $("shelfLoading"),
    shelfError: $("shelfError"),
    shelfEmpty: $("shelfEmpty"),
    bookGrid: $("bookGrid"),

    viewerView: $("viewerView"),
    viewerBookTitle: $("viewerBookTitle"),
    viewerStage: $("viewerStage"),
    pageSurface: $("pageSurface"),
    pageLoading: $("pageLoading"),
    pageError: $("pageError"),
    canvasLeft: $("pageCanvasLeft"),
    canvasRight: $("pageCanvasRight"),

    clickZoneLeft: $("clickZoneLeft"),
    clickZoneRight: $("clickZoneRight"),

    prevPageBtn: $("prevPageBtn"),
    nextPageBtn: $("nextPageBtn"),
    pageSlider: $("pageSlider"),
    pageIndicator: $("pageIndicator"),
    partIndicator: $("partIndicator"),

    spreadToggleBtn: $("spreadToggleBtn"),
    directionToggleBtn: $("directionToggleBtn"),
    fitToggleBtn: $("fitToggleBtn"),
    marginCropToggleBtn: $("marginCropToggleBtn"),
    zoomOutBtn: $("zoomOutBtn"),
    zoomInBtn: $("zoomInBtn"),
    zoomIndicator: $("zoomIndicator"),

    closeViewerBtn: $("closeViewerBtn"),
};

// ----- 状態 -----
/** @type {Array<{name: string, parts: Array<{filename:string, pages:number|null, size:number}>, total_size:number, mtime:number}>} */
let books = [];

/** 現在開いている本の状態 */
let current = null; // { name, parts, partDocPromises, globalPageMap, totalPages, groups, groupIndex }

// このアプリのPDFは横長のフルスクリーンスクショ（1枚に本の見開き相当が写っている）ため、
// ビューア側の見開きはデフォルトOFF。必要ならツールバーで切り替え可能。
let spreadEnabled = false;
let direction = "rtl"; // "rtl" = 右綴じ（デフォルト） / "ltr" = 左綴じ
let fitMode = "height"; // "height" | "width"
let zoomFactor = 1.0;
let marginCropEnabled = true; // ページ外周の余白（黒帯/白帯）自動カット。デフォルトON

let renderVersion = 0; // 連打・高速ページ送り時の競合防止用トークン
let activeRenderTasks = [];
let resizeTimer = null;

// ----- ユーティリティ -----
function formatBytes(bytes) {
    if (!bytes || bytes <= 0) return "0 KB";
    const units = ["B", "KB", "MB", "GB"];
    let value = bytes;
    let unitIndex = 0;
    while (value >= 1024 && unitIndex < units.length - 1) {
        value /= 1024;
        unitIndex += 1;
    }
    const digits = unitIndex === 0 ? 0 : 1;
    return `${value.toFixed(digits)} ${units[unitIndex]}`;
}

function effectiveSpread() {
    return spreadEnabled && window.innerWidth >= SPREAD_MIN_WIDTH;
}

/**
 * 総ページ数と見開き設定から、ページ番号のグループ配列を作る。
 * 書籍慣例により1ページ目（表紙）は単独表示、以降は 2-3, 4-5 のペア。
 * 見開きOFFの場合は常に単ページのグループになる。
 */
function buildGroups(totalPages, spreadOn) {
    const groups = [];
    if (totalPages < 1) return groups;

    if (!spreadOn) {
        for (let p = 1; p <= totalPages; p += 1) {
            groups.push([p]);
        }
        return groups;
    }

    groups.push([1]);
    let p = 2;
    while (p <= totalPages) {
        if (p + 1 <= totalPages) {
            groups.push([p, p + 1]);
            p += 2;
        } else {
            groups.push([p]);
            p += 1;
        }
    }
    return groups;
}

/** ページ番号（1始まり）から、それを含むグループのインデックスを求める */
function pageToGroupIndex(groups, pageNum) {
    for (let i = 0; i < groups.length; i += 1) {
        if (groups[i].includes(pageNum)) return i;
    }
    return 0;
}

// ==========================================================================
// 本棚ビュー
// ==========================================================================

async function loadShelf() {
    els.shelfLoading.style.display = "";
    els.shelfError.style.display = "none";
    els.shelfEmpty.style.display = "none";
    els.bookGrid.innerHTML = "";

    try {
        const res = await fetch("/api/books");
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = await res.json();
        books = data.books || [];
    } catch (err) {
        els.shelfLoading.style.display = "none";
        els.shelfError.style.display = "";
        els.shelfError.textContent = `本の一覧を読み込めませんでした: ${err.message}`;
        return;
    }

    els.shelfLoading.style.display = "none";

    if (books.length === 0) {
        els.shelfEmpty.style.display = "";
        return;
    }

    const fragment = document.createDocumentFragment();
    for (const book of books) {
        const card = document.createElement("button");
        card.type = "button";
        card.className = "book-card";
        card.setAttribute("aria-label", `${book.name} を開く`);

        const icon = document.createElement("div");
        icon.className = "book-card-icon";
        icon.textContent = "📘";

        const name = document.createElement("div");
        name.className = "book-card-name";
        name.textContent = book.name;

        const meta = document.createElement("div");
        meta.className = "book-card-meta";
        meta.textContent = `${book.parts.length} part / ${formatBytes(book.total_size)}`;

        card.appendChild(icon);
        card.appendChild(name);
        card.appendChild(meta);
        card.addEventListener("click", () => openBook(book));

        fragment.appendChild(card);
    }
    els.bookGrid.appendChild(fragment);
}

// ==========================================================================
// ビューア: 本を開く / 閉じる
// ==========================================================================

async function openBook(book) {
    els.shelfView.style.display = "none";
    els.viewerView.style.display = "";
    document.body.classList.add("viewer-open");
    els.readerTitle.textContent = `📖 ${book.name}`;
    els.viewerBookTitle.textContent = book.name;
    els.pageError.style.display = "none";
    els.pageLoading.style.display = "";
    els.canvasLeft.classList.remove("visible");
    els.canvasRight.classList.remove("visible");

    // 全 part の getDocument を並列で開始（キャッシュして開き直さない）
    const partDocPromises = book.parts.map((part) => {
        const url = `/pdfs/${encodeURIComponent(book.name)}/${encodeURIComponent(part.filename)}`;
        return pdfjsLib.getDocument(url).promise;
    });

    current = {
        name: book.name,
        parts: book.parts,
        partDocPromises,
        globalPageMap: [],
        totalPages: 0,
        groups: [],
        groupIndex: 0,
        bboxCache: new Map(), // globalPage -> bbox({x,y,width,height} in base viewport座標) | null（検出失敗/全面表示）
    };

    try {
        const docs = await Promise.all(partDocPromises);
        const globalPageMap = [];
        docs.forEach((doc, partIdx) => {
            for (let localPage = 1; localPage <= doc.numPages; localPage += 1) {
                globalPageMap.push({ partIdx, localPage });
            }
        });

        if (current.name !== book.name) return; // 途中で別の本が開かれた場合は破棄

        current.globalPageMap = globalPageMap;
        current.totalPages = globalPageMap.length;
        current.groups = buildGroups(current.totalPages, effectiveSpread());
        current.groupIndex = 0;

        els.pageSlider.min = "1";
        els.pageSlider.max = String(Math.max(current.totalPages, 1));
        els.pageSlider.value = "1";

        updateToggleLabels();
        await renderCurrentGroup();
    } catch (err) {
        els.pageLoading.style.display = "none";
        els.pageError.style.display = "";
        els.pageError.textContent = `PDFの読み込みに失敗しました: ${err.message}`;
    }
}

async function closeViewer() {
    const closing = current;
    current = null;

    els.viewerView.style.display = "none";
    document.body.classList.remove("viewer-open");
    els.shelfView.style.display = "";
    els.readerTitle.textContent = "📖 読書ビューア";
    els.viewerBookTitle.textContent = "";

    // レンダリング中タスクをキャンセルしてから破棄する
    activeRenderTasks.forEach((task) => {
        try {
            task.cancel();
        } catch (_e) {
            // 既に完了している場合は無視
        }
    });
    activeRenderTasks = [];

    if (closing && closing.partDocPromises) {
        for (const p of closing.partDocPromises) {
            try {
                const doc = await p;
                await doc.destroy();
            } catch (_e) {
                // 読み込み失敗していたドキュメントは destroy 不要
            }
        }
    }
}

// ==========================================================================
// レンダリング
// ==========================================================================

function updateToggleLabels() {
    els.spreadToggleBtn.textContent = spreadEnabled ? "見開き" : "単ページ";
    els.spreadToggleBtn.classList.toggle("active", spreadEnabled);

    els.directionToggleBtn.textContent = direction === "rtl" ? "右綴じ" : "左綴じ";
    els.directionToggleBtn.classList.toggle("active", direction === "rtl");

    els.fitToggleBtn.textContent = fitMode === "height" ? "全体フィット" : "幅フィット";

    els.marginCropToggleBtn.classList.toggle("active", marginCropEnabled);

    els.zoomIndicator.textContent = `${Math.round(zoomFactor * 100)}%`;
}

function updatePageIndicator() {
    if (!current || current.groups.length === 0) return;
    const group = current.groups[current.groupIndex];
    const label = group.length === 2 ? `${group[0]}-${group[1]}` : `${group[0]}`;
    els.pageIndicator.textContent = `${label} / ${current.totalPages}`;
    els.pageSlider.value = String(group[0]);

    const anchorEntry = current.globalPageMap[group[0] - 1];
    if (anchorEntry) {
        const part = current.parts[anchorEntry.partIdx];
        els.partIndicator.textContent = part ? part.filename : "";
    }
}

/**
 * ページ外周の均一色の帯（黒帯/白帯）を検出し、コンテンツのbounding boxを
 * base viewport座標系（scale=1）で返す。検出失敗時はnull（呼び出し側は全面表示にフォールバック）。
 * 低解像度（幅300px程度）のオフスクリーンcanvasにレンダリングして解析するため、
 * ページごとに一度だけ呼ばれる想定（結果はcurrent.bboxCacheでキャッシュされる）。
 */
async function detectContentBBox(page) {
    const baseViewport = page.getViewport({ scale: 1 });
    const detectScale = MARGIN_DETECT_WIDTH / baseViewport.width;
    const detectViewport = page.getViewport({ scale: detectScale });
    const w = Math.max(1, Math.round(detectViewport.width));
    const h = Math.max(1, Math.round(detectViewport.height));

    const offCanvas = document.createElement("canvas");
    offCanvas.width = w;
    offCanvas.height = h;
    const ctx = offCanvas.getContext("2d", { willReadFrequently: true });
    if (!ctx) return null;

    try {
        const task = page.render({ canvasContext: ctx, viewport: detectViewport });
        await task.promise;
    } catch (_e) {
        return null; // 検出用レンダリング失敗 → 全面表示にフォールバック
    }

    let imageData;
    try {
        imageData = ctx.getImageData(0, 0, w, h);
    } catch (_e) {
        return null;
    }
    const data = imageData.data;

    const pixelAt = (x, y) => {
        const idx = (y * w + x) * 4;
        return [data[idx], data[idx + 1], data[idx + 2]];
    };

    // 四隅の色の平均を余白色のサンプルとする（黒帯/白帯どちらにも対応）
    const corners = [pixelAt(0, 0), pixelAt(w - 1, 0), pixelAt(0, h - 1), pixelAt(w - 1, h - 1)];
    const marginColor = [0, 1, 2].map((c) =>
        Math.round(corners.reduce((sum, px) => sum + px[c], 0) / corners.length)
    );

    const isMarginPixel = (x, y) => {
        const [r, g, b] = pixelAt(x, y);
        return (
            Math.abs(r - marginColor[0]) <= MARGIN_COLOR_TOLERANCE &&
            Math.abs(g - marginColor[1]) <= MARGIN_COLOR_TOLERANCE &&
            Math.abs(b - marginColor[2]) <= MARGIN_COLOR_TOLERANCE
        );
    };

    const rowIsMargin = (y) => {
        let count = 0;
        for (let x = 0; x < w; x += 1) {
            if (isMarginPixel(x, y)) count += 1;
        }
        return count / w >= MARGIN_ROW_COL_THRESHOLD;
    };
    const colIsMargin = (x) => {
        let count = 0;
        for (let y = 0; y < h; y += 1) {
            if (isMarginPixel(x, y)) count += 1;
        }
        return count / h >= MARGIN_ROW_COL_THRESHOLD;
    };

    let top = 0;
    while (top < h && rowIsMargin(top)) top += 1;
    let bottom = h - 1;
    while (bottom > top && rowIsMargin(bottom)) bottom -= 1;
    let left = 0;
    while (left < w && colIsMargin(left)) left += 1;
    let right = w - 1;
    while (right > left && colIsMargin(right)) right -= 1;

    if (top >= bottom || left >= right) return null; // ページ全体が余白色 → 検出失敗

    const bboxW = right - left + 1;
    const bboxH = bottom - top + 1;

    // 誤検出ガード: bboxがページ面積の20%未満なら検出失敗として扱う
    if ((bboxW * bboxH) / (w * h) < MARGIN_MIN_BBOX_AREA_RATIO) {
        return null;
    }

    // 検出解像度 → base viewport座標系（scale=1）へ換算し、パディングを付与
    const scaleToBase = 1 / detectScale;
    const pad = MARGIN_BBOX_PADDING;
    const x = Math.max(0, left * scaleToBase - pad);
    const y = Math.max(0, top * scaleToBase - pad);
    const width = Math.min(baseViewport.width - x, bboxW * scaleToBase + pad * 2);
    const height = Math.min(baseViewport.height - y, bboxH * scaleToBase + pad * 2);

    return { x, y, width, height };
}

/** ページのbboxをキャッシュから取得、なければ検出して current.bboxCache に保存する */
async function getOrDetectBBox(bboxCache, info) {
    if (bboxCache.has(info.globalPage)) {
        return bboxCache.get(info.globalPage);
    }
    const bbox = await detectContentBBox(info.page);
    bboxCache.set(info.globalPage, bbox);
    return bbox;
}

async function renderCurrentGroup() {
    if (!current || current.groups.length === 0) return;

    const version = ++renderVersion;
    els.pageError.style.display = "none";
    els.pageLoading.style.display = "";

    // 前回のレンダリングタスクをキャンセル
    activeRenderTasks.forEach((task) => {
        try {
            task.cancel();
        } catch (_e) {
            /* 無視 */
        }
    });
    activeRenderTasks = [];

    const group = current.groups[current.groupIndex];

    try {
        const pageInfos = await Promise.all(
            group.map(async (globalPage) => {
                const { partIdx, localPage } = current.globalPageMap[globalPage - 1];
                const doc = await current.partDocPromises[partIdx];
                const page = await doc.getPage(localPage);
                return { globalPage, page };
            })
        );

        if (version !== renderVersion) return; // 途中で別ページへ遷移済み

        // 余白カット有効時は各ページのbboxを検出する（ページごとにキャッシュ済みなら再検出しない）
        if (marginCropEnabled) {
            const bboxCache = current.bboxCache;
            await Promise.all(
                pageInfos.map(async (info) => {
                    info.bbox = await getOrDetectBBox(bboxCache, info);
                })
            );
        } else {
            pageInfos.forEach((info) => {
                info.bbox = null;
            });
        }

        if (version !== renderVersion) return; // 検出中に別ページへ遷移済み

        // グループ内でのページ番号→表示スロット（left/right canvas）割り当て
        // RTL: 若い番号を右、LTR: 若い番号を左
        const sorted = [...pageInfos].sort((a, b) => a.globalPage - b.globalPage);
        let leftInfo = null;
        let rightInfo = null;
        if (sorted.length === 1) {
            rightInfo = sorted[0];
        } else if (direction === "rtl") {
            rightInfo = sorted[0];
            leftInfo = sorted[1];
        } else {
            leftInfo = sorted[0];
            rightInfo = sorted[1];
        }

        const stageRect = els.pageSurface.getBoundingClientRect();
        const availWidth = Math.max(stageRect.width - STAGE_PADDING * 2, 100);
        const availHeight = Math.max(els.viewerStage.clientHeight - STAGE_PADDING * 2, 100);
        const slotCount = leftInfo && rightInfo ? 2 : 1;

        const renderInto = async (canvasEl, info) => {
            if (!info) {
                canvasEl.classList.remove("visible");
                return;
            }
            const baseViewport = info.page.getViewport({ scale: 1 });
            const bbox = info.bbox; // null = 余白カット無効 or 検出失敗 → 全面表示

            // フィット計算は bbox（コンテンツ領域）のサイズを基準に行う
            const contentWidth = bbox ? bbox.width : baseViewport.width;
            const contentHeight = bbox ? bbox.height : baseViewport.height;

            const perSlotWidth =
                slotCount === 2 ? (availWidth - PAGE_GAP) / 2 : availWidth;
            let scale;
            if (fitMode === "height") {
                // 全体フィット: 高さ・スロット幅の両方に収まるスケール（横長ページの見切れ防止）
                scale = Math.min(availHeight / contentHeight, perSlotWidth / contentWidth);
            } else {
                scale = perSlotWidth / contentWidth;
            }
            scale *= zoomFactor;

            const outputScale = window.devicePixelRatio || 1;
            const displayWidth = contentWidth * scale;
            const displayHeight = contentHeight * scale;

            canvasEl.width = Math.floor(displayWidth * outputScale);
            canvasEl.height = Math.floor(displayHeight * outputScale);
            canvasEl.style.width = `${Math.floor(displayWidth)}px`;
            canvasEl.style.height = `${Math.floor(displayHeight)}px`;
            canvasEl.classList.add("visible");

            const ctx = canvasEl.getContext("2d");

            if (!bbox) {
                // 従来通り: ページ全体を直接表示用canvasへレンダリング
                const viewport = info.page.getViewport({ scale });
                const transform = outputScale !== 1 ? [outputScale, 0, 0, outputScale, 0, 0] : null;
                const renderTask = info.page.render({ canvasContext: ctx, transform, viewport });
                activeRenderTasks.push(renderTask);
                await renderTask.promise;
                return;
            }

            // bboxあり: ページ全体を必要スケールでオフスクリーンにレンダリングしてから
            // bbox領域だけを表示用canvasへdrawImageで転写する
            const fullViewport = info.page.getViewport({ scale: scale * outputScale });
            const offCanvas = document.createElement("canvas");
            offCanvas.width = Math.max(1, Math.ceil(fullViewport.width));
            offCanvas.height = Math.max(1, Math.ceil(fullViewport.height));
            const offCtx = offCanvas.getContext("2d");

            const renderTask = info.page.render({ canvasContext: offCtx, viewport: fullViewport });
            activeRenderTasks.push(renderTask);
            await renderTask.promise;

            const sx = bbox.x * scale * outputScale;
            const sy = bbox.y * scale * outputScale;
            const sw = bbox.width * scale * outputScale;
            const sh = bbox.height * scale * outputScale;
            ctx.clearRect(0, 0, canvasEl.width, canvasEl.height);
            ctx.drawImage(offCanvas, sx, sy, sw, sh, 0, 0, canvasEl.width, canvasEl.height);
        };

        await Promise.all([renderInto(els.canvasLeft, leftInfo), renderInto(els.canvasRight, rightInfo)]);

        if (version !== renderVersion) return;

        els.pageLoading.style.display = "none";
        updatePageIndicator();
    } catch (err) {
        if (err && err.name === "RenderingCancelledException") return;
        if (version !== renderVersion) return;
        els.pageLoading.style.display = "none";
        els.pageError.style.display = "";
        els.pageError.textContent = `ページの表示に失敗しました: ${err.message}`;
    }
}

// ==========================================================================
// ナビゲーション
// ==========================================================================

function goToGroupIndex(idx) {
    if (!current || current.groups.length === 0) return;
    const clamped = Math.max(0, Math.min(idx, current.groups.length - 1));
    if (clamped === current.groupIndex) return;
    current.groupIndex = clamped;
    renderCurrentGroup();
}

function goNext() {
    if (!current) return;
    goToGroupIndex(current.groupIndex + 1);
}

function goPrev() {
    if (!current) return;
    goToGroupIndex(current.groupIndex - 1);
}

/** 綴じ方向に応じた「進む」方向のナビゲーション（ArrowLeft/ArrowRight・クリックゾーン共通） */
function goByKeyDirection(key) {
    // RTL（右綴じ）: ArrowLeft = 次へ, ArrowRight = 前へ
    // LTR（左綴じ）: ArrowRight = 次へ, ArrowLeft = 前へ
    const isNext = direction === "rtl" ? key === "ArrowLeft" : key === "ArrowRight";
    if (isNext) {
        goNext();
    } else {
        goPrev();
    }
}

function regroupPreservingPosition() {
    if (!current) return;
    const anchorPage = current.groups[current.groupIndex]
        ? current.groups[current.groupIndex][0]
        : 1;
    current.groups = buildGroups(current.totalPages, effectiveSpread());
    current.groupIndex = pageToGroupIndex(current.groups, anchorPage);
    renderCurrentGroup();
}

// ==========================================================================
// イベントハンドラ
// ==========================================================================

els.prevPageBtn.addEventListener("click", goPrev);
els.nextPageBtn.addEventListener("click", goNext);

els.clickZoneLeft.addEventListener("click", () => goByKeyDirection("ArrowLeft"));
els.clickZoneRight.addEventListener("click", () => goByKeyDirection("ArrowRight"));

els.pageSlider.addEventListener("input", () => {
    if (!current) return;
    const value = Number(els.pageSlider.value);
    const group = current.groups[pageToGroupIndex(current.groups, value)];
    const label = group && group.length === 2 ? `${group[0]}-${group[1]}` : `${value}`;
    els.pageIndicator.textContent = `${label} / ${current.totalPages}`;
});

els.pageSlider.addEventListener("change", () => {
    if (!current) return;
    const value = Number(els.pageSlider.value);
    goToGroupIndex(pageToGroupIndex(current.groups, value));
});

els.spreadToggleBtn.addEventListener("click", () => {
    spreadEnabled = !spreadEnabled;
    updateToggleLabels();
    regroupPreservingPosition();
});

els.directionToggleBtn.addEventListener("click", () => {
    direction = direction === "rtl" ? "ltr" : "rtl";
    updateToggleLabels();
    renderCurrentGroup();
});

els.fitToggleBtn.addEventListener("click", () => {
    fitMode = fitMode === "height" ? "width" : "height";
    updateToggleLabels();
    renderCurrentGroup();
});

els.marginCropToggleBtn.addEventListener("click", () => {
    marginCropEnabled = !marginCropEnabled;
    updateToggleLabels();
    renderCurrentGroup();
});

els.zoomInBtn.addEventListener("click", () => {
    zoomFactor = Math.min(ZOOM_MAX, Math.round((zoomFactor + ZOOM_STEP) * 100) / 100);
    updateToggleLabels();
    renderCurrentGroup();
});

els.zoomOutBtn.addEventListener("click", () => {
    zoomFactor = Math.max(ZOOM_MIN, Math.round((zoomFactor - ZOOM_STEP) * 100) / 100);
    updateToggleLabels();
    renderCurrentGroup();
});

els.closeViewerBtn.addEventListener("click", closeViewer);

document.addEventListener("keydown", (e) => {
    if (!current || els.viewerView.style.display === "none") return;

    if (e.key === "Escape") {
        e.preventDefault();
        closeViewer();
        return;
    }
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
        e.preventDefault();
        goByKeyDirection(e.key);
        return;
    }
    if (e.key === " " || e.code === "Space") {
        e.preventDefault();
        goNext();
    }
});

window.addEventListener("resize", () => {
    if (!current) return;
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
        regroupPreservingPosition();
    }, 150);
});

// ----- 初期化 -----
updateToggleLabels();
loadShelf();
