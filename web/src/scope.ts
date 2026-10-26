import { useEffect, useState } from 'react'
import { currentLibrary, setLibrary, type PublicLibrary } from './api'

/**
 * Which libraries Search and Query look in: all of them, or the ones
 * ticked. Each page keeps its own choice in this browser (localStorage), so
 * Search over two libraries and Query over one stay as they were left. A
 * page with no choice of its own covers every library, as a new tab does.
 * The choice is only ever a subset of the libraries that exist: ids no
 * longer there are dropped, and ticking every one is "all".
 */
export type Scope = 'all' | string[]
export type ScopePage = 'search' | 'query'

const ALL = 'all'
const key = (page: ScopePage) => `ade-scope-${page}`
const listeners = new Map<ScopePage, Set<(s: Scope) => void>>()
const held = new Map<ScopePage, Scope>()

function stored(page: ScopePage): Scope | null {
  try {
    const raw = localStorage.getItem(key(page))
    if (!raw) return null
    const v = JSON.parse(raw) as unknown
    if (v === ALL) return ALL
    if (Array.isArray(v) && v.every(x => typeof x === 'string') && v.length) return v as string[]
  } catch { /* storage unavailable or not ours */ }
  return null
}

/** The scope as it can be, given the libraries there are. */
export function settleScope(s: Scope, libraries: Pick<PublicLibrary, 'id' | 'primary'>[]): Scope {
  if (libraries.length <= 1) return libraries.length ? [libraries[0].id] : ALL
  if (s === ALL) return ALL
  const ids = s.filter(id => libraries.some(l => l.id === id))
  if (!ids.length) return ALL
  return ids.length === libraries.length ? ALL : ids
}

/** The page's scope, before the libraries are known. */
export function readScope(page: ScopePage): Scope {
  return held.get(page) ?? stored(page) ?? ALL
}

export function saveScope(page: ScopePage, s: Scope): void {
  held.set(page, s)
  try { localStorage.setItem(key(page), JSON.stringify(s)) } catch { /* kept for this page load only */ }
  for (const fn of listeners.get(page) ?? []) fn(s)
}

/** The page's scope as chosen, kept current as it changes anywhere. */
export function useScopeValue(page: ScopePage): Scope {
  const [s, set] = useState<Scope>(() => readScope(page))
  useEffect(() => {
    const fns = listeners.get(page) ?? new Set()
    fns.add(set)
    listeners.set(page, fns)
    return () => { fns.delete(set) }
  }, [page])
  return s
}

/** The page's scope, settled against the libraries, and a setter. */
export function useScope(page: ScopePage, libraries: Pick<PublicLibrary, 'id' | 'primary'>[]): [Scope, (s: Scope) => void] {
  const s = useScopeValue(page)
  return [settleScope(s, libraries), (next: Scope) => saveScope(page, settleScope(next, libraries))]
}

/** The scope as requests send it: "all", one id, or ids with commas. */
export function scopeParam(s: Scope): string {
  return s === ALL ? ALL : s.join(',')
}

/** The scope in a request body: nothing for the knowledge base alone (the
 *  server's default, and all there is with one library), "all", one id, or
 *  the ids as a list. `count` is how many libraries there are, if known. */
export function scopeBody(s: Scope, count?: number): { library?: string | string[] } {
  if (s === ALL) return count !== undefined && count <= 1 ? {} : { library: ALL }
  if (s.length === 1) return s[0] === 'kb' ? {} : { library: s[0] }
  return { library: s }
}

/** Whether the scope covers more than one library, so hits and rows say
 *  which library they are in. */
export function scopeIsSeveral(s: Scope, libraries: unknown[]): boolean {
  return s === ALL ? libraries.length > 1 : s.length > 1
}

/** The libraries in a scope. */
export function librariesIn<T extends { id: string }>(libraries: T[], s: Scope): T[] {
  return s === ALL ? libraries : libraries.filter(l => s.includes(l.id))
}

/** Opens a match in its own library: in the combined tree when the Library
 *  page shows all of them, else by switching the Library page to it. */
export function openInLibrary(onOpen: (path: string) => void, libraryId: string | undefined, path: string): void {
  const cur = currentLibrary()
  if (cur === ALL) { onOpen(libraryId && libraryId !== ALL ? `${libraryId}/${path}` : path); return }
  if (libraryId && libraryId !== ALL && libraryId !== cur) setLibrary(libraryId)
  onOpen(path)
}

/** For tests: forget every page's choice. */
export function resetScopesForTests(): void {
  held.clear()
  for (const page of ['search', 'query'] as const) {
    try { localStorage.removeItem(key(page)) } catch { /* none kept */ }
  }
}
