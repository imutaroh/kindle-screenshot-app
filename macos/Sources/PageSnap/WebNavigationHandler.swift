// WKWebView のナビゲーションを制御する:
// - 遷移先はローカルホスト（127.0.0.1 / localhost）のみ許可し、それ以外は拒否する
// - target="_blank" や window.open() による新規ウィンドウ生成は行わず、同一WebViewで読み込む
import WebKit

final class WebNavigationHandler: NSObject, WKNavigationDelegate, WKUIDelegate {
    private static let allowedHosts: Set<String> = ["127.0.0.1", "localhost"]

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
    ) {
        if let host = navigationAction.request.url?.host, Self.allowedHosts.contains(host) {
            decisionHandler(.allow)
        } else {
            decisionHandler(.cancel)
        }
    }

    func webView(
        _ webView: WKWebView,
        createWebViewWith configuration: WKWebViewConfiguration,
        for navigationAction: WKNavigationAction,
        windowFeatures: WKWindowFeatures
    ) -> WKWebView? {
        // 新規ウィンドウ/タブは作らず、同一WebViewでリクエストを読み込む。
        if navigationAction.targetFrame == nil {
            webView.load(navigationAction.request)
        }
        return nil
    }
}
