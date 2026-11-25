import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths } from '@parallelworks/code'
import { liveURL } from '../live'

/**
 * The Code page's client: @parallelworks/code, as PW Code on the web uses
 * it, calling the platform's routes at the Studio's /api/code-page
 * (server-go/internal/codesessions/page.go), which sends each on as the
 * person and only what the page uses. A pw code chat's session view keeps
 * its own client (pwcode/api.ts), which reaches only the person's chats.
 */

/** Where the Studio serves a platform path the page calls. */
export function pagePath(path: string): string {
  if (path.startsWith('/api/code/')) return '/api/code-page/' + path.slice('/api/code/'.length)
  if (path === '/api/openai/v1/models') return '/api/code-page/openai/models'
  return path
}

/** The sessions started on this page, by id: the chat the Studio made for
 *  each, the machine id its view is under, and its first message. */
export interface StartedSession { conversationId?: string; shownOn?: string; first?: string }
const startedHere = new Map<string, StartedSession>()
export const startedSession = (id: string): StartedSession | undefined => startedHere.get(id)

/** A session that is the person's before the listing has it: one their open
 *  session was cleared into. */
export const knowSession = (id: string, s: StartedSession): void => { startedHere.set(id, s) }

/** Keeps what the Studio answered a start with, which the client's own
 *  types leave out. */
async function keepStarted(request: Request, body: ArrayBuffer | undefined, res: Response): Promise<void> {
  if (request.method !== 'POST' || !res.ok || !/\/machines\/[^/]+\/sessions$/.test(new URL(request.url).pathname)) return
  const d = await res.clone().json().catch(() => null) as ({ id?: string } & StartedSession) | null
  if (!d?.id) return
  let first: string | undefined
  try { first = (JSON.parse(new TextDecoder().decode(body)) as { prompt?: string }).prompt } catch { /* no message */ }
  startedHere.set(d.id, { conversationId: d.conversationId, shownOn: d.shownOn, first })
}

/** The same request at the Studio's path. A Request is not a RequestInit:
 *  passed as one it loses its body, or throws on a streamed one. */
const pageFetch = async (request: Request): Promise<Response> => {
  const url = new URL(request.url)
  url.pathname = pagePath(url.pathname)
  const body = request.method === 'GET' || request.method === 'HEAD' ? undefined : await request.arrayBuffer()
  const res = await fetch(url, { method: request.method, headers: request.headers, body, signal: request.signal, credentials: 'same-origin' })
  await keepStarted(request, body, res)
  return res
}

export const pageClient = createClient<PwCodePaths>({
  baseUrl: typeof location === 'undefined' ? '' : location.origin,
  fetch: pageFetch,
})

/** The address of the listing's feed: the Studio sends the listing as it
 *  changes, so the page does not read it on a timer. The page opens no other. */
const listingFeed = (path: string) => new WebSocket(liveURL('/api/code-page/feed' + path.slice('/ws/code/sessions'.length)))

export const pwCodePage = createPwCodeApi(pageClient, {
  openSocket: listingFeed,
  hidden: () => document.hidden,
})

type Listing = NonNullable<ReturnType<typeof pwCodePage.useSessions>['data']>
/** A session and a machine as the page's listing gives them. */
export type PageSession = NonNullable<Listing['sessions']>[number]
export type PageMachine = NonNullable<Listing['machines']>[number]
