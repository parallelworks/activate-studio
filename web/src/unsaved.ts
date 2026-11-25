/**
 * Text being edited in the Library and not saved yet. What would put it
 * away (another file, the list, another library, a rename or delete of the
 * file) goes through leaveUnsaved, which asks first. Another page of the
 * Studio does not: the Library stays as it was behind it.
 */
interface Held {
  path: string
  /** Asks what to do with it, and runs `then` once it is saved or dropped. */
  ask: (then: () => void) => void
}

let held: Held | null = null

export function holdUnsaved(h: Held): () => void {
  held = h
  return () => { if (held === h) held = null }
}

/** Runs `then` now when nothing unsaved is in the way, else asks. With
 *  `paths`, only what one of them is, or holds, is in the way. */
export function leaveUnsaved(then: () => void, paths?: string[]): void {
  const h = held
  if (!h || (paths && !paths.some(p => p === h.path || h.path.startsWith(`${p}/`)))) { then(); return }
  h.ask(then)
}

/** The file whose edit is not saved, or null. */
export function unsavedPath(): string | null { return held?.path ?? null }
