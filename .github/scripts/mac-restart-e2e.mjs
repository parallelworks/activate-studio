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

function searchCounts(r) {
  const by = {}
  for (const h of r.hits ?? []) { const id = h.library?.id ?? String(h.library ?? 'kb'); by[id] = (by[id] ?? 0) + 1 }
  return { lib: by[lib] ?? 0, by, slow: r.slow ?? [], error: r.error ?? null }
}
function queryCounts(r) {
  const by = {}
  for (const id of r.rowLibraries ?? []) by[id] = (by[id] ?? 0) + 1
  return { lib: by[lib] ?? 0, rows: (r.rows ?? []).length, by, failed: r.failed ?? [], engine: r.engine ?? null, error: r.error ?? r.message ?? null }
}

const counts = { stage, at: new Date().toISOString() }
counts.searchWord = searchCounts(await get(`/api/search?q=${WORD}&library=all&limit=1000`))
counts.searchName = searchCounts(await get(`/api/search?q=${NAME}&library=all&limit=100`))
for (const [k, q] of Object.entries(QUERIES)) counts[`query_${k}`] = queryCounts(await post('/api/query', q))
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
try {
  await page.goto(`${BASE}/#view=search`)
  await sleep(2500)
  for (const [name, q] of [['search-word', WORD], ['search-name', NAME]]) {
    const box = page.locator('.view:not(.hidden) input').first()
    await box.fill(q)
    await box.press('Enter')
    await sleep(4000)
    await shot(name)
  }
  await page.goto(`${BASE}/#view=query`)
  await sleep(2500)
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
