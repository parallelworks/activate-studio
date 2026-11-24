import { useEffect, useState } from 'react'

/** The phone layout's breakpoint in styles.css: one column, the navigation along the bottom edge. */
export const PHONE_QUERY = '(max-width: 760px)'

/** Wider than a phone, but below 1200px, where the status panel floats over the page instead of taking a column. */
export const MID_QUERY = '(min-width: 761px) and (max-width: 1199px)'

/** Whether the window matches a media query, following it as the window changes size. */
export function useMediaQuery(query: string): boolean {
  const [on, setOn] = useState(() => typeof matchMedia === 'function' && matchMedia(query).matches)
  useEffect(() => {
    if (typeof matchMedia !== 'function') return
    const mq = matchMedia(query)
    const change = () => setOn(mq.matches)
    change()
    mq.addEventListener?.('change', change)
    return () => mq.removeEventListener?.('change', change)
  }, [query])
  return on
}

/** Whether the window is at the phone layout's width, following it as the window changes size. */
export function usePhoneLayout(): boolean {
  return useMediaQuery(PHONE_QUERY)
}
