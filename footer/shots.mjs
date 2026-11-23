// The sidebar's status footer: each state at the narrowest and widest
// sidebar, light and dark, in Chromium and WebKit.
//   node footer/shots.mjs <base> <outdir> <label>
// States: whatsnew (this version is newer than the one last opened here),
// update (a newer version is available: the #709 tint), plain (neither).
import { chromium, webkit } from 'playwright'
import fs from 'node:fs'
const [BASE, OUT, LABEL] = process.argv.slice(2)
fs.mkdirSync(OUT, { recursive: true })
const status = available => ({
  mode: 'app', current: { version: 'v1.203.22', builtAt: '2026-11-22T18:00:00Z' }, policy: { check: true, install: 'manual', channel: 'stable' },
  latest: available ? { version: 'v1.203.23', publishedAt: '2026-11-23T00:00:00Z', notes: '', url: null } : null, releases: [],
  available, canInstall: true, reason: null, command: null, commandNote: null, package: null, state: 'idle', progress: null, error: null,
  checkedAt: '2026-11-23T02:00:00Z', offline: false, previous: null, reopen: false, versions: [], moveToStable: null, stateFormat: 1, snapshots: [],
})
for (const [ename, engine] of [['chromium', chromium], ['webkit', webkit]]) {
  const b = await engine.launch()
  for (const state of ['whatsnew', 'update', 'plain']) for (const w of [200, 400]) for (const theme of ['light', 'dark']) {
    const ctx = await b.newContext({ viewport: { width: 1280, height: 800 }, serviceWorkers: 'block', colorScheme: theme })
    const page = await ctx.newPage()
    await page.route('**/api/updates', r => r.request().method() === 'GET' ? r.fulfill({ json: status(state === 'update') }) : r.fulfill({ json: status(state === 'update') }))
    await page.route('**/api/updates/check', r => r.fulfill({ json: status(state === 'update') }))
    // Two libraries, All libraries open, and a count as large as the report's.
    await page.route('**/api/config', async r => {
      const c = await (await r.fetch()).json()
      const main = c.libraries?.[0]
      return r.fulfill({ json: { ...c, libraries: main ? [main, { ...main, id: 'papers', label: 'Papers', primary: false }] : c.libraries } })
    })
    await page.route(/\/api\/stats(\?|$)/, r => r.fulfill({ json: { files: 22332, dirs: 1830, totalBytes: 41e9, ms: 12 } }))
    await page.addInitScript(([t, s]) => {
      try { localStorage.setItem('theme', t); sessionStorage.setItem('ade-library', 'all'); localStorage.setItem('ade-library-count', '2'); localStorage.setItem('ade-seen-version', s === 'whatsnew' ? 'v1.203.21' : 'v1.203.22') } catch { /* none */ }
    }, [theme, state])
    await page.goto(`${BASE}/#view=chat`, { waitUntil: 'networkidle' })
    await page.addStyleTag({ content: `:root { --nav-w: ${w}px !important; }` })
    await page.waitForTimeout(1500)
    const f = page.locator('.sidenav-footer').first()
    await f.screenshot({ path: `${OUT}/${LABEL}-footer-${state}-${w}-${theme}-${ename}.png` })
    await ctx.close()
  }
  await b.close()
}
console.log('done', LABEL)
