// The macOS window's bridge, for the Linux and Windows shell: puts
// window.webkit.messageHandlers.studioShell in the page, whose postMessage
// returns a Promise, as WKWebView's handler with a reply does, so
// web/src/nativeShell.ts works unchanged. Also sends links that leave the
// Studio to the default browser and saves downloads in the Downloads
// folder, which Wails v2's web views do not do on their own.
//
// The shell fills in the config placeholder: {mode, transport, token, test}.
// transport is how calls reach the shell:
//   wails  Wails' bindings, window.go.main.Bridge (the page is on Wails' origin)
//   ipc    Wails' message channel spoken directly (the page is on
//          http://127.0.0.1, where Wails does not load its runtime)
//   http   POST /__shell/bridge on the shell's own localhost proxy
(function () {
  if (window.__studioShell || window.top !== window) return;
  var cfg = __STUDIO_SHELL_CONFIG__;
  var installedAt = performance.now();
  var readyStateAtInstall = document.readyState;

  var call;
  if (cfg.transport === 'wails') {
    call = function (method, args) {
      var b = window.go && window.go.main && window.go.main.Bridge;
      if (!b || !b[method]) return Promise.reject(new Error('the shell is not available'));
      return b[method].apply(null, args);
    };
  } else if (cfg.transport === 'ipc') {
    // Wails' protocol: 'C' + {name, args, callbackID} to the shell, which
    // answers by running window.wails.Callback({callbackid, result, error}).
    var pending = {};
    var seq = 0;
    var wailsHandler = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.external;
    var post = window.chrome && window.chrome.webview
      ? function (s) { window.chrome.webview.postMessage(s); }
      : function (s) { wailsHandler.postMessage(s); };
    window.wails = window.wails || {};
    var earlier = window.wails.Callback;
    window.wails.Callback = function (json) {
      var m = JSON.parse(json);
      var p = pending[m.callbackid];
      if (!p) { if (earlier) earlier(json); return; }
      delete pending[m.callbackid];
      if (m.error) p.reject(new Error(String(m.error))); else p.resolve(m.result);
    };
    call = function (method, args) {
      return new Promise(function (resolve, reject) {
        var id = 'studio-shell-' + (++seq) + '-' + Math.floor(Math.random() * 1e9);
        pending[id] = { resolve: resolve, reject: reject };
        try {
          post('C' + JSON.stringify({ name: 'main.Bridge.' + method, args: args, callbackID: id }));
        } catch (e) { delete pending[id]; reject(e); }
      });
    };
  } else {
    call = function (method, args) {
      return fetch('/__shell/bridge', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-Studio-Shell-Token': cfg.token },
        body: JSON.stringify({ method: method, args: args }),
      }).then(function (r) { return r.json(); }).then(function (j) {
        if (j.error) throw new Error(j.error);
        return j.result;
      });
    };
  }

  // The handler, answering as StudioShell.swift does.
  var handler = {
    postMessage: function (msg) {
      msg = msg || {};
      switch (msg.action) {
        case 'chooseFolder':
          return call('ChooseFolder', [msg.start || '', msg.message || '', msg.prompt || ''])
            .then(function (p) { return p ? p : null; });
        case 'studioMoved':
          return call('StudioMoved', [Number(msg.port), String(msg.hash || '')]);
        case 'relaunch':
          return call('Relaunch', []);
        default:
          return Promise.reject(new Error('unknown request ' + msg.action));
      }
    },
  };

  // Into window.webkit.messageHandlers. WebKitGTK has one already (Wails'
  // "external" lives there). An expando on that WebKit-owned object did not
  // last on WebKitGTK 4.1 (the property was gone a few seconds later), so
  // window.webkit is replaced by an object of ours that keeps "external"
  // reachable through a getter; only if window.webkit cannot be replaced is
  // the expando used.
  var how;
  var original = window.webkit && window.webkit.messageHandlers;
  var handlers = { studioShell: handler };
  if (original) Object.defineProperty(handlers, 'external', { get: function () { return original.external; }, enumerable: true });
  try {
    Object.defineProperty(window, 'webkit', { value: { messageHandlers: handlers }, configurable: true, writable: true });
    how = original ? 'replaced' : 'defined';
  } catch (e) {
    try { original.studioShell = handler; } catch (_) { /* not extensible */ }
    how = original && original.studioShell === handler ? 'added' : 'failed: ' + e;
  }

  // Links that leave the Studio open in the default browser; the window
  // stays on the Studio. Checked after the page's own handlers, so a link
  // the page handles itself (preventDefault) is left alone.
  var downloads = 'bridge';
  function isStudio(u) { return u.origin === location.origin; }
  function leavesStudio(u) { return /^(https?|mailto):$/.test(u.protocol) && !isStudio(u); }
  function toBase64(buf) {
    var bytes = new Uint8Array(buf), s = '', step = 0x8000;
    for (var i = 0; i < bytes.length; i += step) s += String.fromCharCode.apply(null, bytes.subarray(i, i + step));
    return btoa(s);
  }
  function save(a, u) {
    var name = a.getAttribute('download') || decodeURIComponent(u.pathname.split('/').pop() || '') || 'download';
    if (u.protocol === 'blob:' || u.protocol === 'data:') {
      return fetch(u.href).then(function (r) { return r.arrayBuffer(); })
        .then(function (buf) { return call('SaveDownload', [name, toBase64(buf)]); });
    }
    return call('SaveURL', [u.pathname + u.search, name]);
  }
  window.addEventListener('click', function (e) {
    if (e.defaultPrevented || e.button !== 0) return;
    var a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
    if (!a) return;
    var u;
    try { u = new URL(a.href, location.href); } catch (_) { return; }
    if (a.hasAttribute('download') && downloads === 'bridge' && (isStudio(u) || u.protocol === 'blob:' || u.protocol === 'data:')) {
      e.preventDefault();
      save(a, u).catch(function (err) { console.error('download failed', err); });
      return;
    }
    if (leavesStudio(u)) {
      e.preventDefault();
      call('OpenExternal', [u.href]).catch(function (err) { console.error(err); });
    }
  });
  var nativeOpen = window.open;
  window.open = function (url) {
    try {
      var u = new URL(url, location.href);
      if (leavesStudio(u)) { call('OpenExternal', [u.href]); return null; }
    } catch (_) { /* let the web view decide */ }
    return nativeOpen.apply(window, arguments);
  };

  window.__studioShell = {
    cfg: cfg,
    call: call,
    how: how,
    installedAt: installedAt,
    readyStateAtInstall: readyStateAtInstall,
    nativeOpen: nativeOpen,
    setDownloads: function (mode) { downloads = mode; },
  };
})();
