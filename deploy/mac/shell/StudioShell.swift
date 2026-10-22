// ACTIVATE Studio's own window on macOS.
//
// The app's executable when it is built on a Mac. It starts the Studio's
// background server through the launcher script (Resources/launcher.sh,
// which loads the LaunchAgent and waits for the server), then shows the
// Studio it serves on 127.0.0.1 in a native window: no browser needed, one
// Dock icon, the window remembered between launches. Links that leave the
// Studio open in the default browser, file pickers and downloads behave as
// in any Mac app, and quitting leaves the server running for the next open
// (Stop Background Server in the app menu stops it). The title bar takes the
// color the page names for it, as an installed web app's does. The Studio's
// page can ask the window, through window.webkit.messageHandlers.studioShell,
// for the system folder chooser and to follow the Studio to another port.
//
// Built with: swiftc -O -target arm64-apple-macos13 StudioShell.swift
// (deploy/mac/build_app.sh does this when it runs on a Mac).

import AppKit
import WebKit

let launcher = Bundle.main.resourceURL!.appendingPathComponent("launcher.sh")

// `ACTIVATE Studio --stop`, `--uninstall` and `--help` from a terminal go to
// the launcher, with no window.
let passThrough: Set<String> = ["--stop", "--uninstall", "--help", "-h"]
if CommandLine.arguments.count > 1, passThrough.contains(CommandLine.arguments[1]) {
  let p = Process()
  p.executableURL = URL(fileURLWithPath: "/bin/bash")
  p.arguments = [launcher.path] + CommandLine.arguments.dropFirst()
  do { try p.run(); p.waitUntilExit(); exit(p.terminationStatus) } catch { exit(1) }
}

/// The standard window ground, for the views the shell draws itself: the
/// window's own background color is the title bar's (see paintBar).
final class GroundView: NSView {
  override func draw(_ dirtyRect: NSRect) {
    NSColor.windowBackgroundColor.setFill()
    dirtyRect.fill()
  }
}

/// "#rrggbb" for a color, in sRGB.
func hexString(_ color: NSColor) -> String? {
  guard let c = color.usingColorSpace(.sRGB) else { return nil }
  let byte = { (v: CGFloat) -> Int in Int((max(0, min(1, v)) * 255).rounded()) }
  let s = String((byte(c.redComponent) << 16) | (byte(c.greenComponent) << 8) | byte(c.blueComponent), radix: 16)
  return "#" + String(repeating: "0", count: 6 - s.count) + s
}

func colorFromHex(_ hex: String?) -> NSColor? {
  guard let hex, hex.count == 7, hex.hasPrefix("#"), let n = Int(hex.dropFirst(), radix: 16) else { return nil }
  return NSColor(srgbRed: CGFloat((n >> 16) & 255) / 255, green: CGFloat((n >> 8) & 255) / 255,
                 blue: CGFloat(n & 255) / 255, alpha: 1)
}

/// True when white text reads better on the color than black, by the WCAG
/// contrast ratio: the rule the page uses for the classification banner's
/// text (bannerInk in web/src/components/ClassificationBanner.tsx).
func wantsLightInk(_ color: NSColor) -> Bool {
  guard let c = color.usingColorSpace(.sRGB) else { return false }
  func linear(_ v: CGFloat) -> CGFloat { v <= 0.03928 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4) }
  let l = 0.2126 * linear(c.redComponent) + 0.7152 * linear(c.greenComponent) + 0.0722 * linear(c.blueComponent)
  return 1.05 / (l + 0.05) >= (l + 0.05) / 0.05
}

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate,
  WKScriptMessageHandlerWithReply {
  var window: NSWindow!
  var webView: WKWebView!
  var studio: URL?
  var popups: [NSWindow] = []
  var downloads: [ObjectIdentifier: URL] = [:]
  var barWatches: [NSKeyValueObservation] = []
  /// The bar's last color, kept between launches so the window opens with it.
  let barColorKey = "TitleBarColor"
  let message = NSTextField(wrappingLabelWithString: "Starting ACTIVATE Studio…")
  let spinner = NSProgressIndicator()
  let buttons = NSStackView()

  func applicationDidFinishLaunching(_ notification: Notification) {
    buildMenu()
    let config = WKWebViewConfiguration()
    // Local storage (the remembered page, drafts) lasts between launches.
    config.websiteDataStore = .default()
    config.applicationNameForUserAgent = "ACTIVATEStudioMac"
    // The page's requests of the window; popups share the configuration.
    config.userContentController.addScriptMessageHandler(self, contentWorld: .page, name: "studioShell")
    webView = WKWebView(frame: .zero, configuration: config)
    webView.navigationDelegate = self
    webView.uiDelegate = self
    if #available(macOS 13.3, *) {
      // Safari's Web Inspector, when `defaults write com.parallelworks.activate-studio WebInspector -bool true`.
      webView.isInspectable = UserDefaults.standard.bool(forKey: "WebInspector")
    }

    window = NSWindow(
      contentRect: NSRect(x: 0, y: 0, width: 1400, height: 900),
      styleMask: [.titled, .closable, .miniaturizable, .resizable],
      backing: .buffered, defer: false)
    window.title = "ACTIVATE Studio"
    window.minSize = NSSize(width: 720, height: 480)
    window.contentView = startingView()
    paintBar(window, colorFromHex(UserDefaults.standard.string(forKey: barColorKey)))
    followBarColor(webView, in: window)
    if !window.setFrameUsingName("ACTIVATE Studio") { window.center() }
    window.setFrameAutosaveName("ACTIVATE Studio")
    window.makeKeyAndOrderFront(nil)
    NSApp.activate(ignoringOtherApps: true)
    startServer()
  }

  func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

  func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
    window.makeKeyAndOrderFront(nil)
    return true
  }

  // MARK: the server

  /// Runs the launcher, which starts or reuses the background server and
  /// prints the address it answers on.
  func startServer() {
    showStarting("Starting ACTIVATE Studio…")
    DispatchQueue.global(qos: .userInitiated).async {
      let p = Process()
      p.executableURL = URL(fileURLWithPath: "/bin/bash")
      p.arguments = [launcher.path]
      var env = ProcessInfo.processInfo.environment
      env["STUDIO_NO_BROWSER"] = "1"
      p.environment = env
      let out = Pipe()
      p.standardOutput = out
      p.standardError = out
      var text = ""
      var status: Int32 = -1
      do {
        try p.run()
        // Read before waiting, so a chatty launcher cannot fill the pipe and stall.
        text = String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        p.waitUntilExit()
        status = p.terminationStatus
      } catch {
        text = "The launcher could not run: \(error.localizedDescription)"
      }
      let output = text, exitStatus = status
      let url = output.range(of: #"http://127\.0\.0\.1:\d+/"#, options: .regularExpression).flatMap { URL(string: String(output[$0])) }
      DispatchQueue.main.async {
        if let url, exitStatus == 0 {
          self.studio = url
          self.window.contentView = self.webView
          self.webView.load(URLRequest(url: url))
        } else {
          let tail = output.split(separator: "\n").suffix(6).joined(separator: "\n")
          self.showFailure(tail.isEmpty ? "ACTIVATE Studio did not start." : tail)
        }
      }
    }
  }

  func isStudio(_ url: URL) -> Bool {
    guard let studio else { return false }
    return url.scheme == studio.scheme && url.host == studio.host && url.port == studio.port
  }

  @objc func stopServer(_ sender: Any?) {
    let p = Process()
    p.executableURL = URL(fileURLWithPath: "/bin/bash")
    p.arguments = [launcher.path, "--stop"]
    try? p.run()
    p.waitUntilExit()
    NSApp.terminate(nil)
  }

  @objc func retry(_ sender: Any?) { startServer() }

  /// The setup wizard, through the page's address for it (#view=settings:setup).
  @objc func runSetup(_ sender: Any?) {
    guard studio != nil, window.contentView === webView else { return }
    window.makeKeyAndOrderFront(nil)
    webView.evaluateJavaScript("location.hash = '#view=settings:setup'", completionHandler: nil)
  }

  @objc func openLogs(_ sender: Any?) {
    let logs = FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent("Library/Application Support/ACTIVATE Studio/logs")
    NSWorkspace.shared.open(logs)
  }

  // MARK: the title bar

  /// The bar takes the color the page names in its theme-color meta tag,
  /// which is the color an installed web app's title bar takes: the
  /// classification banner's when one is shown, otherwise the page's ground
  /// in its current theme. The page changes the tag when its theme, the
  /// banner, or the system's appearance changes, and WebKit reports each
  /// change. A page without the tag leaves the bar as it was.
  func followBarColor(_ view: WKWebView, in window: NSWindow) {
    barWatches.append(view.observe(\.themeColor, options: [.new]) { [weak self, weak window] view, _ in
      guard let window else { return }
      DispatchQueue.main.async { self?.barColorChanged(view, window) }
    })
  }

  func barColorChanged(_ view: WKWebView, _ window: NSWindow) {
    guard let color = view.themeColor else { return }
    paintBar(window, color)
    if window === self.window, let hex = hexString(color) {
      UserDefaults.standard.set(hex, forKey: barColorKey)
    }
  }

  /// A transparent title bar shows the window's background color, so the
  /// bar is painted by giving the window that color. The title and the
  /// window controls take the light or dark appearance that reads on it;
  /// it is set on the title bar alone, since the page follows the window's
  /// appearance for its own light and dark. Nil restores the standard bar.
  func paintBar(_ window: NSWindow, _ color: NSColor?) {
    window.titlebarAppearsTransparent = color != nil
    window.titlebarSeparatorStyle = color == nil ? .automatic : .none
    window.backgroundColor = color ?? .windowBackgroundColor
    let titlebar = window.standardWindowButton(.closeButton)?.superview
    if let color {
      titlebar?.appearance = NSAppearance(named: wantsLightInk(color) ? .darkAqua : .aqua)
    } else {
      titlebar?.appearance = nil
    }
  }

  // MARK: the waiting and failure views

  func startingView() -> NSView {
    let view = GroundView()
    spinner.style = .spinning
    spinner.controlSize = .regular
    message.alignment = .center
    message.font = .systemFont(ofSize: 13)
    message.textColor = .secondaryLabelColor
    buttons.orientation = .horizontal
    buttons.spacing = 8
    let stack = NSStackView(views: [spinner, message, buttons])
    stack.orientation = .vertical
    stack.spacing = 12
    stack.translatesAutoresizingMaskIntoConstraints = false
    view.addSubview(stack)
    NSLayoutConstraint.activate([
      stack.centerXAnchor.constraint(equalTo: view.centerXAnchor),
      stack.centerYAnchor.constraint(equalTo: view.centerYAnchor),
      stack.widthAnchor.constraint(lessThanOrEqualToConstant: 520),
    ])
    return view
  }

  func showStarting(_ text: String) {
    message.stringValue = text
    buttons.arrangedSubviews.forEach { $0.removeFromSuperview() }
    spinner.isHidden = false
    spinner.startAnimation(nil)
    if window.contentView === webView { window.contentView = startingView() }
  }

  func showFailure(_ text: String) {
    spinner.stopAnimation(nil)
    spinner.isHidden = true
    message.stringValue = text
    buttons.arrangedSubviews.forEach { $0.removeFromSuperview() }
    buttons.addArrangedSubview(NSButton(title: "Try Again", target: self, action: #selector(retry(_:))))
    buttons.addArrangedSubview(NSButton(title: "Open Logs", target: self, action: #selector(openLogs(_:))))
  }

  // MARK: navigation

  func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
               decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
    if action.shouldPerformDownload { decisionHandler(.download); return }
    guard let url = action.request.url else { decisionHandler(.cancel); return }
    // The Studio itself, its blobs and frames, and anything inside a frame
    // (embedded viewers) stay here; a page outside it opens in the browser.
    if isStudio(url) || ["about", "blob", "data"].contains(url.scheme ?? "") || action.targetFrame?.isMainFrame == false {
      decisionHandler(.allow)
      return
    }
    NSWorkspace.shared.open(url)
    decisionHandler(.cancel)
  }

  func webView(_ webView: WKWebView, decidePolicyFor response: WKNavigationResponse,
               decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
    if let http = response.response as? HTTPURLResponse,
       let disposition = http.value(forHTTPHeaderField: "Content-Disposition"),
       disposition.lowercased().hasPrefix("attachment") {
      decisionHandler(.download)
      return
    }
    decisionHandler(response.canShowMIMEType ? .allow : .download)
  }

  func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
    // The server went away (stopped, or restarting after setup chose a
    // folder or a port): wait for it and load the same page again.
    let nsError = error as NSError
    if nsError.code == NSURLErrorCannotConnectToHost, let studio {
      let failed = nsError.userInfo[NSURLErrorFailingURLErrorKey] as? URL
      let again = failed.flatMap { isStudio($0) ? $0 : nil } ?? studio
      DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { webView.load(URLRequest(url: again)) }
    }
  }

  /// window.open and target=_blank: the Studio's own pages (a session popped
  /// out) get a window of their own; anything else opens in the browser.
  func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
               for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
    guard let url = action.request.url else { return nil }
    if !isStudio(url) && !["about", "blob"].contains(url.scheme ?? "") {
      NSWorkspace.shared.open(url)
      return nil
    }
    let child = WKWebView(frame: .zero, configuration: configuration)
    child.navigationDelegate = self
    child.uiDelegate = self
    let w = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1100, height: 760),
                     styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
    w.contentView = child
    w.title = "ACTIVATE Studio"
    w.isReleasedWhenClosed = false
    // The main window's bar until the popped-out page names its own.
    paintBar(w, window.titlebarAppearsTransparent ? window.backgroundColor : nil)
    followBarColor(child, in: w)
    _ = w.cascadeTopLeft(from: window.frame.origin)
    w.makeKeyAndOrderFront(nil)
    popups.append(w)
    return child
  }

  func webViewDidClose(_ webView: WKWebView) {
    if let w = popups.first(where: { $0.contentView === webView }) {
      w.close()
      popups.removeAll { $0 === w }
    }
  }

  // MARK: the page's requests

  /// Answers window.webkit.messageHandlers.studioShell.postMessage, as a
  /// promise in the page. Only the Studio's own top-level page is answered,
  /// never a page shown in a frame inside it.
  func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage,
                             replyHandler: @escaping (Any?, String?) -> Void) {
    guard message.frameInfo.isMainFrame, let page = message.frameInfo.request.url, isStudio(page),
          let body = message.body as? [String: Any], let action = body["action"] as? String else {
      replyHandler(nil, "not available here")
      return
    }
    switch action {
    case "chooseFolder":
      chooseFolder(start: body["start"] as? String, message: body["message"] as? String,
                   prompt: body["prompt"] as? String, in: message.webView, reply: replyHandler)
    case "studioMoved":
      // The server saved another port and is restarting on it: follow it,
      // to the page the request names. The load retries until it answers.
      guard let port = (body["port"] as? NSNumber)?.intValue, (1024...65535).contains(port), let studio,
            var parts = URLComponents(url: studio, resolvingAgainstBaseURL: false) else {
        replyHandler(nil, "not a port")
        return
      }
      parts.port = port
      if let hash = body["hash"] as? String, hash.hasPrefix("#") { parts.fragment = String(hash.dropFirst()) }
      guard let next = parts.url else { replyHandler(nil, "not a port"); return }
      parts.fragment = nil
      self.studio = parts.url
      replyHandler(true, nil)
      DispatchQueue.main.asyncAfter(deadline: .now() + 1) { self.webView.load(URLRequest(url: next)) }
    default:
      replyHandler(nil, "unknown request \(action)")
    }
  }

  /// The system folder chooser, as a sheet on the page's window; replies
  /// with the folder's path, or nothing when the person cancels.
  func chooseFolder(start: String?, message: String?, prompt: String?, in view: WKWebView?,
                    reply: @escaping (Any?, String?) -> Void) {
    let panel = NSOpenPanel()
    panel.canChooseDirectories = true
    panel.canChooseFiles = false
    panel.canCreateDirectories = true
    panel.allowsMultipleSelection = false
    panel.message = message ?? "Choose a folder."
    panel.prompt = prompt ?? "Choose"
    if let start, !start.isEmpty {
      panel.directoryURL = URL(fileURLWithPath: (start as NSString).expandingTildeInPath, isDirectory: true)
    }
    panel.beginSheetModal(for: view?.window ?? window) { result in
      reply(result == .OK ? panel.url?.path : nil, nil)
    }
  }

  // MARK: dialogs, file pickers, the microphone

  func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
               initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
    let panel = NSOpenPanel()
    panel.allowsMultipleSelection = parameters.allowsMultipleSelection
    panel.canChooseDirectories = parameters.allowsDirectories
    panel.canChooseFiles = true
    panel.beginSheetModal(for: webView.window ?? window) { result in
      completionHandler(result == .OK ? panel.urls : nil)
    }
  }

  func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
               initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
    let alert = NSAlert()
    alert.messageText = message
    alert.beginSheetModal(for: webView.window ?? window) { _ in completionHandler() }
  }

  func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
               initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
    let alert = NSAlert()
    alert.messageText = message
    alert.addButton(withTitle: "OK")
    alert.addButton(withTitle: "Cancel")
    alert.beginSheetModal(for: webView.window ?? window) { completionHandler($0 == .alertFirstButtonReturn) }
  }

  @available(macOS 12.0, *)
  func webView(_ webView: WKWebView, requestMediaCapturePermissionFor origin: WKSecurityOrigin,
               initiatedByFrame frame: WKFrameInfo, type: WKMediaCaptureType,
               decisionHandler: @escaping (WKPermissionDecision) -> Void) {
    // Voice input on the Studio's own page; macOS still asks the user once.
    decisionHandler(origin.host == "127.0.0.1" && type == .microphone ? .grant : .deny)
  }

  // MARK: downloads, into ~/Downloads

  func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) {
    download.delegate = self
  }

  func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) {
    download.delegate = self
  }

  func download(_ download: WKDownload, decideDestinationUsing response: URLResponse,
                suggestedFilename: String, completionHandler: @escaping (URL?) -> Void) {
    let dir = FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask)[0]
    let name = (suggestedFilename as NSString).lastPathComponent
    let base = (name as NSString).deletingPathExtension, ext = (name as NSString).pathExtension
    var url = dir.appendingPathComponent(name.isEmpty ? "download" : name)
    var n = 2
    while FileManager.default.fileExists(atPath: url.path) {
      url = dir.appendingPathComponent(ext.isEmpty ? "\(base) \(n)" : "\(base) \(n).\(ext)")
      n += 1
    }
    downloads[ObjectIdentifier(download)] = url
    completionHandler(url)
  }

  func downloadDidFinish(_ download: WKDownload) {
    if let url = downloads.removeValue(forKey: ObjectIdentifier(download)) {
      NSWorkspace.shared.activateFileViewerSelecting([url])
    }
  }

  func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
    downloads.removeValue(forKey: ObjectIdentifier(download))
  }

  // MARK: menus

  @objc func reload(_ sender: Any?) { if studio == nil { startServer() } else { webView.reload() } }
  @objc func zoomIn(_ sender: Any?) { webView.pageZoom = min(webView.pageZoom + 0.1, 3) }
  @objc func zoomOut(_ sender: Any?) { webView.pageZoom = max(webView.pageZoom - 0.1, 0.5) }
  @objc func actualSize(_ sender: Any?) { webView.pageZoom = 1 }

  /// The standard About window (the icon, name, version, and the copyright
  /// from Info.plist), with who builds the app as its credits.
  @objc func showAbout(_ sender: Any?) {
    let credits = NSMutableAttributedString(string: "Built and maintained by Parallel Works", attributes: [
      .font: NSFont.systemFont(ofSize: NSFont.smallSystemFontSize),
      .foregroundColor: NSColor.secondaryLabelColor,
    ])
    let name = (credits.string as NSString).range(of: "Parallel Works")
    credits.addAttribute(.link, value: URL(string: "https://parallelworks.com")!, range: name)
    let centered = NSMutableParagraphStyle()
    centered.alignment = .center
    credits.addAttribute(.paragraphStyle, value: centered, range: NSRange(location: 0, length: credits.length))
    NSApp.orderFrontStandardAboutPanel(options: [.credits: credits])
  }

  func buildMenu() {
    let main = NSMenu()
    func submenu(_ title: String, _ items: [NSMenuItem]) {
      let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
      let menu = NSMenu(title: title)
      items.forEach { menu.addItem($0) }
      item.submenu = menu
      main.addItem(item)
    }
    func item(_ title: String, _ action: Selector?, _ key: String = "", target: AnyObject? = nil,
              modifiers: NSEvent.ModifierFlags = .command) -> NSMenuItem {
      let i = NSMenuItem(title: title, action: action, keyEquivalent: key)
      i.keyEquivalentModifierMask = modifiers
      i.target = target
      return i
    }
    submenu("ACTIVATE Studio", [
      item("About ACTIVATE Studio", #selector(showAbout(_:)), target: self),
      .separator(),
      item("Run Setup…", #selector(runSetup(_:)), target: self),
      item("Stop Background Server", #selector(stopServer(_:)), target: self),
      .separator(),
      item("Hide ACTIVATE Studio", #selector(NSApplication.hide(_:)), "h"),
      item("Hide Others", #selector(NSApplication.hideOtherApplications(_:)), "h", modifiers: [.command, .option]),
      item("Show All", #selector(NSApplication.unhideAllApplications(_:))),
      .separator(),
      item("Quit ACTIVATE Studio", #selector(NSApplication.terminate(_:)), "q"),
    ])
    // The standard actions: without them, copy and paste do nothing in the page.
    submenu("Edit", [
      item("Undo", Selector(("undo:")), "z"),
      item("Redo", Selector(("redo:")), "z", modifiers: [.command, .shift]),
      .separator(),
      item("Cut", #selector(NSText.cut(_:)), "x"),
      item("Copy", #selector(NSText.copy(_:)), "c"),
      item("Paste", #selector(NSText.paste(_:)), "v"),
      item("Select All", #selector(NSText.selectAll(_:)), "a"),
    ])
    submenu("View", [
      item("Reload", #selector(reload(_:)), "r", target: self),
      .separator(),
      item("Actual Size", #selector(actualSize(_:)), "0", target: self),
      item("Zoom In", #selector(zoomIn(_:)), "+", target: self),
      item("Zoom Out", #selector(zoomOut(_:)), "-", target: self),
      .separator(),
      item("Enter Full Screen", #selector(NSWindow.toggleFullScreen(_:)), "f", modifiers: [.command, .control]),
    ])
    let windowMenu: [NSMenuItem] = [
      item("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"),
      item("Zoom", #selector(NSWindow.performZoom(_:))),
      item("Close", #selector(NSWindow.performClose(_:)), "w"),
    ]
    submenu("Window", windowMenu)
    NSApp.mainMenu = main
    NSApp.windowsMenu = main.item(withTitle: "Window")?.submenu
  }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
