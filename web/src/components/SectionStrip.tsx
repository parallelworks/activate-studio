import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FocusEvent, type ReactNode, type RefObject } from 'react'

/** Room left at each edge for the arrow, so a section brought into view is not under it. */
const ARROW = 44

/**
 * The section rail of Settings and the user guide. Beside the page it is a
 * column; at the phone layout's width (styles.css) it is a strip along the
 * top that scrolls sideways with its scrollbar hidden. The strip shows an
 * arrow over each edge it has more sections past, which moves it along, and
 * a section reached with Tab is brought into view clear of the arrows.
 * Without them a strip wider than the window ended mid-word with nothing to
 * say it went on.
 */
export function SectionStrip({ navRef, children }: { navRef?: RefObject<HTMLElement | null>; children: ReactNode }) {
  const own = useRef<HTMLElement>(null)
  const ref = navRef ?? own
  const [more, setMore] = useState({ before: false, after: false })

  const measure = useCallback(() => {
    const nav = ref.current
    if (!nav) return
    const sideways = nav.scrollWidth > nav.clientWidth + 1
    const before = sideways && nav.scrollLeft > 1
    const after = sideways && nav.scrollLeft + nav.clientWidth < nav.scrollWidth - 1
    setMore(m => (m.before === before && m.after === after ? m : { before, after }))
  }, [ref])

  // After every render (the sections listed arrive with the account and the
  // settings, and a group opens in place) and whenever the strip's width changes.
  useLayoutEffect(measure)
  useEffect(() => {
    const nav = ref.current
    if (!nav || typeof ResizeObserver !== 'function') return
    const watch = new ResizeObserver(measure)
    watch.observe(nav)
    return () => watch.disconnect()
  }, [measure, ref])

  const move = (direction: -1 | 1) => {
    const nav = ref.current
    if (!nav) return
    nav.scrollLeft += direction * Math.max(nav.clientWidth - 2 * ARROW, ARROW)
    measure()
  }

  // Only the strip moves, never the page around it.
  const reveal = (event: FocusEvent<HTMLElement>) => {
    const nav = ref.current
    if (!nav || nav.scrollWidth <= nav.clientWidth + 1) return
    const a = (event.target as HTMLElement).getBoundingClientRect(), n = nav.getBoundingClientRect()
    if (a.left < n.left + ARROW) nav.scrollLeft -= n.left + ARROW - a.left
    else if (a.right > n.right - ARROW) nav.scrollLeft += a.right - (n.right - ARROW)
  }

  return (
    <div className="section-strip">
      <nav className="help-nav" ref={ref as RefObject<HTMLElement>} onScroll={measure} onFocus={reveal}>{children}</nav>
      {more.before && (
        <button type="button" className="strip-more strip-more-before" tabIndex={-1} aria-label="Show earlier sections" onClick={() => move(-1)}>
          <span className="strip-more-dot"><svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="m10 4-4 4 4 4"/></svg></span>
        </button>
      )}
      {more.after && (
        <button type="button" className="strip-more strip-more-after" tabIndex={-1} aria-label="Show more sections" onClick={() => move(1)}>
          <span className="strip-more-dot"><svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="m6 4 4 4-4 4"/></svg></span>
        </button>
      )}
    </div>
  )
}
