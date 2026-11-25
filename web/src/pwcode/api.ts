import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths } from '@parallelworks/code'

/**
 * pw code sessions over @parallelworks/code, the client PW Code on the web
 * uses. The Studio's server serves the platform's own routes for the
 * viewer's own pw code chats (server-go/internal/chat/pwcodeproxy.go) and
 * sends each on: through ACTIVATE as the viewer for a session on another
 * machine, or to the daemon the Studio reaches for one it drives directly.
 * The event feed the package opens at /ws/code/... is the Studio's
 * /api/pwcode-feed/..., where the viewer's sign-in identifies them.
 */
export const pwClient = createClient<PwCodePaths>({ baseUrl: typeof location === 'undefined' ? '' : location.origin })

// The session the Code page has open in its own pane, which the listing
// then has too, in a chat or not.
let openPane = ''
export function setOpenPane(machine: string, session: string): void { openPane = machine && session ? `${machine}/${session}` : '' }
/** Forgets it, unless another has been opened since. */
export function clearOpenPane(machine: string, session: string): void { if (openPane === `${machine}/${session}`) openPane = '' }
pwClient.use({
  onRequest({ request }) {
    const url = new URL(request.url)
    if (!openPane || request.method !== 'GET' || url.pathname !== '/api/code/sessions') return undefined
    url.searchParams.set('open', openPane)
    return new Request(url, request)
  },
})

// A command typed in the session's composer reads the session's palette
// first, and the package sends the line as a message when the palette
// comes back empty: "/plan ..." then reached the session as text, which it
// refuses. A palette that could not be read fails the command instead, with
// the Studio's reason (the session not taken up yet, say), and the line
// stays in the composer to send again.
pwClient.use({
  async onResponse({ request, response }) {
    if (response.ok || request.method !== 'GET' || !/\/sessions\/[^/]+\/palette$/.test(new URL(request.url).pathname)) return undefined
    const p = await response.clone().json().catch(() => null) as { detail?: string; error?: string } | null
    throw new Error(p?.detail ?? p?.error ?? `The session's commands could not be read (${response.status}). Try again in a moment.`)
  },
})

/** The Studio's address for a feed the package names by its platform path. */
export function feedURL(path: string, origin = location.origin): string {
  return origin.replace(/^http/, 'ws') + '/api/pwcode-feed' + path.replace(/^\/ws\/code/, '')
}

// As PW Code on the web reads a session: the transcript's end first, and
// its images by ref, each loaded as it is drawn (the Studio passes the
// session's images on, pictures only).
export const pwCode = createPwCodeApi(pwClient, {
  openSocket: path => new WebSocket(feedURL(path)),
  hidden: () => document.hidden,
  previewsByRef: true,
  transcriptEndFirst: true,
})

/** Moves the chat to the session its session was cleared into (/clear, here
 *  or wherever the session is directed from), and has the chat read its
 *  link again. */
export async function followCleared(conversationId: string, session: string): Promise<void> {
  const r = await fetch(`/api/chat/pwcode/${encodeURIComponent(conversationId)}/follow`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session }),
  })
  if (!r.ok) {
    const p = await r.json().catch(() => null) as { detail?: string; error?: string } | null
    throw new Error(p?.detail ?? p?.error ?? `The chat could not follow the cleared session (${r.status}).`)
  }
  window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId, session } }))
}

/** The machine id a chat's session is shown under: its platform machine
 *  for a relayed session, else the Studio's own (directMachine). */
export function sessionMachine(link: { relay?: boolean; machine?: string }): string {
  return link.relay && link.machine ? link.machine : 'studio'
}
