import { countFor, useNotices } from '../notices'

/** A session waits on its person: an approval, a question or a plan. */
export function waitsOnYou(s: { status?: string; archivedAt?: string | null; approvals?: readonly unknown[] | null }): boolean {
  if (s.archivedAt) return false
  return s.status === 'awaiting_approval' || (s.approvals?.length ?? 0) > 0
}

/**
 * How many of the person's pw code sessions wait on them, for the Code
 * entry's mark in the menu, as PW Code on the web's menu counts them: from
 * the notices, which carry the Code page's listing. null when it cannot be
 * read (no sign-in of their own, or no platform).
 */
export function useCodeWaiting(enabled: boolean): number | null {
  const notices = useNotices()
  return enabled && notices.listed ? countFor(notices, 'code') : null
}
