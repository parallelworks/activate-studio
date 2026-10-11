import { getLibrary, listLibraries, type Library } from './libraries.js'
import { blendHits, searchFts, searchNames, searchVector, type SearchHit } from './gufi.js'

/**
 * Which libraries a request means: one, by id (the knowledge base when
 * unnamed), or "all", every library the viewer can read: the knowledge
 * base, the deployment's read-only libraries, and the viewer's own
 * libraries on their clusters. Search and the Query page take either; the
 * assistant's tools too.
 */
export const ALL = 'all'

export function scopeLibraries(param: string | null | undefined, viewerId: string | null, primaryLabel?: string): Library[] {
  if (String(param ?? '').trim().toLowerCase() === ALL) return listLibraries(primaryLabel, viewerId).filter(l => l.caps.index || l.primary)
  return [getLibrary(param, primaryLabel, viewerId)]
}

export interface ScopedHit extends SearchHit { library: { id: string; label: string } }

/**
 * Search over one or several libraries. Each library runs what it
 * supports (full text and semantic where its index has them, names
 * always); one that fails adds nothing and the search goes on.
 * The hits are blended across libraries as within one, each tagged with
 * its library, so the same path in two libraries stays two hits.
 */
export async function searchScope(libs: Library[], q: string, limit: number, opts: { semantic: boolean; nameLimit?: number }): Promise<ScopedHit[]> {
  const SEP = '\u0001'
  const tag = (l: Library) => (hits: SearchHit[]) => hits.map(h => ({ ...h, path: `${l.id}${SEP}${h.path}` }))
  const per = await Promise.all(libs.map(async l => {
    const t = tag(l)
    const [fts, names, vec] = await Promise.all([
      l.caps.fullText || l.primary ? searchFts(q, limit, l.indexRoot).then(t, () => []) : Promise.resolve([]),
      searchNames(q, opts.nameLimit ?? Math.min(Math.max(10, Math.floor(limit / 5)), 50), l.indexRoot).then(t, () => []),
      opts.semantic && (l.caps.vectors || l.primary) ? searchVector(q, Math.min(limit, 10), l.indexRoot).then(t, () => []) : Promise.resolve([]),
    ])
    return { fts, names, vec }
  }))
  const blended = blendHits(per.flatMap(p => p.fts), per.flatMap(p => p.names), per.flatMap(p => p.vec), limit)
  const byId = new Map(libs.map(l => [l.id, l]))
  return blended.map(h => {
    const i = h.path.indexOf(SEP)
    const l = byId.get(h.path.slice(0, i))!
    return { ...h, path: h.path.slice(i + 1), library: { id: l.id, label: l.label } }
  })
}
