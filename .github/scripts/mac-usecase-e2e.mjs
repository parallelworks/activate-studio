// Drives a new user's first session through the Studio's web pages, as the
// macOS app's window shows them, against the installed app's server on
// 127.0.0.1:4180: the setup wizard on a fresh data folder (the main folder
// chosen by path, its index built at the default depth), a second library
// connected read only over SSH (an existing GUFI index on this machine,
// reached as on another one), a search that has to find a word in a .docx,
// a word only OCR can read in a picture, and a file name in the SSH index,
// a Query across both libraries, and Settings, Updates. Each step is
// screenshotted at 1300 px wide and written to summary.txt with its outcome;
// a failed step is recorded and the run goes on, and the exit status says
// whether any failed.
//
// Usage: node mac-usecase-e2e.mjs OUT_DIR MAIN_FOLDER SSH_HOST SSH_INDEX SSH_SOURCE GUFI_BIN
import { chromium } from 'playwright'
import fs from 'node:fs'
import path from 'node:path'

const [out, mainFolder, sshHost, sshIndex, sshSource, gufiBin] = process.argv.slice(2)
const BASE = 'http://127.0.0.1:4180'
const WORDS = { docx: 'converge', ocr: 'heliotrope', ssh: 'orbitnotes' }
fs.mkdirSync(path.join(out, 'shots'), { recursive: true })
const summary = path.join(out, 'summary.txt')
const t0 = Date.now()
let n = 0
let failed = 0
const log = msg => { const line = `${String(Math.round((Date.now() - t0) / 1000)).padStart(4)}s  ${msg}`; console.log(line); fs.appendFileSync(summary, line + '\n') }
const sleep = ms => new Promise(r => setTimeout(r, ms))
const api = async (p, init) => { const r = await fetch(BASE + p, init); return r.json() }

const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1300, height: 860 } })
page.on('pageerror', e => log(`page error: ${e.message}`))

async function shot(name) {
  n++
  const f = `${String(n).padStart(2, '0')}-${name}.png`
  await page.screenshot({ path: path.join(out, 'shots', f), fullPage: false }).catch(() => {})
  log(`screenshot ${f}`)
}
async function step(name, fn) {
  log(`== ${name}`)
  try {
    await fn()
    log(`ok: ${name}`)
  } catch (e) {
    failed++
    log(`FAILED: ${name}: ${String(e?.message ?? e).split('\n')[0]}`)
    await shot(`failed-${name.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}`)
  }
}
async function until(fn, ms, what) {
  const end = Date.now() + ms
  let last
  while (Date.now() < end) { last = await fn().catch(e => { last = e; return null }); if (last) return last; await sleep(2000) }
  throw new Error(`timed out waiting for ${what}`)
}
const button = name => page.getByRole('button', { name, exact: true }).first()
const indexIdle = () => api('/api/index/status').then(s => (!s.running ? s : null))

await step('the Studio opens on the setup wizard', async () => {
  await page.goto(BASE + '/')
  await page.getByRole('dialog').waitFor({ timeout: 30_000 })
  await sleep(1500)
  await shot('setup-welcome')
})

await step('setup: on to the folder step', async () => {
  // Welcome, the Studio's name, appearance, AI features, sidebar, then the folder.
  for (let i = 0; i < 8; i++) {
    if (await page.getByLabel('Folder', { exact: true }).count()) break
    await button('Next').click()
    await sleep(800)
    if (i === 3) await shot('setup-ai-step')
  }
  await page.getByLabel('Folder', { exact: true }).waitFor({ timeout: 10_000 })
  await shot('setup-folder-step')
})

await step('setup: choose the main folder by path, and build its index at the default depth', async () => {
  await page.getByLabel('Folder', { exact: true }).fill(mainFolder)
  await button('Open').click()
  await sleep(1500)
  await shot('setup-folder-opened')
  await button('Use this folder').click()
  // The Studio restarts on the new folder, then offers the build.
  await until(async () => (await button('Build the index').count()) ? true : null, 90_000, 'the Build the index button after the restart')
  const st = await api('/api/index/settings').catch(() => ({}))
  log(`main folder depth before the build: ${st.depth ?? JSON.stringify(st).slice(0, 120)}`)
  await shot('setup-folder-chosen')
  await button('Build the index').first().click()
  await sleep(3000)
  await shot('setup-index-building')
  const s = await until(indexIdle, 600_000, 'the main folder index to finish')
  log(`index finished: ${JSON.stringify(s.last ?? s.lastBuild ?? {}).slice(0, 300)}`)
  await sleep(1500)
  await shot('setup-index-built')
})

await step('setup: connect an existing GUFI index read only over SSH', async () => {
  await button('Next').click()
  await sleep(1000)
  await page.getByRole('button', { name: /^Other kinds/ }).first().click()
  await sleep(600)
  await page.getByRole('radio', { name: 'Over SSH' }).or(page.getByRole('tab', { name: 'Over SSH' })).or(page.getByText('Over SSH', { exact: true })).first().click()
  await sleep(600)
  await page.getByLabel(/^Host/).first().fill(sshHost)
  await page.getByLabel(/^Index directory/).first().fill(sshIndex)
  await page.getByLabel(/^Source directory/).first().fill(sshSource)
  await page.getByLabel(/^GUFI bin directory/).first().fill(gufiBin)
  await page.getByLabel(/^Label/).first().fill('Remote over SSH')
  await shot('setup-ssh-form')
  await button('Test connection').click()
  await until(async () => (await button('Add library').isEnabled()) ? true : null, 120_000, 'a passing connection test')
  await shot('setup-ssh-tested')
  await button('Add library').click()
  await until(async () => (await page.getByText('Added:').count()) ? true : null, 60_000, 'the library to be added')
  await shot('setup-ssh-added')
})

await step('setup: finish', async () => {
  for (let i = 0; i < 4; i++) {
    if (await button('Finish').count()) break
    await button('Next').click()
    await sleep(800)
  }
  await shot('setup-done-step')
  await button('Finish').click()
  await page.getByRole('dialog').waitFor({ state: 'detached', timeout: 30_000 }).catch(() => {})
  await sleep(2000)
  await shot('after-setup')
  const libs = await api('/api/libraries')
  log(`libraries: ${(libs.libraries ?? []).map(l => `${l.id} (${l.label}, ${l.kind ?? l.transport ?? ''}${l.readOnly || l.readonly ? ', read only' : ''})`).join('; ')}`)
})

async function search(word, expect, what) {
  await page.goto(`${BASE}/#view=search`)
  await sleep(1500)
  const box = page.getByRole('searchbox').or(page.locator('input[type="search"]')).or(page.locator('.view:not(.hidden) input').first()).first()
  await box.fill(word)
  await box.press('Enter')
  await until(async () => (await page.getByText(expect, { exact: false }).count()) ? true : null, 60_000, `${expect} in the results`)
  await sleep(1000)
  await shot(`search-${what}`)
  const r = await api(`/api/search?q=${encodeURIComponent(word)}&library=all`)
  log(`api search ${word}: ${(r.hits ?? []).slice(0, 5).map(h => `${h.library ?? 'kb'}:${h.path ?? h.name} (${h.mode ?? ''})`).join(', ')}${r.slow?.length ? ` slow: ${r.slow}` : ''}`)
}

await step(`search: a word in the .docx (${WORDS.docx})`, () => search(WORDS.docx, 'handbook.docx', 'docx'))
await step(`search: a word only OCR reads in the picture (${WORDS.ocr})`, () => search(WORDS.ocr, 'ocr-sample.png', 'ocr'))
await step(`search: a file name in the SSH index (${WORDS.ssh})`, () => search(WORDS.ssh, WORDS.ssh, 'ssh-name'))

await step('query: across both libraries (gufi_vt)', async () => {
  await page.goto(`${BASE}/#view=query`)
  await sleep(1500)
  const sw = page.locator('.scope-bar').getByRole('combobox')
  if (await sw.count()) {
    await sw.click()
    await page.getByRole('option', { name: 'All libraries' }).click()
    await sleep(800)
  } else {
    log('no library switch on the Query page')
  }
  await shot('query-page')
  await button('Run query').click()
  await until(async () => (await page.locator('table tbody tr').count()) ? true : null, 120_000, 'query rows')
  await sleep(1000)
  await shot('query-results')
  const rows = await page.locator('table tbody tr').count()
  const body = await page.locator('.view:not(.hidden)').innerText()
  log(`query rows shown: ${rows}; the remote library's file is ${body.includes(WORDS.ssh) ? '' : 'not '}among them`)
  const r = await api('/api/query', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ builder: { source: 'files', filters: [], groupBy: '', sort: 'name', dir: 'asc', limit: 200, subtree: '' }, library: 'all' }) })
  const libsSeen = [...new Set((r.rows ?? []).map(x => x.library ?? x._library ?? '?'))]
  log(`api query across all: ${(r.rows ?? []).length} rows from ${libsSeen.join(', ')}${r.failed?.length ? `; failed: ${JSON.stringify(r.failed)}` : ''}${r.error ? `; error: ${r.error}` : ''}`)
  if (!body.includes(WORDS.ssh)) throw new Error('the Query across all libraries did not show the SSH library\'s file')
})

await step('settings: updates', async () => {
  await page.goto(`${BASE}/#view=settings:updates`)
  await sleep(2500)
  await shot('settings-updates')
  const u = await api('/api/updates')
  log(`updates: mode ${u.mode}, running ${u.current?.version}, latest ${u.latest?.version ?? 'none'}, available ${u.available}, canInstall ${u.canInstall}${u.reason ? `, reason ${u.reason}` : ''}`)
})

await step('settings: libraries', async () => {
  await page.goto(`${BASE}/#view=settings:libraries`)
  await sleep(2500)
  await shot('settings-libraries')
})

await browser.close()
log(failed ? `${failed} step(s) failed` : 'PASS')
process.exit(failed ? 1 : 0)
