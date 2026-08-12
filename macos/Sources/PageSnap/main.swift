// PageSnap のエントリーポイント。
// AppKit の通常アプリとして起動する（LSUIElement は付けない＝Dockに常時表示）。
import AppKit

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
