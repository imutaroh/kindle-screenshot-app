// アプリアイコン生成: 角丸スクエア・インディゴ系グラデーション背景 + 📚 絵文字を中央配置。
// 1024pxで描いた後、iconutil が要求する全サイズをそれぞれ描き直して assets/AppIcon.iconset/ に書き出し、
// 最後に iconutil で assets/AppIcon.icns を生成する。
// 使い方: swift scripts/make_icon.swift
import AppKit

let indigoStart = NSColor(deviceRed: 0x63 / 255.0, green: 0x66 / 255.0, blue: 0xF1 / 255.0, alpha: 1)
let indigoEnd = NSColor(deviceRed: 0x4F / 255.0, green: 0x46 / 255.0, blue: 0xE5 / 255.0, alpha: 1)

/// 指定サイズでアイコンを描画し、PNG表現(NSBitmapImageRep)を返す。
/// ベクター的に都度描き直すため、どのサイズでも輪郭がぼやけない。
func drawIcon(size px: Int) -> NSBitmapImageRep {
    let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
    )!
    rep.size = NSSize(width: px, height: px)

    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

    let size = CGFloat(px)
    // Apple のアイコングリッド: 1024px 中、角丸スクエアは約 824px（マージン 100px相当）に合わせて比率を保つ
    let margin = size * (100.0 / 1024.0)
    let squareRect = CGRect(x: margin, y: margin, width: size - margin * 2, height: size - margin * 2)
    let cornerRadius = size * (185.0 / 1024.0)
    let square = NSBezierPath(roundedRect: squareRect, xRadius: cornerRadius, yRadius: cornerRadius)

    square.addClip()
    let gradient = NSGradient(starting: indigoStart, ending: indigoEnd)!
    gradient.draw(in: square, angle: -45)

    NSGraphicsContext.restoreGraphicsState()
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

    // 📚 絵文字を中央に大きく配置
    let emojiFontSize = size * 0.56
    let font = NSFont.systemFont(ofSize: emojiFontSize)
    let attrs: [NSAttributedString.Key: Any] = [.font: font]
    let emoji = "📚" as NSString
    let emojiSize = emoji.size(withAttributes: attrs)
    let emojiOrigin = CGPoint(
        x: (size - emojiSize.width) / 2,
        y: (size - emojiSize.height) / 2 - size * 0.02 // 絵文字の視覚的重心を中央へ寄せる微調整
    )
    emoji.draw(at: emojiOrigin, withAttributes: attrs)

    NSGraphicsContext.restoreGraphicsState()
    return rep
}

func writePNG(_ rep: NSBitmapImageRep, to path: String) {
    let png = rep.representation(using: .png, properties: [:])!
    try! png.write(to: URL(fileURLWithPath: path))
    print("wrote \(path)")
}

let fm = FileManager.default
try! fm.createDirectory(atPath: "assets", withIntermediateDirectories: true)
try! fm.createDirectory(atPath: "assets/AppIcon.iconset", withIntermediateDirectories: true)

// 1024px版はトップレベルにも保存（README等での参照用）
writePNG(drawIcon(size: 1024), to: "assets/icon_1024.png")

// iconutil が要求する iconset の全サイズ
let iconsetSizes: [(name: String, px: Int)] = [
    ("icon_16x16.png", 16),
    ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32),
    ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128),
    ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256),
    ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512),
    ("icon_512x512@2x.png", 1024),
]

for entry in iconsetSizes {
    writePNG(drawIcon(size: entry.px), to: "assets/AppIcon.iconset/\(entry.name)")
}

// iconutil で .icns に変換
let task = Process()
task.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
task.arguments = ["-c", "icns", "assets/AppIcon.iconset", "-o", "assets/AppIcon.icns"]
try! task.run()
task.waitUntilExit()
if task.terminationStatus != 0 {
    print("iconutil failed with status \(task.terminationStatus)")
    exit(task.terminationStatus)
}
print("wrote assets/AppIcon.icns")
