// The self test's page side (STUDIO_SHELL_TEST=1), appended to the shim.
// On each load of the Studio page it asks the shell which phase it is in
// (TestHello), does that phase's part from inside the page, and reports
// (TestReport); the shell (selftest.go) moves the server and the window
// between phases and writes the results file.
(function () {
  var shell = window.__studioShell;
  if (!shell || !shell.cfg.test || window.__studioShellTest) return;
  window.__studioShellTest = true;
  var now = function () { return performance.timeOrigin + performance.now(); };
  var log = function (m) { shell.call('TestLog', [String(m)]).catch(function () {}); };
  var sleep = function (ms) { return new Promise(function (r) { setTimeout(r, ms); }); };
  var withTimeout = function (p, ms, what) {
    return Promise.race([p, sleep(ms).then(function () { throw new Error(what + ' timed out after ' + ms + ' ms'); })]);
  };

  // Script errors from the start of the load (the Studio's bundle is a
  // module script that can fail before anything is drawn).
  window.__probeErrors = window.__probeErrors || [];
  window.addEventListener('error', function (e) { window.__probeErrors.push(String(e.message) + ' @' + e.filename + ':' + e.lineno); });
  window.addEventListener('unhandledrejection', function (e) { window.__probeErrors.push('rejection: ' + String(e.reason && (e.reason.stack || e.reason))); });

  // Whether the web view's Intl can make what the Studio's bundle makes at
  // load time (new Intl.NumberFormat(undefined, ...)).
  function intl() {
    var out = {};
    var cases = {
      defaultLocale: [undefined, undefined],
      defaultFraction: [undefined, { maximumFractionDigits: 2 }],
      defaultCurrency: [undefined, { style: 'currency', currency: 'USD' }],
      enUS: ['en-US', undefined],
      enUSCompact: ['en-US', { notation: 'compact', maximumFractionDigits: 1 }],
      enUSNumbering: ['en-US', { numberingSystem: 'latn' }],
      percent: [undefined, { style: 'percent' }]
    };
    Object.keys(cases).forEach(function (k) {
      try { out[k] = 'ok ' + new Intl.NumberFormat(cases[k][0], cases[k][1]).format(1234.5); } catch (e) { out[k] = 'error: ' + String(e); }
    });
    try { out.resolvedLocale = new Intl.DateTimeFormat().resolvedOptions().locale; } catch (e) { out.resolvedLocale = 'error: ' + String(e); }
    out.navigatorLanguage = navigator.language;
    out.navigatorLanguages = (navigator.languages || []).join(',');
    return out;
  }

  // Other parts of Intl, one at a time with a log line before each (run
  // late in the checks: when one of these hung the page in a first try).
  function intlOthers() {
    var out = {};
    var tries = {
      toLocaleString: function () { return (1234.5).toLocaleString(); },
      Collator: function () { new Intl.Collator('en-US'); return 'ok'; },
      PluralRules: function () { new Intl.PluralRules('en-US'); return 'ok'; },
      RelativeTimeFormat: function () { new Intl.RelativeTimeFormat('en-US'); return 'ok'; },
      ListFormat: function () { new Intl.ListFormat('en-US'); return 'ok'; },
      DateTimeFormat: function () { new Intl.DateTimeFormat('en-US'); return 'ok'; }
    };
    Object.keys(tries).forEach(function (k) {
      log('intl case ' + k);
      try { out[k] = 'ok ' + tries[k](); } catch (e) { out[k] = 'error: ' + String(e); }
    });
    return out;
  }

  // The Studio's own page replaces the #boot placeholder when its app
  // mounts; a bundle that failed leaves it. Waits up to ms.
  async function appMounted(ms) {
    var t0 = performance.now();
    while (performance.now() - t0 < ms) {
      if (!document.getElementById('boot')) return { mounted: true, afterMs: Math.round(performance.now() - t0) };
      await sleep(100);
    }
    return { mounted: false, afterMs: Math.round(performance.now() - t0) };
  }

  // What web/src/nativeShell.ts does to find the handler.
  function handler() {
    var w = window;
    return (w.webkit && w.webkit.messageHandlers && w.webkit.messageHandlers.studioShell) || null;
  }

  // The Studio has drawn: its root element has content.
  function whenDrawn() {
    return new Promise(function (resolve) {
      (function check() {
        var root = document.getElementById('root');
        if (root && root.children.length > 0 && document.body.innerText.trim().length > 0) resolve(now());
        else setTimeout(check, 25);
      })();
    });
  }

  // A streamed response, read as it arrives: when each chunk came, from
  // the request's start.
  async function readStream(url, init) {
    var t0 = performance.now();
    var res = await fetch(url, Object.assign({ cache: 'no-store' }, init || {}));
    var headersAt = performance.now() - t0;
    var reader = res.body.getReader();
    var chunks = [];
    var text = '';
    var dec = new TextDecoder();
    for (;;) {
      var r = await reader.read();
      if (r.done) break;
      chunks.push({ at: Math.round(performance.now() - t0), bytes: r.value.length });
      text += dec.decode(r.value, { stream: true });
    }
    var events = (text.match(/^data: /gm) || []).length;
    return {
      status: res.status, contentType: res.headers.get('content-type'), headersAtMs: Math.round(headersAt),
      chunks: chunks.length, events: events,
      firstChunkMs: chunks.length ? chunks[0].at : null,
      lastChunkMs: chunks.length ? chunks[chunks.length - 1].at : null,
      arrivals: chunks.map(function (c) { return c.at; }),
      text: text.slice(0, 400),
    };
  }

  // The same stream through EventSource, which the Studio does not use,
  // for comparison.
  function eventSource(url, n) {
    return new Promise(function (resolve) {
      var t0 = performance.now(), times = [], es;
      var done = function (err) { try { es.close(); } catch (_) {} resolve({ events: times.length, arrivals: times, error: err || null }); };
      try { es = new EventSource(url); } catch (e) { resolve({ events: 0, error: String(e) }); return; }
      es.onmessage = function () { times.push(Math.round(performance.now() - t0)); if (times.length >= n) done(); };
      es.onerror = function () { done(times.length >= n ? null : 'error event after ' + times.length + ' events'); };
      setTimeout(function () { done('timeout'); }, 8000);
    });
  }

  function webgl() {
    var out = {};
    ['webgl2', 'webgl'].forEach(function (kind) {
      try {
        var c = document.createElement('canvas');
        var gl = c.getContext(kind);
        if (!gl) { out[kind] = null; return; }
        var info = gl.getExtension('WEBGL_debug_renderer_info');
        out[kind] = {
          vendor: info ? gl.getParameter(info.UNMASKED_VENDOR_WEBGL) : gl.getParameter(gl.VENDOR),
          renderer: info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER),
          version: gl.getParameter(gl.VERSION),
        };
        var strict = document.createElement('canvas').getContext(kind, { failIfMajorPerformanceCaveat: true });
        out[kind].hardware = !!strict;
      } catch (e) { out[kind] = { error: String(e) }; }
    });
    return out;
  }

  // Export CSV's way of saving (web/src/tableExport.ts saveText).
  function saveText(name, text) {
    var url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }));
    var a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 60000);
  }

  function clickLink(href, target) {
    var a = document.createElement('a');
    a.href = href;
    if (target) a.target = target;
    a.textContent = 'test link';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  async function version() {
    try {
      var r = await fetch('/api/version', { cache: 'no-store' });
      return { status: r.status, body: await r.json() };
    } catch (e) { return { error: String(e) }; }
  }

  async function checks(cfg, drawnAt) {
    var out = { drawnAt: drawnAt };
    out.intl = intl();
    out.appMounted = await appMounted(15000);
    out.pageErrors = (window.__probeErrors || []).slice(-8);
    // The bridge, as the web code finds it.
    var h = handler();
    out.bridge = {
      present: !!h, how: shell.how, transport: shell.cfg.transport,
      installedAtMs: Math.round(shell.installedAt), readyStateAtInstall: shell.readyStateAtInstall,
      navigationDomContentLoadedMs: Math.round((performance.getEntriesByType('navigation')[0] || {}).domContentLoadedEventStart || -1),
    };
    if (h) {
      var p = h.postMessage({ action: 'chooseFolder', start: '~', message: 'Choose the folder for the Studio to index.', prompt: 'Use This Folder' });
      out.bridge.returnsPromise = p instanceof Promise;
      try { out.bridge.chooseFolder = await withTimeout(p, 10000, 'chooseFolder'); } catch (e) { out.bridge.chooseFolderError = String(e); }
      try { await h.postMessage({ action: 'nonsense' }); out.bridge.unknownRejects = false; } catch (e) { out.bridge.unknownRejects = String(e.message || e); }
      // The second request opens the real dialog; the shell photographs it
      // and closes it (Escape), so the answer is null.
      try {
        var real = await withTimeout(h.postMessage({ action: 'chooseFolder', start: '~', message: 'Choose the folder for the Studio to index.', prompt: 'Use This Folder' }), 20000, 'real chooseFolder');
        out.bridge.chooseFolderReal = real === null ? 'null (dialog closed)' : real;
      } catch (e) { out.bridge.chooseFolderReal = 'error: ' + String(e); }
      // The bridge still answers after the dialog (and whatever was done to
      // the window while it was open).
      out.bridge.focusAfterDialog = { hasFocus: document.hasFocus(), active: document.activeElement ? document.activeElement.tagName : null };
      try { await h.postMessage({ action: 'nonsense' }); out.bridge.afterDialog = 'resolved'; } catch (e) { out.bridge.afterDialog = String(e.message || e); }
    }
    out.version = await version();
    // Streaming: ten events 200 ms apart, through the same path as the
    // Studio's own requests in this mode.
    try { out.stream = await withTimeout(readStream(cfg.streamURL), 15000, 'stream'); } catch (e) { out.stream = { error: String(e) }; }
    // The same stream fetched straight from its port (cross-origin, CORS),
    // past any proxy, for comparison.
    try { out.streamDirect = await withTimeout(readStream(cfg.directStreamURL), 15000, 'direct stream'); } catch (e) { out.streamDirect = { error: String(e) }; }
    try { out.eventSource = await eventSource(cfg.streamURL, 10); } catch (e) { out.eventSource = { error: String(e) }; }
    // The Studio's own server-sent events: POST /api/chat/stream, which
    // with AI off answers with a content event and a done event.
    try {
      out.chatStream = await withTimeout(readStream('/api/chat/stream', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: [{ role: 'user', content: 'hello' }], model: '' }),
      }), 15000, 'chat stream');
    } catch (e) { out.chatStream = { error: String(e) }; }
    out.intlOthers = intlOthers();
    out.webgl = webgl();
    out.userAgent = navigator.userAgent;
    out.origin = location.origin;
    // Downloads: first with the web view's own handling (the shim stands
    // aside), then through the shim's bridge.
    shell.setDownloads('native');
    saveText(cfg.nativeDownload, 'kind,value\nnative,1\n');
    await sleep(1500);
    shell.setDownloads('bridge');
    saveText(cfg.bridgeDownload, 'kind,value\nbridge,1\n');
    await sleep(1500);
    // Links: a plain link and a target=_blank link to an outside page, and
    // window.open; the window must stay on the Studio.
    var before = location.href;
    clickLink(cfg.externalURL + '?plain');
    clickLink(cfg.externalURL + '?blank', '_blank');
    window.open(cfg.externalURL + '?window-open');
    await sleep(1000);
    out.links = { stayed: location.href === before };
    // window.open with the shim standing aside: does the web view open
    // anything by itself?
    try {
      var w = shell.nativeOpen.call(window, 'about:blank');
      out.links.nativeWindowOpenReturned = w ? 'a window' : String(w);
      if (w) { await sleep(500); try { w.close(); } catch (_) {} }
    } catch (e) { out.links.nativeWindowOpenError = String(e); }
    return out;
  }

  async function run() {
    var drawnAt = await whenDrawn();
    var hello = await shell.call('TestHello', [{ href: location.href, drawnAt: drawnAt, hash: location.hash }]);
    log('phase ' + hello.phase + ' at ' + location.href);
    var data = { href: location.href, hash: location.hash, drawnAt: drawnAt };
    try {
      switch (hello.phase) {
        case 'checks':
          Object.assign(data, await checks(hello, drawnAt));
          break;
        case 'after76':
          data.version = await version();
          break;
        case 'relaunch':
          data.version = await version();
          await shell.call('TestReport', [hello.phase, data]);
          // As web/src/nativeShell.ts reopenApp does.
          log('handler before relaunch: ' + typeof handler() + ', how ' + shell.how);
          var ok = await handler().postMessage({ action: 'relaunch' });
          log('relaunch answered ' + JSON.stringify(ok));
          await shell.call('TestReport', ['relaunchAnswer', { answer: ok }]);
          return;
        case 'move':
          data.version = await version();
          // Settings, General, Server's way (web/src/components/ServerSection.tsx).
          var r = await fetch('/api/setup/port', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ port: hello.movePort }) });
          data.portChange = await r.json();
          data.studioMoved = await handler().postMessage({ action: 'studioMoved', port: hello.movePort, hash: '#view=settings' });
          break;
        case 'idle':
          return;
        default:
          data.version = await version();
      }
    } catch (e) {
      data.error = String(e && e.message || e) + ' | ' + String(e && e.stack || '');
    }
    await shell.call('TestReport', [hello.phase, data]);
  }

  var go = function () { run().catch(function (e) { log('self test failed: ' + (e && e.stack || e)); }); };
  if (document.readyState === 'complete') go(); else window.addEventListener('load', go);
})();
