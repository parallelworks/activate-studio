import { api } from './api'
import { reloadAppConfig } from './config'

/**
 * After a library is added, the page's library list (the app config, which
 * the Library switcher, Search and Query read) is read again each time one
 * finishes building, and once more when none is building, wherever the
 * library was added from. Settings, Libraries does this while it is open;
 * a library added from the Library page's dialog was otherwise missing from
 * the switcher until the app restarted.
 */
const building = (state: string | undefined) => state === 'building' || state === 'updating'

let watching = false

export function reloadWhenLibrariesBuilt(opts: { everyMs?: number; forMs?: number } = {}): void {
  if (watching) return
  watching = true
  const every = opts.everyMs ?? 3000
  const until = Date.now() + (opts.forMs ?? 60 * 60 * 1000)
  let ready: Set<string> | null = null
  // The last answer of each list: one that fails this time is taken as it
  // last was, never as empty, which would read as nothing building and end
  // the watch before the library is ready.
  let lastUser: Awaited<ReturnType<typeof api.userLibraries>>['libraries'] | null = null
  let lastHere: Awaited<ReturnType<typeof api.libraries>>['libraries'] | null = null
  const tick = async () => {
    const [gotUser, gotHere] = await Promise.all([
      api.userLibraries().then(r => r.libraries ?? [], () => null),
      api.libraries().then(r => r.libraries ?? [], () => null),
    ])
    const user = (lastUser = gotUser ?? lastUser)
    const here = (lastHere = gotHere ?? lastHere)
    // Until both lists have answered once, nothing is concluded.
    if (user && here) {
      const now = new Set([
        ...(user ?? []).filter(l => l.state === 'ready').map(l => l.id),
        ...(here ?? []).filter(l => l.build && !building(l.build.state) && l.caps.index).map(l => l.id),
      ])
      if (ready && [...now].some(id => !ready!.has(id))) reloadAppConfig()
      ready = now
      const pending = (user ?? []).some(l => building(l.state)) || (here ?? []).some(l => !!l.build && building(l.build.state))
      if (!pending) { watching = false; reloadAppConfig(); return }
    }
    if (Date.now() > until) { watching = false; return }
    setTimeout(() => void tick(), every)
  }
  void tick()
}

/** For tests: forget a watch left running. */
export function resetLibraryWatchForTests(): void { watching = false }
