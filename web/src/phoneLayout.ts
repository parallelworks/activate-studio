import { useEffect, useState } from 'react'

/** The phone layout's breakpoint in styles.css: one column, the navigation along the bottom edge. */
export const PHONE_QUERY = '(max-width: 760px)'

/** Whether the window is at the phone layout's width, following it as the window changes size. */
export function usePhoneLayout(): boolean {
  const [phone, setPhone] = useState(() => typeof matchMedia === 'function' && matchMedia(PHONE_QUERY).matches)
  useEffect(() => {
    if (typeof matchMedia !== 'function') return
    const mq = matchMedia(PHONE_QUERY)
    const on = () => setPhone(mq.matches)
    on()
    mq.addEventListener?.('change', on)
    return () => mq.removeEventListener?.('change', on)
  }, [])
  return phone
}
