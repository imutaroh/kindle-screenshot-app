// visionocr: kindlesnap が使う Apple Vision OCR ヘルパー。
//
// 使い方: visionocr img1.png [img2.png ...]
//
// 引数の PNG を順番に RecognizeDocumentsRequest（macOS 26 の Swift 専用 API）で読み、
// 1ページにつき1行の JSON（JSON Lines）を stdout に書く。stderr は診断用。
//
// なぜ Swift か: ObjC の VNRecognizeTextRequest は縦書き（tategaki）を読めない。
// Swift の RecognizeDocumentsRequest だけが、縦書き日本語を右→左の正しい順で、
// 段落構造つきで返す（ベンチマークで確認済み）。
//
// 座標はすべて正規化（0〜1）・左上原点の [x, y, w, h]。Vision は左下原点なので変換する。
// ダークモードの画像は反転せずにそのまま渡す。色管理された CIContext で反転すると
// 精度が大きく落ちた（CER 19%）ため。
import Foundation
import ImageIO
import Vision

struct LineOut: Encodable {
    let text: String
    let bbox: [Double]
    let direction: String
    let confidence: Float
}

struct ParagraphOut: Encodable {
    let text: String
    let bbox: [Double]
    let direction: String
    let isTitle: Bool
    let confidence: Float
    let lines: [LineOut]
}

struct ListOut: Encodable {
    let bbox: [Double]
    let items: [String]
}

struct TableOut: Encodable {
    let bbox: [Double]
    let rows: [[String]]
}

struct PageOut: Encodable {
    let path: String
    var width: Int = 0
    var height: Int = 0
    var paragraphs: [ParagraphOut] = []
    var lists: [ListOut] = []
    var tables: [TableOut] = []
    var error: String? = nil
}

// Vision の正規化矩形（左下原点）を左上原点の [x, y, w, h] に変換する。
func bbox(_ r: CGRect) -> [Double] {
    [r.origin.x, 1 - r.origin.y - r.size.height, r.size.width, r.size.height]
}

func dirName(_ d: RecognizedTextObservation.Direction?) -> String {
    guard let d else { return "" }
    switch d {
    case .leftToRight: return "ltr"
    case .rightToLeft: return "rtl"
    case .topToBottom: return "ttb"
    @unknown default: return ""
    }
}

func lineOut(_ o: RecognizedTextObservation) -> LineOut {
    let t = o.topCandidates(1).first
    return LineOut(text: t?.string ?? o.transcript,
                   bbox: bbox(o.boundingBox.cgRect),
                   direction: dirName(o.textDirection),
                   confidence: t?.confidence ?? o.confidence)
}

func loadImage(_ path: String) -> CGImage? {
    guard let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil) else { return nil }
    return CGImageSourceCreateImageAtIndex(src, 0, nil)
}

func recognize(_ cg: CGImage, into out: inout PageOut) async throws {
    var req = RecognizeDocumentsRequest()
    req.textRecognitionOptions.recognitionLanguages = [Locale.Language(identifier: "ja-JP"),
                                                       Locale.Language(identifier: "en-US")]
    req.textRecognitionOptions.automaticallyDetectLanguage = false
    req.textRecognitionOptions.useLanguageCorrection = true
    req.barcodeDetectionOptions.enabled = false
    let observations = try await req.perform(on: cg)
    for obs in observations {
        let doc = obs.document
        // 行単位の isTitle は実画像で常に false だった。見出しの情報は document.title にだけ
        // 入るので、本文と一致する段落（最初の1つ）に isTitle を付ける。
        var titleText = doc.title?.transcript
        // 段落の並びは Vision が返す読み順のまま（縦書きなら右→左）。並べ替えない。
        for p in doc.paragraphs {
            let lines = p.lines.map(lineOut)
            // 1行でも縦書きなら縦書き段落とみなす（縦書き段落に混じる横組みの数字対策）
            let dir = lines.contains { $0.direction == "ttb" } ? "ttb" : (lines.first?.direction ?? "")
            let conf = lines.isEmpty ? 0 : lines.reduce(Float(0)) { $0 + $1.confidence } / Float(lines.count)
            var isTitle = false
            if let t = titleText, t == p.transcript {
                isTitle = true
                titleText = nil
            }
            out.paragraphs.append(ParagraphOut(text: p.transcript, bbox: bbox(p.boundingRegion.boundingBox.cgRect),
                                               direction: dir, isTitle: isTitle, confidence: conf, lines: lines))
        }
        for l in doc.lists {
            out.lists.append(ListOut(bbox: bbox(l.boundingRegion.boundingBox.cgRect),
                                     items: l.items.map { $0.itemString }))
        }
        for t in doc.tables {
            let rows = t.rows.map { $0.map { $0.content.text.transcript.replacingOccurrences(of: "\n", with: " ") } }
            out.tables.append(TableOut(bbox: bbox(t.boundingRegion.boundingBox.cgRect), rows: rows))
        }
    }
}

// 1行書くたびに flush する。パイプ先ではバッファされ、Go 側の進捗表示が遅れたり、
// 途中で落ちたときにそれまでのページが失われたりするのを防ぐ。
//
// エンコードに失敗しても（座標が NaN になった等）行を省かない。Go 側は「引数1つにつき
// 1行、同じ順番」で結果を突き合わせるので、1行抜けると後ろのページがすべてずれて
// 全体がエラーになる。代わりに path と error だけの行を書き、そのページだけを失敗にする。
func emit(_ page: PageOut, _ enc: JSONEncoder) {
    var data: Data
    do {
        data = try enc.encode(page)
    } catch {
        fputs("visionocr: JSON エンコード失敗: \(page.path): \(error)\n", stderr)
        let fallback = PageOut(path: page.path, width: page.width, height: page.height,
                               error: "JSON エンコード失敗: \(error)")
        // 文字列と整数だけの PageOut のエンコードは失敗しない（失敗しても Go 側が
        // 次の行の順番のずれとして検出し、このページを失敗にして続ける）
        guard let d = try? enc.encode(fallback) else { return }
        data = d
    }
    data.append(0x0A)
    FileHandle.standardOutput.write(data)
}

@main struct Main {
    static func main() async {
        let paths = Array(CommandLine.arguments.dropFirst())
        guard !paths.isEmpty else {
            fputs("usage: visionocr img1.png [img2.png ...]\n", stderr)
            exit(2)
        }
        let enc = JSONEncoder()
        enc.outputFormatting = [.withoutEscapingSlashes]
        // 1プロセスで順に処理する（モデルの読み込みを1回で済ませる）。
        // 1ページの失敗は error 付きの行にして、残りのページは続ける。
        for path in paths {
            var out = PageOut(path: path)
            guard let cg = loadImage(path) else {
                out.error = "画像を読み込めません"
                emit(out, enc)
                continue
            }
            out.width = cg.width
            out.height = cg.height
            let t0 = Date()
            do {
                try await recognize(cg, into: &out)
            } catch {
                out = PageOut(path: path, width: cg.width, height: cg.height, error: "\(error)")
            }
            fputs(String(format: "visionocr: %@ %.2fs paragraphs=%d\n", path, Date().timeIntervalSince(t0), out.paragraphs.count), stderr)
            emit(out, enc)
        }
    }
}
