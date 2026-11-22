import { api, currentLibrary } from './api'

/**
 * A change made in the Library: the views that read the library are told
 * at once (ade-kb-changed). A person's own library (My library, or one they
 * built on a system of theirs) indexes the change about 20 seconds later,
 * on its system, and nothing else says when that is done. So its index is
 * watched (its builtAt in /api/user-libraries) for a while after each
 * change, and the views are told again each time it is rebuilt: the Details
 * card, Stats, the status panel's counts and the footer then show the
 * change rather than the index from before it.
 */
const EVERY_MS = 5000
const FOR_MS = 120_000

interface Watch { lib: string; built: string | null | undefined; until: number; timer: number }
let watch: Watch | null = null
let every = EVERY_MS

/** Tests poll faster. */
export function setCatchUpPollingForTests(ms: number): void { every = ms }
/** Tests start with nothing watched. */
export function resetCatchUpForTests(): void { if (watch) window.clearTimeout(watch.timer); watch = null; every = EVERY_MS }

const changed = () => window.dispatchEvent(new Event('ade-kb-changed'))

/** A person's own library: its id is the user library's. */
const ownIndexed = (lib: string) => lib.startsWith('u-')

export function libraryWritten(lib: string = currentLibrary()): void {
  changed()
  if (!ownIndexed(lib)) return
  if (watch && watch.lib === lib) { watch.until = Date.now() + FOR_MS; return }
  if (watch) window.clearTimeout(watch.timer)
  const w: Watch = { lib, built: undefined, until: Date.now() + FOR_MS, timer: 0 }
  watch = w
  const tick = async () => {
    if (watch !== w) return
    const now = await api.userLibraries().then(r => r.libraries?.find(l => l.id === lib), () => undefined)
    if (watch !== w) return
    if (now) {
      const built = now.builtAt ?? null
      const busy = now.state === 'building' || now.state === 'updating'
      if (w.built === undefined) w.built = built
      else if (!busy && built !== w.built) { w.built = built; changed() }
    }
    if (Date.now() > w.until) { watch = null; return }
    w.timer = window.setTimeout(() => void tick(), every)
  }
  void tick()
}
