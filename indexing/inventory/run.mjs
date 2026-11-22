// Settings, Indexing check: for each scenario, the Libraries tab's inventory
// (every line of text, every control with its name and state, every menu's
// items, every hover title) and screenshots.
//   node ixset/run.mjs <base> <outdir> <label> [shots]
// "shots" also takes 1280x800, 1920x1080 and a narrow width, light and dark,
// in Chromium and WebKit.
import { chromium, webkit } from 'playwright'
import fs from 'node:fs'
import { SCENARIOS } from './fixtures.mjs'
import os from 'node:os'
const HOME = os.homedir()
const RUN = `${HOME}/studio-dev/ui-check/run/ixset`

const [BASE, OUT, LABEL, SHOTS] = process.argv.slice(2)
const TAB = process.env.IX_TAB ?? 'libraries'
const ONLY = process.env.IX_ONLY?.split(',')
fs.mkdirSync(OUT, { recursive: true })

async function prepare(page, name, theme) {
  const s = SCENARIOS[name]()
  await page.route('**/api/**', async route => {
    const url = new URL(route.request().url())
    const method = route.request().method()
    if (method === 'GET' && s[url.pathname]) return route.fulfill({ json: s[url.pathname] })
    if (method === 'GET' && url.pathname === '/api/config') {
      const r = await route.fetch(); const c = await r.json()
      return route.fulfill({ json: { ...c, theme, signIn: s.signIn, libraries: s.configLibraries, kbLabel: s.configLibraries[0]?.label ?? c.kbLabel } })
    }
    if (method === 'GET' && url.pathname === '/api/libraries/research/bucket/text') return route.fulfill({ json: {} })
    // Nothing is changed: every write answers as if done.
    if (method !== 'GET') return route.fulfill({ json: {} })
    // Anything else from the test Studio, with its run folder named as a Mac's would be.
    if (method === 'GET') {
      const r = await route.fetch()
      const type = r.headers()['content-type'] ?? ''
      if (!type.includes('json')) return route.fulfill({ response: r })
      let body = (await r.text()).replaceAll(RUN, '/Users/user/Library/Application Support/ACTIVATE Studio').replaceAll(HOME, '/Users/user')
      if (url.pathname === '/api/index/settings') {
        const j = JSON.parse(body); j.single = !s.signIn; j.location.canChange = !s.signIn; body = JSON.stringify(j)
      }
      return route.fulfill({ response: r, body })
    }
    return route.continue()
  })
  await page.emulateMedia({ colorScheme: theme })
  await page.addInitScript(t => { try { localStorage.setItem('theme', t) } catch { /* none */ } }, theme)
}

async function open(page) {
  await page.goto(`${BASE}/#view=settings:${TAB}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
}

async function inventory(page) {
  const panel = page.locator('[role="tabpanel"]:visible').first()
  const text = (await panel.innerText()).split('\n').map(x => x.trim()).filter(Boolean)
  const controls = await panel.evaluate(root => [...root.querySelectorAll('button, a[href], [role="combobox"], input, [role="switch"], [role="slider"], select, textarea')]
    .filter(e => e.offsetParent !== null || e.getClientRects().length)
    .map(e => ({ role: e.getAttribute('role') ?? e.tagName.toLowerCase(), name: (e.getAttribute('aria-label') || e.innerText || e.getAttribute('title') || '').trim().replace(/\s+/g, ' '),
      disabled: e.matches(':disabled') || e.getAttribute('aria-disabled') === 'true', value: e.value ?? undefined })))
  const titles = await panel.evaluate(root => [...root.querySelectorAll('[title]')].map(e => e.getAttribute('title')))
  // Every menu's items, opened one at a time.
  const menus = []
  const triggers = panel.locator('button[aria-haspopup="menu"]')
  for (let i = 0; i < await triggers.count(); i++) {
    const t = triggers.nth(i)
    if (await t.isDisabled()) { menus.push({ trigger: await t.getAttribute('aria-label'), items: 'disabled' }); continue }
    await t.click(); await page.waitForTimeout(250)
    const items = await page.locator('[role="menuitem"]:visible').evaluateAll(es => es.map(e => `${e.innerText.trim()}${e.getAttribute('aria-disabled') === 'true' || e.hasAttribute('data-disabled') ? ' (disabled)' : ''}`))
    menus.push({ trigger: await t.getAttribute('aria-label'), items })
    await page.keyboard.press('Escape'); await page.waitForTimeout(150)
  }
  return { text, controls, titles, menus }
}

const engines = SHOTS ? [['chromium', chromium], ['webkit', webkit]] : [['chromium', chromium]]
for (const [ename, engine] of engines) {
  const b = await engine.launch()
  for (const name of Object.keys(SCENARIOS)) {
    if (ONLY && !ONLY.includes(name)) continue
    if (ename === 'chromium') {
      const ctx = await b.newContext({ viewport: { width: 1440, height: 900 }, serviceWorkers: 'block' })
      const page = await ctx.newPage()
      await prepare(page, name, 'light'); await open(page)
      fs.writeFileSync(`${OUT}/${LABEL}-${TAB}-${name}.json`, JSON.stringify(await inventory(page), null, 1))
      await ctx.close()
    }
    if (!SHOTS) continue
    const sizes = name === 'pair' || name === 'single' ? [[1280, 800], [1920, 1080], [720, 900]] : [[1280, 800]]
    for (const [w, h] of sizes) {
      for (const theme of ['light', 'dark']) {
        const ctx = await b.newContext({ viewport: { width: w, height: h }, serviceWorkers: 'block' })
        const page = await ctx.newPage()
        await prepare(page, name, theme); await open(page)
        const file = `${OUT}/${LABEL}-${TAB}-${name}-${w}-${theme}-${ename}.png`
        await page.screenshot({ path: file, fullPage: name !== 'pair' })
        await ctx.close()
      }
    }
  }
  await b.close()
}
console.log('done', LABEL)
