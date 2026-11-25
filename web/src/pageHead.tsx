import { useSyncExternalStore, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

/** The end of the page header (App.tsx), where the page in front puts its
 *  own controls. */
let host: HTMLElement | null = null
const listeners = new Set<() => void>()
const setHost = (el: HTMLElement | null) => {
  host = el
  for (const l of [...listeners]) l()
}
const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => { listeners.delete(l) }
}

export function PageHeadSlot() {
  return <div className="page-head-actions" ref={setHost} />
}

/** Controls for the page header; where there is none, they show in place. */
export function PageHeadActions({ children }: { children: ReactNode }) {
  const at = useSyncExternalStore(subscribe, () => host, () => null)
  return at ? createPortal(children, at) : <div className="page-head-actions">{children}</div>
}
