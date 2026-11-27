import { useEffect, useState } from 'react'

/** The phone layout's breakpoint in styles.css: one column, the navigation along the bottom edge. */
export const PHONE_QUERY = '(max-width: 760px)'

/** Wider than a phone, but below 1200px, where the status panel floats over the page instead of taking a column. */
export const MID_QUERY = '(min-width: 761px) and (max-width: 1199px)'

/** A touch screen with no mouse or trackpad (an iPad on its own), matching
 *  the touch rules in styles.css. */
export const TOUCH_QUERY = '(hover: none) and (pointer: coarse)'

/** Where the Assistant's conversations list goes. A tablet held upright has
 *  room for the navigation and the thread but not the list as well, which
 *  left the conversation about 270px wide on an iPad: on a touch screen the
 *  list moves to the drawer whenever the chat area is narrower than the
 *  package measures a list beside a thread to need ('auto'). A desktop
 *  window keeps the list beside the thread down to a phone's width. */
export function chatListPlacement(phone: boolean, touch: boolean): 'drawer' | 'auto' | 'inline' {
  return phone ? 'drawer' : touch ? 'auto' : 'inline'
}

/** Where the Code page's sessions list opens over the page: a phone's width,
 *  and a touch screen narrower than a tablet held sideways, where the list
 *  beside a session left it about 260px wide on an upright iPad. */
export const CODE_DRAWER_QUERY = `(max-width: 720px), ${TOUCH_QUERY} and (max-width: 1099px)`

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
