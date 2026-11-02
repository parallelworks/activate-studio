// The shell's smoke test, from inside the Studio's page. Injected only when
// the shell runs with STUDIO_SMOKE=1. The shell keeps the phase across
// reloads, restarts and the relaunch; each phase records its results through
// the `smoke` command, which the shell writes to a JSON file.
(() => {
  if (location.protocol !== 'http:' || location.hostname !== '127.0.0.1') return
  const internals = () => window.__TAURI_INTERNALS__
  const inv = (op, data = {}) => internals().invoke('smoke', { op, data })
  const rec = (key, value) => inv('record', { key, value })
  const sleep = ms => new Promise(r => setTimeout(r, ms))
  const now = () => performance.now()
  const shell = () => window.webkit.messageHandlers.studioShell
  const msg = e => String((e && e.message) || e)
  const timeout = (p, ms, what) => Promise.race([p, sleep(ms).then(() => { throw new Error(`${what}: no answer within ${ms} ms`) })])

  async function step(name, fn) {
    const t = now()
    try {
      const v = (await fn()) || {}
      await rec(name, { ok: v.ok !== false, ms: Math.round(now() - t), ...v })
    } catch (e) {
      await rec(name, { ok: false, ms: Math.round(now() - t), error: msg(e) })
    }
  }

  // Drawn: React has replaced the boot placeholder in #root, and two frames
  // have gone by.
  async function drawn() {
    if (document.readyState === 'loading') await new Promise(r => addEventListener('DOMContentLoaded', r, { once: true }))
    for (let i = 0; i < 1200; i++) {
      const root = document.getElementById('root')
      if (root && root.children.length && !document.getElementById('boot')) break
      await sleep(25)
    }
    await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))
  }

  // Chunk arrival times against the times the stub sent them.
  function summarize(arrivals, expected) {
    const lat = arrivals.map(a => a.at - a.sent)
    const spread = arrivals.length > 1 ? arrivals[arrivals.length - 1].at - arrivals[0].at : 0
    return {
      chunks: arrivals.length,
      expected,
      firstChunkMs: arrivals.length ? Math.round(arrivals[0].rel) : null,
      spreadMs: Math.round(spread),
      maxLatencyMs: lat.length ? Math.max(...lat) : null,
      meanLatencyMs: lat.length ? Math.round(lat.reduce((a, b) => a + b, 0) / lat.length) : null,
      // Each piece reached the page within 150 ms of being sent: not held
      // back until the end.
      incremental: arrivals.length >= expected && lat.every(l => l < 150),
    }
  }

  async function fetchStream(base, path, type, n, delay) {
    const t0 = now()
    const res = await fetch(`${base}${path}?n=${n}&delay=${delay}&type=${encodeURIComponent(type)}`)
    const reader = res.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    const arrivals = []
    let reads = 0
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      reads++
      buf += dec.decode(value, { stream: true })
      let m
      while ((m = /chunk (\d+) (\d+)\n/.exec(buf))) {
        arrivals.push({ i: Number(m[1]), sent: Number(m[2]), at: Date.now(), rel: now() - t0 })
        buf = buf.slice(m.index + m[0].length)
      }
    }
    return { contentType: res.headers.get('content-type'), reads, ...summarize(arrivals, n) }
  }

  function eventSource(base, n, delay) {
    return new Promise((resolve, reject) => {
      const t0 = now()
      const arrivals = []
      const es = new EventSource(`${base}/sse?n=${n}&delay=${delay}`)
      const stop = setTimeout(() => { es.close(); reject(new Error(`EventSource: ${arrivals.length} of ${n} events in 20 s`)) }, 20000)
      es.onmessage = e => {
        const d = JSON.parse(e.data)
        arrivals.push({ i: d.i, sent: d.t, at: Date.now(), rel: now() - t0 })
      }
      es.addEventListener('end', () => { clearTimeout(stop); es.close(); resolve(summarize(arrivals, n)) })
    })
  }

  function webgl() {
    const out = {}
    for (const kind of ['webgl2', 'webgl']) {
      const c = document.createElement('canvas')
      c.width = c.height = 4
      const gl = c.getContext(kind)
      if (!gl) { out[kind] = false; continue }
      const info = gl.getExtension('WEBGL_debug_renderer_info')
      gl.clearColor(0, 1, 0, 1)
      gl.clear(gl.COLOR_BUFFER_BIT)
      const px = new Uint8Array(4)
      gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px)
      out[kind] = {
        renderer: (info && gl.getParameter(info.UNMASKED_RENDERER_WGL)) || gl.getParameter(gl.RENDERER),
        vendor: (info && gl.getParameter(info.UNMASKED_VENDOR_WGL)) || gl.getParameter(gl.VENDOR),
        version: gl.getParameter(gl.VERSION),
        drewGreen: px[1] === 255 && px[0] === 0,
      }
    }
    out.ok = !!(out.webgl2 || out.webgl)
    return out
  }

  async function poll(op, ready, ms) {
    const end = Date.now() + ms
    let v
    while (Date.now() < end) {
      v = await inv(op)
      if (ready(v)) return v
      await sleep(200)
    }
    return v
  }

  async function phaseA(st) {
    await inv('checkpoint', { name: 'studio' })

    await step('bridge', async () => {
      const h = shell()
      const p = h.postMessage({ action: 'no-such-action' })
      const isPromise = p instanceof Promise
      let rejection = null
      try { await p } catch (e) { rejection = msg(e) }
      return {
        ok: isPromise && /unknown request/.test(rejection || ''),
        how: window.__studioShellBridge,
        postMessageIsFunction: typeof h.postMessage === 'function',
        returnsPromise: isPromise,
        rejection,
        nativeShellCheck: !!(window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.studioShell),
      }
    })

    await step('chooseFolder_testMode', async () => {
      const picked = await shell().postMessage({ action: 'chooseFolder', start: '~', message: 'Choose the folder for the Studio to index.', prompt: 'Use This Folder' })
      return { ok: picked === st.testFolder, picked, expected: st.testFolder }
    })

    if (st.realDialog) {
      await step('chooseFolder_realDialog', async () => {
        // The shell opens the system dialog; CI takes a screenshot and
        // presses Escape, which cancels it: null.
        const picked = await timeout(shell().postMessage({ action: 'chooseFolder', start: '~', message: st.dialogTitle, smokeRealDialog: true }), 60000, 'chooseFolder')
        return { ok: picked === null, picked }
      })
    }

    const n = 10, delay = 200
    await step('stream_fetch_eventstream', () => fetchStream(st.stub, '/stream', 'text/event-stream', n, delay))
    await step('stream_fetch_plain_nosniff', () => fetchStream(st.stub, '/stream', 'text/plain;nosniff', n, delay))
    await step('stream_fetch_plain', () => fetchStream(st.stub, '/stream', 'text/plain', n, delay))
    await step('stream_eventsource', () => eventSource(st.stub, n, delay))

    await step('download', async () => {
      const text = `studio smoke download ${Date.now()}\n`
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
      const a = document.createElement('a')
      a.href = url
      a.download = 'studio-smoke-download.txt'
      document.body.appendChild(a)
      a.click()
      a.remove()
      const d = await poll('downloads', v => v.some(x => x.event === 'finished'), 15000)
      const fin = d.find(x => x.event === 'finished')
      return { ok: !!(fin && fin.success && fin.content === text), events: d, expectedBytes: text.length }
    })

    await step('external_links', async () => {
      // The ways the Studio's pages link outside: a plain anchor, anchors
      // with target=_blank (with and without rel=noopener, as the Studio's
      // own links have), and window.open. These are script-made clicks, so a
      // popup blocker may treat them differently from a person's click.
      const before = location.href
      const variants = {}
      const anchor = (name, attrs) => {
        const a = document.createElement('a')
        a.href = `https://example.com/?studio-smoke=${name}`
        for (const [k, v] of Object.entries(attrs)) a.setAttribute(k, v)
        document.body.appendChild(a)
        a.click()
        a.remove()
      }
      anchor('link', {})
      await sleep(800)
      anchor('blank', { target: '_blank' })
      await sleep(800)
      anchor('noopener', { target: '_blank', rel: 'noopener noreferrer' })
      await sleep(800)
      const w = window.open('https://example.com/?studio-smoke=open', '_blank')
      const w2 = window.open('https://example.com/?studio-smoke=openNoopener', '_blank', 'noopener')
      const want = ['link', 'blank', 'noopener', 'open', 'openNoopener']
      const opened = await poll('opened', v => v.length >= want.length, 6000)
      for (const n of want) variants[n] = opened.some(o => o.ok && o.url.endsWith(`studio-smoke=${n}`))
      // What the Studio uses: target=_blank with rel=noopener, and window.open.
      const required = ['link', 'noopener', 'open']
      return {
        ok: required.every(n => variants[n]) && location.href === before,
        variants,
        stayed: location.href === before,
        windowOpenReturned: [w, w2].map(x => (x === null ? null : typeof x)),
        opened,
      }
    })

    await step('webgl', async () => webgl())

    // Supervision: the server exits with 76, as after an update; the shell
    // starts it again and reloads this page (phase B).
    await inv('set_phase', { phase: 'B' })
    await inv('server_exit', { code: 76 })
  }

  async function phaseB(st) {
    await rec('restart_76', {
      ok: !!st.previousServerPid && st.serverPid !== st.previousServerPid,
      serverPid: st.serverPid,
      previousServerPid: st.previousServerPid,
      exitToDrawnMs: st.exitToDrawnMs,
      earlierChildrenAlive: st.earlierChildrenAlive,
    })
    // Any other exit: the shell shows its error page with Try Again; in the
    // smoke test it presses that button itself (phase C).
    await inv('set_phase', { phase: 'C' })
    await inv('server_exit', { code: 1 })
  }

  async function phaseC(st) {
    await rec('exit_other_retry', {
      ok: !!st.previousServerPid && st.serverPid !== st.previousServerPid && st.sawFailurePage,
      serverPid: st.serverPid,
      previousServerPid: st.previousServerPid,
      sawFailurePage: st.sawFailurePage,
      failureText: st.failureText,
      exitToDrawnMs: st.exitToDrawnMs,
      earlierChildrenAlive: st.earlierChildrenAlive,
    })
    // studioMoved as the Studio's Server settings do it: save a new port
    // (the server exits 75 to be started on it), then ask the window to
    // follow (phase D).
    await step('port_change', async () => {
      const r = await fetch('/api/setup/port', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ port: st.nextPort }),
      })
      const body = await r.json()
      await inv('set_phase', { phase: 'D' })
      const moved = await shell().postMessage({ action: 'studioMoved', port: st.nextPort, hash: '#smoke-moved' })
      return { ok: r.ok && body.restarting === true && moved === true, status: r.status, body, studioMovedReply: moved, to: st.nextPort }
    })
  }

  async function phaseD(st) {
    await step('studio_moved', async () => {
      let rejection = null
      try { await shell().postMessage({ action: 'no-such-action' }) } catch (e) { rejection = msg(e) }
      return {
        ok: Number(location.port) === st.expectedPort && /unknown request/.test(rejection || ''),
        href: location.href,
        port: Number(location.port),
        expectedPort: st.expectedPort,
        hashKept: location.hash === '#smoke-moved',
        bridgeOnNewPort: rejection,
      }
    })
    await inv('checkpoint', { name: 'moved' })
    await step('idle', () => inv('idle_measure'))
    // relaunch, through the bridge: the shell stops the server, starts a
    // new copy of itself and exits; the new copy runs phase R.
    const r = await shell().postMessage({ action: 'relaunch' })
    await rec('relaunch_reply', r)
  }

  async function phaseR() {
    await inv('checkpoint', { name: 'relaunched' })
    await step('idle', () => inv('idle_measure'))
    // Close the window as a person would; the shell stops the server and
    // everything it started, then exits.
    await inv('finish', { close: true })
  }

  async function main() {
    await drawn()
    const st = await inv('drawn', { port: Number(location.port), href: location.href })
    if (st.phase === 'A') await phaseA(st)
    else if (st.phase === 'B') await phaseB(st)
    else if (st.phase === 'C') await phaseC(st)
    else if (st.phase === 'D') await phaseD(st)
    else if (st.phase === 'R') await phaseR(st)
    // T and K: drawn is all; CI ends the shell from outside.
  }

  main().catch(e => inv('record', { key: `fatal_${Date.now()}`, value: String((e && e.stack) || e) }).catch(() => {}))
})()
