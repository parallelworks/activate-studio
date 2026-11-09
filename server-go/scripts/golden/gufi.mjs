// Golden values for internal/gufi: the Node server's search parsing, the
// one-pass walk's stages, the passage and fusion helpers, and querySql's
// plans and commands, for each case in internal/gufi/testdata/cases.json.
// Usage: node gufi.mjs <cases.json> <repo root>  (prints JSON)
import fs from 'node:fs'
import path from 'node:path'

const [, , casesFileArg, repoArg] = process.argv
const cases = JSON.parse(fs.readFileSync(path.resolve(casesFileArg), 'utf8'))
const repo = path.resolve(repoArg)
for (const k of Object.keys(process.env)) if (k.startsWith('AUTH_') || k.startsWith('OIDC_') || k.startsWith('STUDIO_') || k.startsWith('GUFI_')) delete process.env[k]
// Fixed paths, none of which need exist, so the commands do not depend
// on where this runs; the index is INDEX_BASE/gufi/kb.
process.env.KB_ROOT = '/golden/kb'
process.env.INDEX_BASE = '/golden/ix'
process.env.GUFI_BIN = '/golden/gufi/bin'
Date.now = () => cases.now * 1000

const dist = f => path.join(repo, 'server/dist', f)
const g = await import(dist('gufi.js'))
const { withoutProvenance } = await import(dist('textCleanup.js'))
const qy = await import(dist('query.js'))

const attempt = fn => { try { return { value: fn() } } catch (e) { return { error: e.message, status: e.status ?? null } } }
const out = {
  queries: cases.queries.map(q => {
    const parsed = g.parseSearchQuery(q)
    return {
      q, parsed, fts: g.ftsExprFor(parsed), bare: g.ftsExprBare(q), semantic: g.wantsSemantic(q),
      names: g.namesSql(q, 20), rank: g.nameRank(parsed), where: g.nameWhere(parsed),
    }
  }),
  nameFilters: cases.nameFilters.map(f => ({ f, groups: g.parseNameFilter(f) })),
  stages: cases.stages.map(c => g.onePassStages(c.q, c.lim, c.roots, c.vectors ?? [], c.refine ?? null)),
  passages: cases.passages.map(p => ({ snippet: g.passageSnippet(p), clean: withoutProvenance(p) })),
  fuse: cases.fuse.map(c => g.fuseRows(c.rows, c.caps, c.limit)),
  sizes: cases.sizes.map(s => attempt(() => qy.parseSize(s))),
  whens: cases.whens.map(s => attempt(() => qy.parseWhen(s, cases.now))),
  plans: [],
}
for (const body of cases.plans) {
  try { out.plans.push({ value: await qy.querySql(body, null) }) } catch (e) { out.plans.push({ error: e.message, status: e.status ?? null }) }
}
console.log(JSON.stringify(out, null, 1))
