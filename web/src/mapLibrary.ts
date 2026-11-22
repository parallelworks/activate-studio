import { useEffect, useRef, useState } from 'react'
import type { PublicLibrary } from './api'

/**
 * Which library each map on the Stats page shows: the corpus topology and
 * the semantic map keep their own choice in this browser (localStorage),
 * apart from Search and Query's. A choice of a library that is gone falls
 * back to the map's default.
 */
export type MapPage = 'topology' | 'semantic'

const key = (page: MapPage) => `ade-map-library-${page}`

export function storedMapLibrary(page: MapPage): string | null {
  try {
    const v = localStorage.getItem(key(page))
    return v && /^[A-Za-z0-9_:.-]{1,200}$/.test(v) ? v : null
  } catch { return null }
}

export function saveMapLibrary(page: MapPage, id: string): void {
  try { localStorage.setItem(key(page), id) } catch { /* kept for this page only */ }
}

/** The library a map shows: the one chosen here before if it still exists,
 *  else `fallback`. Setting it keeps the choice. `follow` is the library
 *  the Stats page is showing: a map opened on, or switched to, another
 *  library than the main one (or all) shows that library too, so the page
 *  never pairs one library's figures with another's map. */
export function useMapLibrary(page: MapPage, libraries: Pick<PublicLibrary, 'id'>[], fallback: string | null, follow?: string | null): [string | null, (id: string) => void] {
  const [chosen, setChosen] = useState<string | null>(() => (follow && follow !== 'all' && follow !== 'kb' ? follow : storedMapLibrary(page)))
  const followed = useRef(follow)
  useEffect(() => {
    if (follow === followed.current) return
    followed.current = follow
    if (follow && follow !== 'all') setChosen(follow)
  }, [follow])
  const valid = chosen && libraries.some(l => l.id === chosen) ? chosen : null
  return [valid ?? fallback, (id: string) => { saveMapLibrary(page, id); setChosen(id) }]
}

/** For tests: forget both maps' choices. */
export function resetMapLibrariesForTests(): void {
  for (const page of ['topology', 'semantic'] as const) {
    try { localStorage.removeItem(key(page)) } catch { /* none kept */ }
  }
}
