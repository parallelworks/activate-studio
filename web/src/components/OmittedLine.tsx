import { Fragment } from 'react'

/** A library a search or a query left out, or searched only in part, and
 *  why (server/src/omissions.ts). */
export interface Omission {
  id: string
  label: string
  reason: string
  /** Searched in part (by name, say, but not its words). */
  partial: boolean
}

/** The key Settings, Libraries reads to bring a library's row into view. */
export const FOCUS_LIBRARY_KEY = 'studio.focusLibrary'

/** The event a mounted Settings, Libraries listens for. */
export const FOCUS_LIBRARY_EVENT = 'studio:focus-library'

/** Opens Settings, Libraries at a library's row: one that mounts reads the
 *  key, one already mounted (Settings opened before) hears the event. */
export function openLibraryRow(id: string): void {
  try { sessionStorage.setItem(FOCUS_LIBRARY_KEY, id) } catch { /* the row is not brought into view */ }
  location.hash = '#view=settings:libraries'
  window.dispatchEvent(new CustomEvent(FOCUS_LIBRARY_EVENT, { detail: id }))
}

/**
 * One line under the results naming every library a search or a query
 * left out, or searched only in part, with the reason; each name opens the
 * library's row in Settings, Libraries. Nothing when nothing was left out.
 */
export function OmittedLine({ omitted, what = 'search' }: { omitted?: Omission[] | null; what?: 'search' | 'query' }) {
  if (!omitted?.length) return null
  const out = omitted.filter(o => !o.partial)
  const part = omitted.filter(o => o.partial)
  const list = (os: Omission[]) => os.map((o, i) => (
    <Fragment key={o.id}>
      {i > 0 && '; '}
      {/* A button: the app takes clicks on #view= links before a handler here would run. */}
      <button type="button" className="omitted-lib" title="Open in Settings, Libraries" onClick={() => openLibraryRow(o.id)}>{o.label}</button> ({o.reason})
    </Fragment>
  ))
  return (
    <p className="omitted-line" role="status">
      {out.length > 0 && <span>Left out: {list(out)}.</span>}
      {out.length > 0 && part.length > 0 && ' '}
      {part.length > 0 && <span>{what === 'query' ? 'Answered in part' : 'Searched in part'}: {list(part)}.</span>}
    </p>
  )
}
