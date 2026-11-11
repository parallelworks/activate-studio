// The macOS window's bridge, for the Linux and Windows shell: puts
// window.webkit.messageHandlers.studioShell in the page, whose postMessage
// returns a Promise, as WKWebView's handler with a reply does, so
// web/src/nativeShell.ts works unchanged. Also sends links that leave the
// Studio to the default browser and saves downloads in the Downloads
// folder, which Wails v2's web views do not do on their own.
//
// The shell fills in the config placeholder: {test}.
(function () {
  if (window.__studioShell || window.top !== window) return;
  var cfg = __STUDIO_SHELL_CONFIG__;
  var installedAt = performance.now();
  var readyStateAtInstall = document.readyState;

  // Calls reach the shell through Wails' message channel, spoken directly:
  // the page is on http://127.0.0.1, where Wails does not load its runtime.
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
  function call(method, args) {
    return new Promise(function (resolve, reject) {
      var id = 'studio-shell-' + (++seq) + '-' + Math.floor(Math.random() * 1e9);
      pending[id] = { resolve: resolve, reject: reject };
      try {
        post('C' + JSON.stringify({ name: 'main.Bridge.' + method, args: args, callbackID: id }));
      } catch (e) { delete pending[id]; reject(e); }
    });
  }

  // The handler, answering as StudioShell.swift does.
  var handler = {
    postMessage: function (msg) {
      msg = msg || {};
      switch (msg.action) {
        case 'chooseFolder':
          return call('ChooseFolder', [msg.start || '', msg.message || '', msg.prompt || ''])
            .then(function (p) { return p ? p : null; });
        case 'chooseUpload':
          return call('ChooseUpload', [msg.message || '']);
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
  // "external" lives there): add to it, or, if it takes no new names,
  // replace window.webkit with an object that keeps "external" reachable.
  // The handlers object must be kept referenced: on WebKitGTK 4.1 it is a
  // wrapper that the page's JavaScript engine recreates after it has been
  // collected, and a property added to a collected wrapper is gone (the
  // handler was missing a few seconds after load until the shim held on to
  // it). It is kept in window.__studioShell.handlers.
  var how;
  var wk = window.webkit;
  var kept = wk && wk.messageHandlers;
  if (kept) {
    try { kept.studioShell = handler; } catch (e) { /* not extensible */ }
    if (kept.studioShell === handler) {
      how = 'added';
    } else {
      var handlers = { studioShell: handler };
      Object.defineProperty(handlers, 'external', { get: function () { return kept.external; } });
      try {
        Object.defineProperty(window, 'webkit', { value: { messageHandlers: handlers }, configurable: true, writable: true });
        how = 'replaced';
      } catch (e) { how = 'failed: ' + e; }
    }
  } else {
    Object.defineProperty(window, 'webkit', { value: { messageHandlers: { studioShell: handler } }, configurable: true, writable: true });
    how = 'defined';
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
  // The page is told where the file went, or why it did not
  // (studio-shell-saved), and shows a notice; the window shows nothing itself.
  function saved(detail) {
    try { window.dispatchEvent(new CustomEvent('studio-shell-saved', { detail: detail })); } catch (_) { /* an old page */ }
  }
  function save(a, u) {
    var name = a.getAttribute('download') || decodeURIComponent(u.pathname.split('/').pop() || '') || 'download';
    var done = u.protocol === 'blob:' || u.protocol === 'data:'
      ? fetch(u.href).then(function (r) { return r.arrayBuffer(); })
        .then(function (buf) { return call('SaveDownload', [name, toBase64(buf)]); })
      : call('SaveURL', [u.pathname + u.search, name]);
    return done.then(function (path) { saved({ name: name, path: path }); return path; },
      function (err) { saved({ name: name, error: String(err && err.message || err) }); throw err; });
  }
  // The web view's own menu carries Back and Reload, which mean nothing in an
  // app window. It stays where it has a use: copy and paste in text.
  window.addEventListener('contextmenu', function (e) {
    var t = e.target;
    var editable = t && t.closest && t.closest('input, textarea, [contenteditable]:not([contenteditable="false"])');
    var selected = String(window.getSelection && window.getSelection() || '').length > 0;
    if (!editable && !selected) e.preventDefault();
  });
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

  // A window with no title bar of its own (the Mac's): the window controls
  // sit over the page's top row. html.shell-inset, --shell-top (the row's
  // height) and --shell-controls (how far in the controls reach) tell the
  // page to put each column's first row on it and keep the controls' corner
  // clear; a press on an empty part of the row
  // drags the window, and a double click zooms it, as a title bar does.
  // Anything that takes a click is not empty: a control by its element or
  // role, or anything with a pointer or resize cursor, as the page gives
  // whatever it makes clickable.
  //
  // With a classification banner the window takes its compact layout: the
  // controls at a plain title bar's height, over the banner, which is that
  // strip. The page marks the banner on the html element (data-banner,
  // shown or none) once its settings are read; the shim tells the shell,
  // which adds or removes its toolbar row and answers the new height and
  // reach. Until the page says, the layout is the one the last launch
  // ended with (cfg.titleCompact), so a launch does not jump.
  //
  // The row's height and reach are in the window's points. With the window
  // zoomed by z (zoom.go: the web view's page zoom) a page px is z points,
  // so they are divided by the zoom: the row stays level with the window
  // controls, which do not zoom. The row keeps at least 36 px (or its own
  // height, if less) so the buttons in it still fit at the larger zooms.
  var zoom = 1;
  var rowAt = null; // the row as last set, in points: [top, controls]
  var inset = Number(cfg.titleInset) || 0;
  var root = document.documentElement;
  if (inset > 0) {
    var setRow = function (top, controls) {
      rowAt = [top, controls];
      inset = Math.max(top / zoom, Math.min(top, 36));
      root.style.setProperty('--shell-top', inset + 'px');
      root.style.setProperty('--shell-controls', (controls / zoom) + 'px');
    };
    root.classList.add('shell-inset');
    setRow(inset, Number(cfg.titleControls) || 0);
    var compact = !!cfg.titleCompact;
    var followBanner = function () {
      var b = root.getAttribute('data-banner');
      if (b !== 'shown' && b !== 'none') return;
      var want = b === 'shown';
      if (want === compact) return;
      compact = want;
      call('SetCompactTitle', [want]).then(function (m) {
        if (m && m.titleInset > 0 && compact === want) setRow(m.titleInset, m.titleControls || 0);
      }).catch(function () { /* the layout stays as it was */ });
    };
    new MutationObserver(followBanner).observe(root, { attributes: true, attributeFilter: ['data-banner'] });
    var CONTROL = 'button, a, input, select, textarea, label, summary, [role="button"], [role="tab"], [role="link"],'
      + ' [role="menuitem"], [role="option"], [role="treeitem"], [role="checkbox"], [role="switch"], [role="slider"],'
      + ' [contenteditable="true"], [draggable="true"], [tabindex]:not([tabindex="-1"])';
    var onControl = function (el) {
      if (!el || el.nodeType !== 1) return false;
      if (el.closest && el.closest(CONTROL)) return true;
      var cursor = '';
      try { cursor = getComputedStyle(el).cursor; } catch (err) { /* treat as empty */ }
      return !!cursor && cursor !== 'auto' && cursor !== 'default';
    };
    var inRow = function (e) { return e.clientY >= 0 && e.clientY < inset && !onControl(e.target); };
    window.addEventListener('mousedown', function (e) {
      if (e.button !== 0 || !inRow(e)) return;
      try { post('drag'); } catch (err) { /* the window stays where it is */ }
    }, true);
    window.addEventListener('dblclick', function (e) {
      if (!inRow(e)) return;
      call('ToggleMaximise', []).catch(function () { /* the window stays as it is */ });
    }, true);
  }

  // The window's title bar follows the page's theme-color tag, as the macOS
  // window's does (titlebar.go). The tag holds whatever CSS color the theme
  // uses; a canvas turns it into #rrggbb.
  var bar = document.createElement('canvas').getContext('2d');
  function reportBar(tag) {
    var color = tag.getAttribute('content');
    if (!color || !bar) return;
    bar.fillStyle = '#000000';
    bar.fillStyle = color;
    if (/^#[0-9a-f]{6}$/i.test(bar.fillStyle)) call('SetBarColor', [bar.fillStyle]).catch(function () { /* the bar stays as it is */ });
  }
  function followBar() {
    var tag = document.querySelector('meta[name="theme-color"]');
    if (!tag) return;
    var again = function () { reportBar(tag); };
    again();
    new MutationObserver(again).observe(tag, { attributes: true, attributeFilter: ['content'] });
    // Reported again once the page has loaded, a moment later, and when the
    // window comes forward: a report sent before the window was ready to
    // take it would otherwise leave the bar in the last launch's color
    // until the theme changed.
    window.addEventListener('load', again);
    window.addEventListener('focus', again);
    document.addEventListener('visibilitychange', function () { if (!document.hidden) again(); });
    setTimeout(again, 1500);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', followBar); else followBar();

  // The window's zoom (zoom.go). The shell says the level when it changes
  // (setZoom); a page that has just loaded asks, which on Linux also applies
  // the level saved by the last launch. Where the window has no menu
  // (cfg.zoomKeys, Linux) Ctrl+=, Ctrl+- and Ctrl+0 change it, as in a
  // browser.
  function setZoom(z) {
    z = Number(z);
    if (!(z > 0)) return;
    zoom = z;
    if (rowAt) setRow(rowAt[0], rowAt[1]);
  }
  call('Zoom', ['']).then(setZoom, function () { /* an older shell: no zoom */ });
  if (cfg.zoomKeys) {
    window.addEventListener('keydown', function (e) {
      if (!e.ctrlKey || e.altKey || e.metaKey) return;
      var op = e.key === '=' || e.key === '+' ? 'in' : e.key === '-' || e.key === '_' ? 'out' : e.key === '0' ? 'reset' : '';
      if (!op) return;
      e.preventDefault();
      e.stopPropagation();
      call('Zoom', [op]).then(setZoom, function () { /* the zoom stays as it was */ });
    }, true);
  }

  window.__studioShell = {
    cfg: cfg,
    call: call,
    how: how,
    handlers: kept,
    installedAt: installedAt,
    readyStateAtInstall: readyStateAtInstall,
    nativeOpen: nativeOpen,
    setDownloads: function (mode) { downloads = mode; },
    setZoom: setZoom,
    zoom: function () { return zoom; },
    fileDrop: function (x, y, files) {
      // The drop's place is in the window's points; the page's px are
      // zoom points each.
      x = x / zoom;
      y = y / zoom;
      // The page saw the drag arrive but not this drop, so its highlights
      // would stay on.
      var over = document.elementFromPoint(x, y);
      if (over) over.dispatchEvent(new DragEvent('dragleave', { bubbles: true }));
      window.dispatchEvent(new CustomEvent('studio-shell-drop', { detail: { x: x, y: y, files: files } }));
    },
  };
})();
