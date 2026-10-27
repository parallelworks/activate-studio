import { getLibrary, lacks, listLibraries, type Library } from './libraries.js'
import { EXTERNAL_DB_PATTERN, blendHits, ftsExprBare, ftsHit, ftsSql, fuseRows, nameHit, namesSql, queryVectors, searchVector, searchWordsAndNames, walkSearch, type HitMode, type RefineSpec, type SearchHit, type WalkLimits, type WalkRow } from './gufi.js'
import { meaningPhrase, namePathMatches } from './refine.js'
import { gufiVtAvailable, runAcross } from './gufiVt.js'
import { effectiveSettings } from './settings.js'
import { addOmission, failureReason, libraryGap, type Omission } from './omissions.js'
import { REMOTE_PREFIX, remoteHostKey } from './userLibraries.js'

/**
 * Which libraries a request means: one, by id (the knowledge base when
 * unnamed), several, by ids (a list, or one string with commas), or "all",
 * every library the viewer can read: the knowledge base, the deployment's
 * read-only libraries, and the viewer's own libraries on their clusters.
 * Search and the Query page take any of these; the assistant's tools too.
 */
export const ALL = 'all'

/** A scope as a request sends it: null for none (the knowledge base),
 *  'all', or the ids named, each once, in order. */
export type ScopeParam = string | string[] | null | undefined
export function scopeIds(param: ScopeParam): 'all' | string[] | null {
  const parts = (Array.isArray(param) ? param : [param ?? ''])
    .flatMap(p => String(p ?? '').split(','))
    .map(s => s.trim())
    .filter(Boolean)
  if (!parts.length) return null
  if (parts.some(p => p.toLowerCase() === ALL)) return ALL
  return [...new Set(parts)]
}

/** Whether a scope means more than one library. */
export function severalLibraries(param: ScopeParam): boolean {
  const ids = scopeIds(param)
  return ids === ALL || (Array.isArray(ids) && ids.length > 1)
}

export function scopeLibraries(param: ScopeParam, viewerId: string | null, primaryLabel?: string): Library[] {
  return scopeWithOmitted(param, viewerId, primaryLabel).libs
}

/** The same, with the libraries left out because they have no index to
 *  answer from (one being built, one whose build failed, one whose index is
 *  missing), each with its reason, so a request can say so. One library
 *  named alone is asked anyway and answers for itself; among several, or
 *  all, such a library is named instead. An id that names no library is a
 *  404, as it is alone. */
export function scopeWithOmitted(param: ScopeParam, viewerId: string | null, primaryLabel?: string): { libs: Library[]; omitted: Omission[] } {
  const ids = scopeIds(param)
  if (ids === null) return { libs: [getLibrary(undefined, primaryLabel, viewerId)], omitted: [] }
  if (ids !== ALL && ids.length === 1) return { libs: [getLibrary(ids[0], primaryLabel, viewerId)], omitted: [] }
  const named = ids === ALL ? listLibraries(primaryLabel, viewerId) : ids.map(id => getLibrary(id, primaryLabel, viewerId))
  const libs: Library[] = []
  const omitted: Omission[] = []
  for (const l of named) {
    if (l.caps.index || l.primary) { libs.push(l); continue }
    addOmission(omitted, libraryGap(l, { words: false, meaning: false }) ?? { id: l.id, label: l.label, reason: 'index not found', partial: false })
  }
  return { libs, omitted }
}

export interface ScopedHit extends SearchHit { library: { id: string; label: string } }

/** Whether a search looks in a library by meaning: one the Studio builds
 *  when its level is not off, and an index it only reads, unless it is
 *  known to have no vectors (lacks): a probe that has not counted the whole
 *  tree yet is not taken for an answer, so a library never loses its
 *  meaning results to a sample that missed its vectors. */
export function meaningSearched(l: Library): boolean {
  if (l.meaning === 'off') return false
  if (l.primary && l.meaning) return true
  return !lacks(l, 'vectors')
}

/** Whether a search looks in a library's words: the main folder unless it
 *  is indexed by name only, a library the Studio builds past Names, and an
 *  index it only reads, each unless known to have no words (lacks). */
export function wordsSearched(l: Library): boolean {
  if (l.caps.fullText) return true
  if (l.primary) return l.depth !== 'names'
  if (l.depth === 'names') return l.caps.fullText
  return !lacks(l, 'fullText')
}

/** How long a search across several libraries waits for each; one that has
 *  not answered by then is left out and named (opts.slow), so a slow remote
 *  index never holds up the others. STUDIO_SEARCH_DEADLINE_MS changes it. */
export const SEARCH_DEADLINE_MS = Number(process.env.STUDIO_SEARCH_DEADLINE_MS) || 20_000

/** Whether libraries on this machine share one walk. Several index paths in
 *  one gufi_query measured no faster here than a gufi_query each running at
 *  once, and a walk each keeps a library's own deadline, so they do not;
 *  libraries on one remote host do, which saves a round trip each.
 *  STUDIO_STACK_LOCAL=1 stacks them too. */
export const STACK_LOCAL = process.env.STUDIO_STACK_LOCAL === '1'

type ScopeOpts = { semantic: boolean; nameLimit?: number; deadlineMs?: number; slow?: string[]; omitted?: Omission[]; refine?: RefineSpec | null }

/**
 * Search over one or several libraries, each in one walk of its index for
 * every kind of match it supports: names and folder paths always, words
 * where its index has text, meaning where it has vectors (walkSearch). The
 * libraries of one remote host share one walk and one round trip. The kinds
 * are fused across libraries with reciprocal rank fusion (fuseRows), each
 * hit tagged with its library, so the same path in two libraries stays two
 * hits. A library that fails, or does not answer in time, is named in
 * opts.omitted and the search goes on. With Settings, General, "Search
 * across libraries" set to gufi_vt, the earlier form runs (searchScopeVt).
 *
 * A refined search (opts.refine) runs in the same walks, always: its name
 * field gives the name and folder matches, its text the words, and the
 * text without its syntax the meaning. Each kind keeps its own limit and
 * the answer holds them all, so many name matches never crowd out the
 * words or meaning: `limit` is the words' limit there.
 */
export async function searchScope(libs: Library[], q: string, limit: number, opts: ScopeOpts): Promise<ScopedHit[]> {
  const omitted = opts.omitted ?? []
  const refine = opts.refine ?? null
  libs = withoutGaps(libs, opts, omitted)
  if (!refine && libs.length > 1 && effectiveSettings().searchEngine === 'gufi_vt' && await gufiVtAvailable()) return searchScopeVt(libs, q, limit, opts, omitted)
  const SEP = '\u0001'
  const nameLimit = opts.nameLimit ?? Math.min(Math.max(10, Math.floor(limit / 5)), 50)
  const vecLimit = Math.min(limit, 10)
  const caps: Partial<Record<HitMode, number>> = { name: nameLimit, path: nameLimit, fts: limit * 3, vector: vecLimit }
  // A refined search lists every kind to its own limit, words to `limit`.
  if (refine) caps.fts = limit
  const total = refine ? nameLimit * 2 + limit + vecLimit : limit
  // With match 'all', meaning a walk could not carry is kept to the files
  // the name field matches here, as the walk keeps its own.
  const keep = (rel: string) => !refine || refine.match !== 'all' || !refine.text || !refine.name || namePathMatches(refine.name, rel)
  const phrase = refine ? meaningPhrase(refine.text) : q
  const failed = (l: Library, e: unknown, partial: boolean, what = '') => {
    addOmission(omitted, { id: l.id, label: l.label, reason: `${what}${failureReason(l, e)}`, partial })
  }
  const wantVec = (l: Library) => opts.semantic && meaningSearched(l)
  const external = EXTERNAL_DB_PATTERN || null
  // The query is embedded for any search by meaning: external databases can
  // carry vectors where the Studio's own tables have none.
  const vectors = opts.semantic && phrase && (libs.some(wantVec) || external) ? await queryVectors(phrase) : []
  const limitsFor = (words: boolean, vec: boolean): WalkLimits => ({
    names: nameLimit, paths: nameLimit,
    words: limit * 3, wordsPerDir: limit,
    vectors: vectors.length ? vecLimit : 0, vectorsPerDir: 3,
    studioText: words, studioVectors: vec, external,
  })
  // Libraries that share a walk: the same machine, and the same kinds, so
  // one query fits them all. A remote host's are stacked; this machine's
  // only with STACK_LOCAL.
  const groups = new Map<string, Library[]>()
  // The host each remote library reaches, as ssh resolves it, so two that
  // name one account differently share a walk.
  const hostKeys = new Map(await Promise.all(libs.filter(l => l.indexRoot.startsWith(REMOTE_PREFIX))
    .map(async l => [l.id, await remoteHostKey(l.indexRoot).catch(() => null)] as const)))
  for (const l of libs) {
    const words = wordsSearched(l), vec = wantVec(l) && vectors.length > 0
    const remote = l.indexRoot.startsWith(REMOTE_PREFIX)
    const host: string | null = remote ? hostKeys.get(l.id) ?? null : 'local'
    const shared = remote ? host !== null : STACK_LOCAL
    const key = shared ? `${host}\0${words}\0${vec}` : `one\0${l.id}`
    groups.set(key, [...(groups.get(key) ?? []), l])
  }
  // One library alone is waited for; among several, each walk gets the deadline.
  const deadline = libs.length > 1 ? (opts.deadlineMs ?? SEARCH_DEADLINE_MS) : 0
  const late = (ls: Library[]) => {
    for (const l of ls) {
      opts.slow?.push(l.label)
      addOmission(omitted, { id: l.id, label: l.label, reason: `did not answer within ${deadline >= 1000 ? `${Math.round(deadline / 1000)} s` : `${deadline} ms`}`, partial: false })
    }
  }
  const results = await Promise.all([...groups.values()].map(async members => {
    const words = wordsSearched(members[0]), vec = wantVec(members[0]) && vectors.length > 0
    const byRoot = new Map<string, Library[]>()
    for (const l of members) byRoot.set(l.indexRoot, [...(byRoot.get(l.indexRoot) ?? []), l])
    const work = walkSearch(q, limitsFor(words, vec), [...byRoot.keys()], vec ? vectors : [], refine)
      .then(rows => rows.flatMap(r => (byRoot.get(r.root) ?? []).map(l => ({ ...r, path: `${l.id}${SEP}${r.path}` }))))
      .catch(e => { for (const l of members) failed(l, e, false); return [] as WalkRow[] })
    // Meaning for a library whose walk could not carry it: one embedded
    // with another model (an index from before the ONNX model), or one
    // read here without that model.
    const extra = work.then(async rows => {
      const out: WalkRow[] = []
      for (const l of members) {
        if (!wantVec(l) || l.indexRoot.startsWith(REMOTE_PREFIX)) continue
        if (vec && rows.some(r => r.kind === 'vector' && r.path.startsWith(`${l.id}${SEP}`))) continue
        const hits = await searchVector(phrase, vecLimit, l.indexRoot).catch(e => { failed(l, e, true, 'not searched by meaning: '); return [] as SearchHit[] })
        for (const h of hits.filter(h => keep(h.path))) out.push({ kind: 'vector', r: h.score, path: `${l.id}${SEP}${h.path}`, name: h.name, size: h.size, mtime: h.mtime, snippet: h.snippet, root: l.indexRoot })
      }
      return [...rows, ...out]
    })
    if (!deadline) return extra
    let timer: ReturnType<typeof setTimeout> | undefined
    const timeout = new Promise<null>(resolve => { timer = setTimeout(() => resolve(null), deadline) })
    const first = await Promise.race([extra, timeout])
    clearTimeout(timer)
    if (first) return first
    late(members)
    return [] as WalkRow[]
  }))
  const rows = results.flat()
  // A library whose words or meaning came from external databases was
  // searched there after all: the note that said it could not be goes.
  const found = (kind: HitMode, id: string) => rows.some(r => r.kind === kind && r.path.startsWith(`${id}${SEP}`))
  for (let i = omitted.length - 1; i >= 0; i--) {
    const o = omitted[i]
    if (!o.partial) continue
    const left = o.reason.split('; ').filter(r => !((r.startsWith('words not searched') && found('fts', o.id)) || (r.startsWith('not searched by meaning') && found('vector', o.id))))
    if (!left.length) omitted.splice(i, 1)
    else omitted[i] = { ...o, reason: left.join('; ') }
  }
  const byId = new Map(libs.map(l => [l.id, l]))
  return fuseRows(rows, caps, total).map(h => {
    const i = h.path.indexOf(SEP)
    const l = byId.get(h.path.slice(0, i))!
    return { ...h, path: h.path.slice(i + 1), library: { id: l.id, label: l.label } }
  })
}

/** What each library's state leaves out, said before anything runs; one
 *  with nothing to answer from is not searched at all. */
function withoutGaps(libs: Library[], opts: ScopeOpts, omitted: Omission[]): Library[] {
  const gone = new Set<string>()
  for (const l of libs) {
    // A refined search with nothing in its text field reads no words.
    const gap = libraryGap(l, { words: !opts.refine || !!opts.refine.text, meaning: opts.semantic })
    if (!gap) continue
    addOmission(omitted, gap)
    if (!gap.partial) gone.add(l.id)
  }
  return gone.size ? libs.filter(l => !gone.has(l.id)) : libs
}

/** The search as it ran before the one walk, for the gufi_vt setting: full
 *  text and names across libraries as one gufi_vt statement each, meaning
 *  per library, blended by blendHits. */
async function searchScopeVt(libs: Library[], q: string, limit: number, opts: ScopeOpts, omitted: Omission[]): Promise<ScopedHit[]> {
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
