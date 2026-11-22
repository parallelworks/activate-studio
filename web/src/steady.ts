/** Counts and lists the Status panel and footer poll. The platform's own
 *  answers flap now and then: a running session reported as something
 *  else for one read, a model served from a session missing while that
 *  session does not answer. Shown as read, the count flickers between
 *  polls (16, 17, 16 sessions). So something shown goes only when two
 *  reads in a row leave it out; something new shows at once. */

/** The list to show: this read's items, and any the last read showed that
 *  this read alone left out. `missed` remembers, between reads, the keys
 *  left out once; the caller keeps one per list. */
export function keepMissing<T>(shown: readonly T[] | null, read: readonly T[], key: (t: T) => string, missed: Set<string>): T[] {
  const now = new Set(read.map(key))
  for (const k of [...missed]) if (now.has(k)) missed.delete(k)
  const kept: T[] = []
  for (const t of shown ?? []) {
    const k = key(t)
    if (now.has(k)) continue
    if (missed.has(k)) { missed.delete(k); continue }
    missed.add(k)
    kept.push(t)
  }
  return [...read, ...kept]
}

/** The count to show: a higher one at once, a lower one only when the read
 *  before was lower too. `low` remembers the last lower read. */
export function steadyCount(shown: number | null, read: number, low: { last: number | null }): number {
  if (shown == null || read >= shown) { low.last = null; return read }
  if (low.last != null) { low.last = null; return read }
  low.last = read
  return shown
}
