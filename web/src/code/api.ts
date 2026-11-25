import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths } from '@parallelworks/code'
import { feedSocket, liveURL } from '../live'

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
/** Its start is not over: it was made empty, and its first images or command are yet to go. */
type KeptStart = StartedSession & { filling?: boolean }
const startedHere = new Map<string, KeptStart>()
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
  startedHere.set(d.id, { conversationId: d.conversationId, shownOn: d.shownOn, first, filling: !first })
}

/** The session's start is over: what is done to it from here is the person's. */
export function startedWell(id: string): void {
  const s = startedHere.get(id)
  if (s) s.filling = false
}

const SESSION_PATH = /^\/api\/code\/machines\/([^/]+)\/sessions\/([^/]+)(\/(?:goal|plan|images|messages))?$/

/** Where a start's own first requests to the session it made go (an image,
 *  /goal or /plan, the first message): to its chat's session, as its
 *  composer's would, where the Studio's rules for them are. */
export function startPath(method: string, path: string): string | null {
  const m = SESSION_PATH.exec(path)
  const s = m && startedHere.get(m[2])
  return m && s?.filling && method === 'POST' && m[3] ? `/api/code/machines/${encodeURIComponent(s.shownOn ?? m[1])}/sessions/${m[2]}${m[3]}` : null
}

/** A session made for a start that then failed is deleted by the package,
 *  and its chat, which holds nothing, with it. */
async function forgetFailed(request: Request, res: Response): Promise<void> {
  const m = SESSION_PATH.exec(new URL(request.url).pathname)
  const s = m && startedHere.get(m[2])
  if (!m || m[3] || !s?.filling || request.method !== 'DELETE' || !res.ok) return
  startedHere.delete(m[2])
  if (s.conversationId) await fetch(`/api/chat/conversations/${encodeURIComponent(s.conversationId)}`, { method: 'DELETE' }).catch(() => {})
}

/** Whether the next start on a machine adds the Studio's tools there first,
 *  as the person chose on the page; the Studio reads it and sends it no
 *  further. With no choice, the one they made before stands. */
const toolsChoice = new Map<string, boolean>()
export function setToolsChoice(machine: string, add: boolean | undefined): void {
  if (add === undefined) toolsChoice.delete(machine)
  else toolsChoice.set(machine, add)
}

function withToolsChoice(request: Request, path: string, body: ArrayBuffer | undefined): ArrayBuffer | string | undefined {
  const machine = request.method === 'POST' && body && /\/machines\/([^/]+)\/sessions$/.exec(path)?.[1]
  const add = machine ? toolsChoice.get(decodeURIComponent(machine)) : undefined
  if (add === undefined) return body
  try { return JSON.stringify({ ...JSON.parse(new TextDecoder().decode(body)), studioTools: add }) } catch { return body }
}

/** The same request at the Studio's path. A Request is not a RequestInit:
 *  passed as one it loses its body, or throws on a streamed one. */
const pageFetch = async (request: Request): Promise<Response> => {
  const url = new URL(request.url)
  url.pathname = startPath(request.method, url.pathname) ?? pagePath(url.pathname)
  const body = request.method === 'GET' || request.method === 'HEAD' ? undefined : await request.arrayBuffer()
  const res = await fetch(url, { method: request.method, headers: request.headers, body: withToolsChoice(request, url.pathname, body), signal: request.signal, credentials: 'same-origin' })
  await keepStarted(request, body, res)
  await forgetFailed(request, res)
  return res
}

export const pageClient = createClient<PwCodePaths>({
  baseUrl: typeof location === 'undefined' ? '' : location.origin,
  fetch: pageFetch,
})

/** The address of the listing's feed: the Studio sends the listing as it
 *  changes, so the page does not read it on a timer. The page opens no other. */
const listingFeed = (path: string) => feedSocket(liveURL('/api/code-page/feed' + path.slice('/ws/code/sessions'.length)))

export const pwCodePage = createPwCodeApi(pageClient, {
  openSocket: listingFeed,
  hidden: () => document.hidden,
})

type Listing = NonNullable<ReturnType<typeof pwCodePage.useSessions>['data']>
/** A session and a machine as the page's listing gives them. */
export type PageSession = NonNullable<Listing['sessions']>[number]
export type PageMachine = NonNullable<Listing['machines']>[number]
