import { useEffect, useState } from 'react'

/** A session waits on its person: an approval, a question or a plan. */
export function waitsOnYou(s: { status?: string; archivedAt?: string | null; approvals?: readonly unknown[] | null }): boolean {
  if (s.archivedAt) return false
  return s.status === 'awaiting_approval' || (s.approvals?.length ?? 0) > 0
}

/**
 * How many of the person's pw code sessions wait on them, for the Code
 * entry's mark in the menu, as PW Code on the web's menu counts them. Read
 * from the Code page's listing every 30 seconds while the tab shows; null
 * when it cannot be read (no sign-in of their own, or no platform).
 */
export function useCodeWaiting(enabled: boolean): number | null {
  const [count, setCount] = useState<number | null>(null)
  useEffect(() => {
    if (!enabled) { setCount(null); return }
    let gone = false
    const load = async () => {
      if (document.visibilityState !== 'visible') return
      try {
        const res = await fetch('/api/code-page/sessions')
        if (!res.ok) { if (!gone) setCount(null); return }
        const d = await res.json() as { sessions?: { status?: string; archivedAt?: string | null; approvals?: unknown[] | null }[] }
        if (!gone) setCount((d.sessions ?? []).filter(waitsOnYou).length)
      } catch { if (!gone) setCount(null) }
    }
    void load()
    const t = setInterval(() => void load(), 30_000)
    return () => { gone = true; clearInterval(t) }
  }, [enabled])
  return count
}
