// Gives the page the macOS app's message handler,
// window.webkit.messageHandlers.studioShell.postMessage({action, ...}),
// returning a Promise (web/src/nativeShell.ts), over Tauri's IPC: one app
// command, studio_shell, which the Studio's origin may call (see the
// run-time capability in main.rs). Runs before the page's own scripts on
// every load of the main frame.
(() => {
  const handler = Object.freeze({
    postMessage(message) {
      const internals = window.__TAURI_INTERNALS__
      if (!internals) return Promise.reject(new Error('the shell bridge is not available'))
      return internals.invoke('studio_shell', { msg: message ?? null })
        .catch(e => { throw new Error(typeof e === 'string' ? e : String(e && e.message || e)) })
    },
  })
  let how
  const existing = window.webkit
  if (existing && existing.messageHandlers) {
    // WebKitGTK has its own window.webkit, whose messageHandlers carries
    // the ipc handler wry's IPC falls back to, so it stays reachable. A
    // property added to that native object does not last: its JavaScript
    // wrapper can be collected and made again without it (seen in the
    // smoke test after ten idle seconds). A proxy, held by window, does.
    const native = existing.messageHandlers
    const handlers = new Proxy(native, {
      get: (t, p) => (p === 'studioShell' ? handler : t[p]),
      has: (t, p) => p === 'studioShell' || p in t,
    })
    const webkit = new Proxy(existing, { get: (t, p) => (p === 'messageHandlers' ? handlers : t[p]) })
    try {
      Object.defineProperty(window, 'webkit', { value: webkit, configurable: true, writable: false })
      how = window.webkit.messageHandlers.studioShell === handler ? 'proxy over the native window.webkit' : 'proxy not in effect'
    } catch (e) {
      // Fallback: the property on the native object, whose wrapper is kept
      // alive by a reference from window so the property lasts.
      Object.defineProperty(window, '__studioShellNative', { value: native })
      try { native.studioShell = handler } catch { /* read-only */ }
      how = native.studioShell === handler ? `expando (window.webkit not replaceable: ${e && e.message})` : `not installed: ${e && e.message}`
    }
  } else {
    Object.defineProperty(window, 'webkit', {
      value: Object.freeze({ messageHandlers: Object.freeze({ studioShell: handler }) }),
      configurable: true,
      writable: false,
    })
    how = 'defined'
  }
  Object.defineProperty(window, '__studioShellBridge', { value: how })
})()
