// The install check's page side (STUDIO_SHELL_TEST=1), appended to the shim.
// On each load of the Studio page it asks the shell which phase it is in
// (TestHello), does that phase's part from inside the page, and reports
// (TestReport); the shell (selftest.go) moves the server and the window
// between phases and writes the results file.
(function () {
  var shell = window.__studioShell;
  if (!shell || !shell.cfg.test || window.__studioShellTest) return;
  window.__studioShellTest = true;
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

  // The Studio's own page replaces the #boot placeholder when its app
  // mounts; a bundle that failed leaves it. Waits up to ms.
  async function appMounted(ms) {
    var t0 = performance.now();
    while (performance.now() - t0 < ms) {
      var root = document.getElementById('root');
      if (!document.getElementById('boot') && root && root.children.length > 0) return { mounted: true, afterMs: Math.round(performance.now() - t0) };
      await sleep(100);
    }
    return { mounted: false, afterMs: Math.round(performance.now() - t0), text: (document.body ? document.body.innerText : '').slice(0, 200) };
  }

  // What web/src/nativeShell.ts does to find the handler.
  function handler() {
    var w = window;
    return (w.webkit && w.webkit.messageHandlers && w.webkit.messageHandlers.studioShell) || null;
  }

  async function version() {
    try {
      var r = await fetch('/api/version', { cache: 'no-store' });
      return { status: r.status, body: await r.json() };
    } catch (e) { return { error: String(e) }; }
  }

  async function json(method, url, body) {
    var r = await fetch(url, { method: method, cache: 'no-store', headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
    return { status: r.status, body: await r.json().catch(function () { return null; }) };
  }

  // A full index of the documents folder, then a search for a word that
  // only one file has. The search goes through the shell's proxy, as the
  // page's own requests do.
  async function search(word, file) {
    var out = { word: word, file: file, found: false };
    if (!word) { out.skipped = true; return out; }
    out.reindex = await json('POST', '/api/reindex', {});
    var t0 = performance.now();
    while (performance.now() - t0 < 240000) {
      var r = await json('GET', '/api/search?q=' + encodeURIComponent(word));
      var hits = (r.body && (r.body.results || r.body.hits)) || [];
      if (Array.isArray(hits) && hits.some(function (h) { return h && h.name === file; })) { out.found = true; out.afterMs = Math.round(performance.now() - t0); return out; }
      await sleep(1500);
    }
    out.afterMs = Math.round(performance.now() - t0);
    return out;
  }

  async function checks(hello) {
    var out = {};
    out.appMounted = await appMounted(90000);
    out.pageErrors = (window.__probeErrors || []).slice(-8);
    out.userAgent = navigator.userAgent;
    out.origin = location.origin;
    var h = handler();
    out.bridge = { present: !!h, how: shell.how };
    if (h) {
      var p = h.postMessage({ action: 'chooseFolder', start: '~', message: 'Choose the folder for the Studio to index.', prompt: 'Use This Folder' });
      out.bridge.returnsPromise = p instanceof Promise;
      try { out.bridge.chooseFolder = await withTimeout(p, 10000, 'chooseFolder'); } catch (e) { out.bridge.chooseFolderError = String(e); }
      try { await h.postMessage({ action: 'nonsense' }); out.bridge.unknownRejects = false; } catch (e) { out.bridge.unknownRejects = String(e.message || e); }
    }
    out.version = await version();
    out.search = await search(hello.searchWord, hello.searchFile);
    return out;
  }

  async function run() {
    var hello = await shell.call('TestHello', [{ href: location.href, hash: location.hash }]);
    log('phase ' + hello.phase + ' at ' + location.href);
    var data = { href: location.href, hash: location.hash };
    try {
      switch (hello.phase) {
        case 'checks':
          Object.assign(data, await checks(hello));
          break;
        case 'relaunch':
          data.appMounted = await appMounted(90000);
          data.version = await version();
          await shell.call('TestReport', [hello.phase, data]);
          // As web/src/nativeShell.ts reopenApp does.
          var ok = await handler().postMessage({ action: 'relaunch' });
          log('relaunch answered ' + JSON.stringify(ok));
          await shell.call('TestReport', ['relaunchAnswer', { answer: ok }]);
          return;
        case 'move':
          data.appMounted = await appMounted(90000);
          data.version = await version();
          // Settings, Server's way (web/src/components/ServerSection.tsx).
          var r = await json('POST', '/api/setup/port', { port: hello.movePort });
          data.portChange = r.body;
          data.studioMoved = await handler().postMessage({ action: 'studioMoved', port: hello.movePort, hash: '#view=settings' });
          break;
        case 'idle':
          return;
        default:
          data.appMounted = await appMounted(90000);
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
