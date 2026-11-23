// The footer's update rows at the narrowest and widest sidebar, light and
// dark, in Chromium and WebKit, with what each row's text shows and needs.
//   node footer/update-rows.mjs <base> <outdir> <label>
// States: reopen (an update changed the app's window; the window can reopen),
// reload (a newer build was deployed while the page was open), both, and
// browser (reopen, in a browser, which cannot reopen the app).
import { chromium, webkit } from 'playwright'
import fs from 'node:fs'
const [BASE, OUT, LABEL] = process.argv.slice(2)
fs.mkdirSync(OUT, { recursive: true })
const status = reopen => ({
  mode: 'app', current: { version: 'v1.203.23', builtAt: '2026-11-23T18:00:00Z' }, policy: { check: true, install: 'auto', channel: 'stable' },
  latest: null, releases: [], available: false, canInstall: true, reason: null, command: null, commandNote: null, package: null, state: 'idle', progress: null, error: null,
  checkedAt: '2026-11-23T02:00:00Z', offline: false, previous: null, reopen, versions: [], moveToStable: null, stateFormat: 1, snapshots: [],
})
const report = []
for (const [ename, engine] of [['chromium', chromium], ['webkit', webkit]]) {
  const b = await engine.launch()
  for (const state of (process.env.ROW_STATES ?? 'reopen,reload,both,browser').split(',')) for (const w of [200, 400]) for (const theme of ['light', 'dark']) {
    const ctx = await b.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block', colorScheme: theme })
    const page = await ctx.newPage()
    const reopen = state !== 'reload'
    const browser = state === 'browser'
    await page.route('**/api/updates', r => r.fulfill({ json: status(reopen) }))
    await page.route('**/api/updates/check', r => r.fulfill({ json: status(reopen) }))
    let calls = 0
    await page.route('**/api/version', async r => {
      const v = await (await r.fetch()).json()
      calls++
      return r.fulfill({ json: (state === 'reload' || state === 'both') && calls > 1 ? { ...v, version: 'v1.203.24', commit: 'b'.repeat(40) } : { ...v, version: 'v1.203.23' } })
    })
    await page.addInitScript(([t, browser]) => {
      try { localStorage.setItem('theme', t); localStorage.setItem('ade-seen-version', 'v1.203.23') } catch { /* none */ }
      // The app's window, which can reopen; "browser" is a page in a browser.
      if (!browser) window.webkit = { messageHandlers: { studioShell: { postMessage: async () => true } } }
    }, [theme, browser])
    await page.goto(`${BASE}/#view=chat`, { waitUntil: 'networkidle' })
    await page.addStyleTag({ content: `:root { --nav-w: ${w}px !important; }` })
    await page.waitForTimeout(800)
    await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
    await page.waitForTimeout(1200)
    const rows = await page.locator('.sidenav-footer [data-testid^="footer-re"]').evaluateAll(es => es.map(e => {
      const t = e.querySelector('.identity-text')
      return { row: e.dataset.testid, text: e.innerText.replace(/\s+/g, ' '), cut: t ? t.scrollWidth > t.clientWidth : null, need: t?.scrollWidth, have: t?.clientWidth }
    }))
    report.push({ ename, state, w, theme, rows })
    await page.locator('.sidenav-footer').first().screenshot({ path: `${OUT}/${LABEL}-footer-${state}-${w}-${theme}-${ename}.png` })
    await ctx.close()
  }
  await b.close()
}
fs.writeFileSync(`${OUT}/${LABEL}-rows.json`, JSON.stringify(report, null, 1))
for (const r of report) if (r.theme === 'light') console.log(r.ename, r.state, r.w, JSON.stringify(r.rows))
