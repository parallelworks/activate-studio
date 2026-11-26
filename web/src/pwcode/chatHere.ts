// New pw code chat here, from a folder or a file of the Library: a new chat
// on pw code whose session works in that folder, on the machine that holds
// it. The Studio says which, or why not (server-go chat, pwcodeplace.go),
// and looks again as the first message starts the session.
import { toast } from '@parallelworks/ui'
import { type MentionLibrary, mentionToken } from '../components/MentionPalette'
import { buildOpenHash } from '../nav'
import { VIEWER_CACHES_CLEARED } from '../viewerCache'
import { clearModelPick } from './newChatPick'
import { type NewChatStart, setStart, STUDIO_MACHINE } from './newChatStart'
import { setNewChatEngine } from '../chatEngine'

export const CHAT_HERE = 'New pw code chat here'

/** GET /api/chat/pwcode/start/library: where the chat's session works. */
export interface ChatPlace {
  machine: string
  machineName: string
  folder: string
  library: string
  /** The folder's path in the library, and the file's that was asked for. */
  path: string
  file: string
  claudeCodeAllowed?: boolean
  claudeInstalled?: boolean
}
export type PlaceAnswer = { place: ChatPlace; why?: undefined } | { place?: undefined; why: string }

export interface LibraryItem { library: string; path: string }

// An answer is shown a little while, so a menu opened again has it at
// once, and asked for again once it is a few seconds old, so pw code that
// stopped answering (or started) shows while the menu is open; a reason is
// shown for less, since the person may have dealt with it.
const KEPT_MS = 60_000
const WHY_KEPT_MS = 10_000
const FRESH_MS = 5_000
const WAIT_MS = 400
const answers = new Map<string, { at: number; answer: PlaceAnswer }>()
const reading = new Map<string, Promise<PlaceAnswer>>()
let epoch = 0

const keyOf = (i: LibraryItem) => `${i.library}\n${i.path}`

/** Forgets what was said: the viewer or their ACTIVATE sign-in changed. */
export function forgetPlaces(): void { epoch++; answers.clear(); reading.clear() }
if (typeof window !== 'undefined') window.addEventListener(VIEWER_CACHES_CLEARED, forgetPlaces)

/** The answer the page has for an item, if it is still good. */
export function knownPlace(item: LibraryItem): PlaceAnswer | undefined {
  const had = answers.get(keyOf(item))
  return had && Date.now() - had.at < (had.answer.place ? KEPT_MS : WHY_KEPT_MS) ? had.answer : undefined
}

/** Whether the answer the page has for an item is a few seconds old or
 *  more, so the Studio is asked again. */
export function stalePlace(item: LibraryItem): boolean {
  const had = answers.get(keyOf(item))
  return !had || Date.now() - had.at >= FRESH_MS
}

/** Asks the Studio where a chat at an item would work, or why it cannot:
 *  the answer the page has while it is fresh. */
export function askPlace(item: LibraryItem): Promise<PlaceAnswer> {
  const known = knownPlace(item)
  if (known && !stalePlace(item)) return Promise.resolve(known)
  const key = keyOf(item), at = epoch
  const going = reading.get(key)
  if (going) return going
  const read = fetch(`/api/chat/pwcode/start/library?${new URLSearchParams(item as unknown as Record<string, string>)}`, { cache: 'no-store' })
    .then(async (res): Promise<PlaceAnswer> => {
      const b = await res.json().catch(() => null) as (ChatPlace & { detail?: string; error?: string }) | null
      if (res.ok && b?.machine && b.folder) return { place: b }
      return { why: b?.detail || b?.error || 'The Studio could not say where this chat would work. Try again in a moment.' }
    })
    .catch((): PlaceAnswer => ({ why: 'The Studio could not be reached. Try again in a moment.' }))
    .then(answer => {
      if (at === epoch) answers.set(key, { at: Date.now(), answer })
      return answer
    })
    .finally(() => { if (reading.get(key) === read) reading.delete(key) })
  reading.set(key, read)
  return read
}

/** A file as a message names it, a link to it in the library. */
export function fileReference(path: string, library: string, libraries: readonly MentionLibrary[]): string {
  if (!/[\]\n]/.test(path)) return mentionToken(path, library, libraries)
  const hash = buildOpenHash({ kind: 'file', target: path, lib: library }).replace(/[()]/g, c => (c === '(' ? '%28' : '%29'))
  return `[${(path.split('/').pop() ?? path).replace(/[[\]]/g, '')}](${hash})`
}

/** Where the new chat starts, as its composer keeps it. */
export function startAt(place: ChatPlace): NewChatStart {
  return {
    ...(place.machine === STUDIO_MACHINE ? {} : { machine: { id: place.machine, claudeCodeAllowed: place.claudeCodeAllowed, claudeInstalled: place.claudeInstalled } }),
    folder: place.folder,
    library: { id: place.library, path: place.file || place.path, machineName: place.machineName },
  }
}

/** The chat page opens a new chat on this; the text goes into its box. */
export const CHAT_HERE_EVENT = 'ade-pwcode-chat-here'

/** Opens the chat where the Studio says it works, or says why it cannot. */
export async function chatHere(item: LibraryItem, libraries: readonly MentionLibrary[]): Promise<void> {
  // A system that answers late: what the click does is said meanwhile.
  let said = ''
  const wait = setTimeout(() => { said = toast.loading('Looking at the folder on its machine…') }, WAIT_MS)
  const answer = await askPlace(item)
  clearTimeout(wait)
  if (said) toast.dismiss(said)
  if (!answer.place) { toast.error(answer.why); return }
  const { place } = answer
  // The models offered are the machine's own.
  clearModelPick(null)
  setStart(null, startAt(place))
  setNewChatEngine('pwcode')
  window.dispatchEvent(new CustomEvent(CHAT_HERE_EVENT, { detail: { text: place.file ? fileReference(place.file, place.library, libraries) : '' } }))
  location.hash = '#view=chat'
}
