// One stage of the restart test (mac-restart-e2e.sh): counts what a built
// library answers, through the routes the Search and Query pages use, then
// screenshots those pages at 1300 px with All libraries in scope. The
// counts come first, so nothing here asks for GET /api/libraries (which
// probes every library) before them; neither page asks for it either.
//
// Usage: node mac-restart-e2e.mjs STAGE OUT_DIR LIBRARY_ID [counts-only]
// Prints one JSON line with the counts, and appends it to OUT_DIR/counts.jsonl.
import { chromium } from 'playwright'
import fs from 'node:fs'
import path from 'node:path'

const [stage, out, lib, only] = process.argv.slice(2)
const BASE = 'http://127.0.0.1:4180'
const WORD = 'heliotrope'       // only OCR reads it, in the pictures and scanned PDFs
const NAME = 'ledger-0042'      // one file's name
const NAMES = 'ledger-'         // 600 files' names
const SIZE = '7777'             // 300 files of exactly this many bytes
fs.mkdirSync(path.join(out, 'shots'), { recursive: true })

const post = (p, body) => fetch(BASE + p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then(r => r.json())
const get = p => fetch(BASE + p).then(r => r.json())
const builder = filters => ({ builder: { source: 'files', filters, match: 'all', groupBy: 'none', sort: 'name', dir: 'asc', limit: 2000, subtree: '' }, library: 'all' })
const QUERIES = {
  name: builder([{ field: 'name', op: 'contains', value: NAMES }]),
  size: builder([{ field: 'size', op: 'eq', value: SIZE }]),
  words: builder([{ field: 'text', op: 'match', value: WORD }]),
}

// A library is in a response's results, or named in its omitted list (with
// a reason, and partial when it was searched in part).
const omittedOf = r => (r.omitted ?? []).map(o => ({ id: o.id, reason: o.reason, partial: !!o.partial }))
function searchCounts(r, ms) {
  const by = {}
  for (const h of r.hits ?? []) { const id = h.library?.id ?? String(h.library ?? 'kb'); by[id] = (by[id] ?? 0) + 1 }
  return { lib: by[lib] ?? 0, by, omitted: omittedOf(r), slow: r.slow ?? [], error: r.error ?? null, ms }
}
function queryCounts(r, ms) {
  const by = {}
  for (const id of r.rowLibraries ?? []) by[id] = (by[id] ?? 0) + 1
  return { lib: by[lib] ?? 0, rows: (r.rows ?? []).length, by, omitted: omittedOf(r), failed: r.failed ?? [], engine: r.engine ?? null, error: r.error ?? r.message ?? null, ms }
}
const timed = async fn => { const t = Date.now(); const r = await fn(); return [r, Date.now() - t] }

const counts = { stage, at: new Date().toISOString() }
counts.searchWord = searchCounts(...await timed(() => get(`/api/search?q=${WORD}&library=all&limit=1000`)))
counts.searchName = searchCounts(...await timed(() => get(`/api/search?q=${NAME}&library=all&limit=100`)))
for (const [k, q] of Object.entries(QUERIES)) counts[`query_${k}`] = queryCounts(...await timed(() => post('/api/query', q)))
// Each of these finds files in the library, so it is in the results or in
// omitted; silent lists the responses where it is in neither.
counts.silent = Object.entries(counts).filter(([, v]) => v && typeof v === 'object' && 'lib' in v && v.lib === 0 && !v.omitted.some(o => o.id === lib)).map(([k]) => k)
const line = JSON.stringify(counts)
console.log(line)
fs.appendFileSync(path.join(out, 'counts.jsonl'), line + '\n')

// The same, as the pages show them (not while a stop is waiting on it).
if (only === 'counts-only') process.exit(0)
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1300, height: 860 } })
await page.addInitScript(() => { try { sessionStorage.setItem('ade-library', 'all') } catch { /* none */ } })
const shot = async name => page.screenshot({ path: path.join(out, 'shots', `${stage}-${name}.png`) }).catch(() => {})
const sleep = ms => new Promise(r => setTimeout(r, ms))
// Setup has not been run in this test: its dialog is closed as a person does.
const closeSetup = async () => {
  const later = page.getByRole('button', { name: 'Finish later', exact: true })
  if (await later.count()) { await later.first().click(); await sleep(1000) }
}
try {
  await page.goto(`${BASE}/#view=search`)
  await sleep(2500)
  await closeSetup()
  for (const [name, q] of [['search-word', WORD], ['search-name', NAME]]) {
    const box = page.locator('.view:not(.hidden) input[type="search"]').first()
    await box.fill(q, { timeout: 15_000 }).catch(async e => { await shot(`${name}-not-editable`); throw e })
    await box.press('Enter')
    await sleep(4000)
    await shot(name)
  }
  await page.goto(`${BASE}/#view=query`)
  await sleep(2500)
  await closeSetup()
  for (const [name, q] of Object.entries(QUERIES)) {
    await page.evaluate(detail => window.dispatchEvent(new CustomEvent('ade-run-query', { detail })), { builder: q.builder })
    await sleep(4000)
    await shot(`query-${name}`)
  }
} catch (e) {
  console.error(`screenshots: ${e.message}`)
} finally {
  await browser.close()
}
