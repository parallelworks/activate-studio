// The Node server's answers on a built test index, for internal/gufi's
// fixture test: walkSearch and fuseRows for each walk case, queryScope's
// columns and rows for each plan case. The caller sets INDEX_BASE (the
// index is INDEX_BASE/gufi/kb), KB_ROOT and GUFI_BIN.
// Usage: node gufi-walk.mjs <walk-cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import path from 'node:path'

const [, , casesFileArg, repoArg] = process.argv
const cases = JSON.parse(fs.readFileSync(path.resolve(casesFileArg), 'utf8'))
const repo = path.resolve(repoArg)
const dist = f => path.join(repo, 'server/dist', f)
const g = await import(dist('gufi.js'))
const qy = await import(dist('query.js'))
const root = path.join(process.env.INDEX_BASE, 'gufi', 'kb')

const out = { walks: [], plans: [] }
for (const c of cases.walks) {
  const rows = await g.walkSearch(c.q, c.lim, [root], [], c.refine ?? null)
  out.walks.push(g.fuseRows(rows, c.caps, c.limit))
}
for (const body of cases.plans) {
  try {
    const r = await qy.queryScope(body, null)
    out.plans.push({ columns: r.columns, rows: r.rows, truncated: r.truncated })
  } catch (e) { out.plans.push({ error: e.message, status: e.status ?? null }) }
}
console.log(JSON.stringify(out, null, 1))
