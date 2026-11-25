import { mutate } from 'swr'
import { clearChatRecords, setRecordViewer } from './chatRecord'
import { forgetEngines } from './chatEngine'
import { forgetCommands } from './pwcode/chatCommands'
import { forgetStarts } from './pwcode/newChatStart'
import { VIEWER_CACHES_CLEARED } from './viewerEvents'
import { forgetModelSource } from './pwcode/session/usePlatformModels'

/**
 * What the chat page keeps so that going back to a chat is quick is the
 * person's own: their chats' records (chatRecord.ts), and the pw code
 * sessions, transcripts and listings @parallelworks/code reads into SWR's
 * cache as them. All of it goes when someone else is the viewer, and when
 * the person's ACTIVATE sign-in or key changes (signed out, or another
 * account), since pw code is read with that credential. A Studio sign-out
 * leaves the page, which takes everything with it.
 */
let viewer: string | null = null

/** Said on the window when the caches are cleared, for what the page keeps
 *  drawn (the hidden pw code chats). */
export { VIEWER_CACHES_CLEARED }

/** Says who the viewer is: someone other than before clears what was kept. */
export function setCacheViewer(username: string): void {
  if (viewer !== null && viewer !== username) clearViewerCaches()
  viewer = username
  setRecordViewer(username)
}

/** Forgets everything kept for the viewer. */
export function clearViewerCaches(): void {
  clearChatRecords()
  forgetEngines()
  forgetStarts()
  forgetCommands()
  forgetModelSource()
  // SWR's own cache, where the pw code session reads live. Cleared without
  // reading anything again: what is shown next is read as the new viewer.
  void mutate(() => true, undefined, { revalidate: false })
  // Told after the call, which can come while the page is being drawn.
  if (typeof window !== 'undefined') queueMicrotask(() => window.dispatchEvent(new Event(VIEWER_CACHES_CLEARED)))
}

/** The channel the Studio's tabs tell each other on that an ACTIVATE
 *  sign-in or key changed, so a chat open in another tab reads again;
 *  null where there is none. */
let channel: BroadcastChannel | null | undefined
export function accessChannel(): BroadcastChannel | null {
  if (channel === undefined) {
    try { channel = typeof BroadcastChannel === 'function' ? new BroadcastChannel('ade-model-access') : null } catch { channel = null }
  }
  return channel
}

if (typeof window !== 'undefined') {
  // Settings, Accounts says so when an ACTIVATE sign-in or key changes,
  // in this tab or, over the channel, in another.
  window.addEventListener('ade:model-access-changed', clearViewerCaches)
  accessChannel()?.addEventListener('message', () => window.dispatchEvent(new CustomEvent('ade:model-access-changed')))
}
