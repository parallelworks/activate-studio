import { getLibrary, listLibraries, type Library } from './libraries.js'
import { blendHits, ftsExprBare, ftsHit, ftsSql, nameHit, namesSql, searchVector, searchWordsAndNames, type SearchHit } from './gufi.js'
import { gufiVtAvailable, runAcross } from './gufiVt.js'
import { effectiveSettings } from './settings.js'
import { addOmission, failureReason, libraryGap, type Omission } from './omissions.js'

/**
 * Which libraries a request means: one, by id (the knowledge base when
 * unnamed), or "all", every library the viewer can read: the knowledge
 * base, the deployment's read-only libraries, and the viewer's own
 * libraries on their clusters. Search and the Query page take either; the
 * assistant's tools too.
 */
export const ALL = 'all'

export function scopeLibraries(param: string | null | undefined, viewerId: string | null, primaryLabel?: string): Library[] {
  return scopeWithOmitted(param, viewerId, primaryLabel).libs
}

/** The same, with the libraries "all" leaves out because they have no
 *  index to answer from (one being built, one whose build failed, one whose
 *  index is missing), each with its reason, so a request can say so. */
export function scopeWithOmitted(param: string | null | undefined, viewerId: string | null, primaryLabel?: string): { libs: Library[]; omitted: Omission[] } {
  if (String(param ?? '').trim().toLowerCase() !== ALL) return { libs: [getLibrary(param, primaryLabel, viewerId)], omitted: [] }
  const libs: Library[] = []
  const omitted: Omission[] = []
  for (const l of listLibraries(primaryLabel, viewerId)) {
    if (l.caps.index || l.primary) { libs.push(l); continue }
    addOmission(omitted, libraryGap(l, { words: false, meaning: false }) ?? { id: l.id, label: l.label, reason: 'index not found', partial: false })
  }
  return { libs, omitted }
}

export interface ScopedHit extends SearchHit { library: { id: string; label: string } }

/**
 * Search over one or several libraries. Each library runs what it
 * supports (full text and semantic where its index has them, names
 * always); one that fails adds nothing and the search goes on.
 * The hits are blended across libraries as within one, each tagged with
 * its library, so the same path in two libraries stays two hits.
 */
/** Whether a search looks in a library by meaning: one the Studio builds
 *  when its level is not off, and an index it only reads when it has vectors. */
export function meaningSearched(l: Library): boolean {
  if (l.meaning === undefined || l.meaning === null) return l.caps.vectors
  return l.meaning !== 'off' && (l.caps.vectors || l.primary)
}

/** Whether a search looks in a library's words: one whose index has its
 *  full-text tables, and the main folder unless it is indexed by name only. */
export function wordsSearched(l: Library): boolean {
  return l.caps.fullText || (l.primary && l.depth !== 'names')
}

/** How long a search across several libraries waits for each; one that has
 *  not answered by then is left out and named (opts.slow), so a slow remote
 *  index never holds up the others. STUDIO_SEARCH_DEADLINE_MS changes it. */
export const SEARCH_DEADLINE_MS = Number(process.env.STUDIO_SEARCH_DEADLINE_MS) || 20_000

export async function searchScope(libs: Library[], q: string, limit: number,
  opts: { semantic: boolean; nameLimit?: number; deadlineMs?: number; slow?: string[]; omitted?: Omission[] }): Promise<ScopedHit[]> {
  // What each library's state leaves out, said before anything runs; one
  // with nothing to answer from is not searched at all.
  const omitted = opts.omitted ?? []
  const gone = new Set<string>()
  for (const l of libs) {
    const gap = libraryGap(l, { words: true, meaning: opts.semantic })
    if (!gap) continue
    addOmission(omitted, gap)
    if (!gap.partial) gone.add(l.id)
  }
  if (gone.size) libs = libs.filter(l => !gone.has(l.id))
  const failed = (l: Library, e: unknown, partial: boolean, what = '') => {
    addOmission(omitted, { id: l.id, label: l.label, reason: `${what}${failureReason(l, e)}`, partial })
  }
  const SEP = '\u0001'
  const tag = (l: Library) => (hits: SearchHit[]) => hits.map(h => ({ ...h, path: `${l.id}${SEP}${h.path}` }))
  const nameLimit = opts.nameLimit ?? Math.min(Math.max(10, Math.floor(limit / 5)), 50)
  const ftsLibs = libs.filter(wordsSearched)
  // With Settings, General, "Search across libraries" set to gufi_vt,
  // full-text and name matches run as one gufi_vt statement each
  // (gufiVt.ts), as Query does; a library gufi_vt could not answer, and a
  // search gufi_vt cannot carry, run on their own below. The default runs
  // every library in parallel, which measured two to three times faster
  // here, since gufi_vt answers its locations one after another.
  const vt = libs.length > 1 && effectiveSettings().searchEngine === 'gufi_vt' && await gufiVtAvailable()
  const viaVt = { fts: new Set<string>(), names: new Set<string>() }
  const vtHits: { fts: SearchHit[]; names: SearchHit[] } = { fts: [], names: [] }
  if (vt) {
    const bare = ftsExprBare(q)
    const nSql = namesSql(q, nameLimit)
    const across = async (which: 'fts' | 'names', sql: string, members: Library[], cap: number, hit: (r: string[], rel: string) => SearchHit) => {
      const r = await runAcross(members.map(lib => ({ lib, plan: { stages: { E: sql }, columns: [] } })), null).catch(() => null)
      if (!r) return
      const failed = new Set(r.failed.map(f => f.library))
      const count = new Map<string, number>()
      for (const l of members) if (!failed.has(l.label)) viaVt[which].add(l.id)
      for (const { id, row } of r.rows) {
        const n = (count.get(id) ?? 0) + 1
        if (n > cap) continue
        count.set(id, n)
        vtHits[which].push({ ...hit(row, row[0]), path: `${id}${SEP}${row[0]}` })
      }
    }
    await Promise.all([
      bare && ftsLibs.length > 1 ? across('fts', ftsSql(bare, limit), ftsLibs, limit * 3, ftsHit) : Promise.resolve(),
      nSql ? across('names', nSql, libs, nameLimit, nameHit) : Promise.resolve(),
    ])
  }
  // One library alone is waited for; among several, each gets the deadline.
  const deadline = libs.length > 1 ? (opts.deadlineMs ?? SEARCH_DEADLINE_MS) : 0
  const per = await Promise.all(libs.map(async l => {
    const t = tag(l)
    // Words and names in one walk of the library's index (searchWordsAndNames).
    const ftsLimit = wordsSearched(l) && !viaVt.fts.has(l.id) ? limit : 0
    const namesLimit = !viaVt.names.has(l.id) ? nameLimit : 0
    const none = { fts: [] as SearchHit[], names: [] as SearchHit[] }
    const all = Promise.all([
      ftsLimit || namesLimit ? searchWordsAndNames(q, { ftsLimit, nameLimit: namesLimit }, l.indexRoot).then(r => ({ fts: t(r.fts), names: t(r.names) }), e => { failed(l, e, false); return none }) : Promise.resolve(none),
      opts.semantic && meaningSearched(l) ? searchVector(q, Math.min(limit, 10), l.indexRoot).then(t, e => { failed(l, e, true, 'not searched by meaning: '); return [] as SearchHit[] }) : Promise.resolve([]),
    ]).then(([wn, vec]) => ({ fts: wn.fts, names: wn.names, vec }))
    if (!deadline) return all
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<null>(resolve => { timer = setTimeout(() => resolve(null), deadline) })
    const first = await Promise.race([all, late])
    clearTimeout(timer)
    if (first) return first
    opts.slow?.push(l.label)
    addOmission(omitted, { id: l.id, label: l.label, reason: `did not answer within ${deadline >= 1000 ? `${Math.round(deadline / 1000)} s` : `${deadline} ms`}`, partial: false })
    return { fts: [] as SearchHit[], names: [] as SearchHit[], vec: [] as SearchHit[] }
  }))
  per.push({ fts: vtHits.fts, names: vtHits.names, vec: [] })
  // Across libraries, word matches go best bm25 first and name matches best
  // nameRank first, as within one; a stable sort keeps each library's order
  // among equals, and hits with no rank (gufi_vt's) after the ranked ones.
  const ranked = (hits: SearchHit[]) => libs.length > 1 ? [...hits].sort((a, b) => (a.rank ?? Infinity) - (b.rank ?? Infinity) || 0) : hits
  const blended = blendHits(ranked(per.flatMap(p => p.fts)), ranked(per.flatMap(p => p.names)), per.flatMap(p => p.vec), limit)
  const byId = new Map(libs.map(l => [l.id, l]))
  return blended.map(h => {
    const i = h.path.indexOf(SEP)
    const l = byId.get(h.path.slice(0, i))!
    return { ...h, path: h.path.slice(i + 1), library: { id: l.id, label: l.label } }
  })
}
