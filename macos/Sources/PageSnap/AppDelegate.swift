// PageSnap のアプリ本体。Go製サーバー（kindleweb）をサブプロセスとして起動し、
// 起動確認後に WKWebView を載せた通常ウィンドウで表示する。
//
// 終了時（Cmd+Q・ウィンドウを閉じる、のどちらでも）はサーバーの GET /api/running を
// 確認し、キャプチャ実行中なら確認ダイアログを出してから、子プロセスに SIGTERM を
// 送ってから本体を終了する。ゾンビプロセスを残さない。
import AppKit
import WebKit

final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var serverProcess: Process?
    private var serverPort: UInt16?

    private var pollTimer: Timer?
    private var pollAttempts = 0
    private let maxPollAttempts = 50 // 0.2秒 × 50 = 10秒
    private var isPolling = false

    private var window: NSWindow?
    private var webView: WKWebView?
    private var navigationHandler: WebNavigationHandler?

    /// ウィンドウを閉じる操作（windowShouldClose）が既に確認・子プロセス停止まで
    /// 完了させている場合に true。二重にダイアログを出さないためのフラグ。
    private var terminationConfirmed = false

    private let dataDirectory: URL = {
        let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        return docs.appendingPathComponent("PageSnap", isDirectory: true)
    }()

    // MARK: - 起動

    func applicationDidFinishLaunching(_ notification: Notification) {
        do {
            try FileManager.default.createDirectory(at: dataDirectory, withIntermediateDirectories: true)
        } catch {
            showFatalErrorAndQuit("データ保存フォルダの作成に失敗しました:\n\(error.localizedDescription)")
            return
        }

        guard let port = Self.findFreePort() else {
            showFatalErrorAndQuit("空きポートの確保に失敗しました。")
            return
        }
        serverPort = port

        // ヘルパーは Contents/MacOS/ に同梱している（Resources/ に置くと署名が
        // nested code として扱われず、TCC が本体と別アプリ扱いして画面収録の
        // 許可が効かなくなるため）。
        guard let binaryURL = Bundle.main.url(forAuxiliaryExecutable: "kindleweb"),
              FileManager.default.isExecutableFile(atPath: binaryURL.path) else {
            showFatalErrorAndQuit("サーバー本体（kindleweb）がアプリバンドル内に見つかりません。")
            return
        }

        let process = Process()
        process.executableURL = binaryURL
        process.arguments = ["-port", String(port), "-out", dataDirectory.path]
        process.terminationHandler = { [weak self] proc in
            DispatchQueue.main.async {
                guard let self, self.window == nil else { return }
                // まだ起動確認中（ウィンドウ未表示）にサーバーが落ちた場合は、
                // 10秒のタイムアウトを待たずに即座にエラー終了する。
                self.pollTimer?.invalidate()
                self.pollTimer = nil
                self.showFatalErrorAndQuit("サーバープロセスが予期せず終了しました（終了コード: \(proc.terminationStatus)）。")
            }
        }

        do {
            try process.run()
        } catch {
            showFatalErrorAndQuit("サーバーの起動に失敗しました:\n\(error.localizedDescription)")
            return
        }
        serverProcess = process

        startPolling(port: port)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    // MARK: - 空きポート確保（port 0 で bind → 割り当てられたポート番号を取得 → close）

    private static func findFreePort() -> UInt16? {
        let sock = socket(AF_INET, SOCK_STREAM, 0)
        guard sock >= 0 else { return nil }
        defer { close(sock) }

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        addr.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))

        let bindResult = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPtr in
                Darwin.bind(sock, sockaddrPtr, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bindResult == 0 else { return nil }

        var assigned = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let getResult = withUnsafeMutablePointer(to: &assigned) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockaddrPtr in
                getsockname(sock, sockaddrPtr, &len)
            }
        }
        guard getResult == 0 else { return nil }
        return UInt16(bigEndian: assigned.sin_port)
    }

    // MARK: - サーバー起動待ちポーリング（0.2秒間隔、最大10秒）

    private func startPolling(port: UInt16) {
        pollAttempts = 0
        pollTimer = Timer.scheduledTimer(withTimeInterval: 0.2, repeats: true) { [weak self] _ in
            self?.pollOnce(port: port)
        }
    }

    private func pollOnce(port: UInt16) {
        guard !isPolling else { return }
        pollAttempts += 1

        if pollAttempts > maxPollAttempts {
            pollTimer?.invalidate()
            pollTimer = nil
            showFatalErrorAndQuit("サーバーが10秒以内に応答しませんでした。")
            return
        }

        isPolling = true
        let url = URL(string: "http://127.0.0.1:\(port)/")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 1.0
        let task = URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self else { return }
                self.isPolling = false
                if let http = response as? HTTPURLResponse, http.statusCode == 200 {
                    self.pollTimer?.invalidate()
                    self.pollTimer = nil
                    self.showMainWindow(port: port)
                }
            }
        }
        task.resume()
    }

    // MARK: - メインウィンドウ

    private func showMainWindow(port: UInt16) {
        let contentRect = NSRect(x: 0, y: 0, width: 820, height: 640)
        let window = NSWindow(
            contentRect: contentRect,
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "PageSnap"
        window.minSize = NSSize(width: 720, height: 560)
        window.center()
        window.delegate = self

        let webView = WKWebView(frame: contentRect, configuration: WKWebViewConfiguration())
        let navigationHandler = WebNavigationHandler()
        webView.navigationDelegate = navigationHandler
        webView.uiDelegate = navigationHandler
        window.contentView = webView

        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        webView.load(URLRequest(url: URL(string: "http://127.0.0.1:\(port)/")!))

        self.window = window
        self.webView = webView
        self.navigationHandler = navigationHandler
    }

    // MARK: - エラー終了

    private func showFatalErrorAndQuit(_ message: String) {
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "PageSnap を起動できません"
        alert.informativeText = message
        alert.addButton(withTitle: "終了")
        alert.runModal()
        NSApp.terminate(nil)
    }

    // MARK: - 終了処理（ウィンドウを閉じる）

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        if terminationConfirmed { return true }
        guard let process = serverProcess, process.isRunning, let port = serverPort else {
            return true
        }

        confirmAndStopServerIfNeeded(port: port) { [weak self] proceed in
            guard let self, proceed else { return }
            self.terminationConfirmed = true
            sender.close()
        }
        return false
    }

    // MARK: - 終了処理（Cmd+Q）

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if terminationConfirmed { return .terminateNow }
        guard let process = serverProcess, process.isRunning, let port = serverPort else {
            return .terminateNow
        }

        confirmAndStopServerIfNeeded(port: port) { proceed in
            sender.reply(toApplicationShouldTerminate: proceed)
        }
        return .terminateLater
    }

    /// GET /api/running でキャプチャ実行中か確認し、実行中なら確認ダイアログを出す。
    /// ユーザーが終了を選んだ（または未実行だった）場合は子プロセスを SIGTERM で止めてから
    /// completion(true) を呼ぶ。キャンセルした場合は completion(false)（何もしない）。
    private func confirmAndStopServerIfNeeded(port: UInt16, completion: @escaping (Bool) -> Void) {
        checkIfCaptureRunning(port: port) { [weak self] isRunning in
            guard let self else {
                completion(true)
                return
            }

            if isRunning {
                let alert = NSAlert()
                alert.alertStyle = .warning
                alert.messageText = "キャプチャ実行中です"
                alert.informativeText = "終了すると中断されます。よろしいですか？"
                alert.addButton(withTitle: "終了する")
                alert.addButton(withTitle: "キャンセル")
                guard alert.runModal() == .alertFirstButtonReturn else {
                    completion(false)
                    return
                }
            }

            self.terminateServerProcess {
                completion(true)
            }
        }
    }

    /// GET /api/running を叩く。通信エラー時はサーバーが既に落ちている等とみなし、
    /// 実行中ではない扱いにする（終了処理をブロックしない）。
    private func checkIfCaptureRunning(port: UInt16, completion: @escaping (Bool) -> Void) {
        let url = URL(string: "http://127.0.0.1:\(port)/api/running")!
        var request = URLRequest(url: url)
        request.timeoutInterval = 2.0
        let task = URLSession.shared.dataTask(with: request) { data, _, _ in
            var running = false
            if let data,
               let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let value = json["running"] as? Bool {
                running = value
            }
            DispatchQueue.main.async {
                completion(running)
            }
        }
        task.resume()
    }

    /// 子プロセス（kindleweb）に SIGTERM を送って終了を待つ。待機はバックグラウンド
    /// キューで行い（メインスレッドをブロックしない）、完了後にメインキューへ戻す。
    private func terminateServerProcess(completion: @escaping () -> Void) {
        guard let process = serverProcess, process.isRunning else {
            completion()
            return
        }
        DispatchQueue.global(qos: .userInitiated).async {
            process.terminate() // Foundation の Process.terminate() は SIGTERM を送る
            process.waitUntilExit()
            DispatchQueue.main.async {
                completion()
            }
        }
    }
}
