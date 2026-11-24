import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths } from '@parallelworks/code'

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

/** The same request at the Studio's path. A Request is not a RequestInit:
 *  passed as one it loses its body, or throws on a streamed one. */
const pageFetch = async (request: Request): Promise<Response> => {
  const url = new URL(request.url)
  url.pathname = pagePath(url.pathname)
  const body = request.method === 'GET' || request.method === 'HEAD' ? undefined : await request.arrayBuffer()
  return fetch(url, { method: request.method, headers: request.headers, body, signal: request.signal, credentials: 'same-origin' })
}

export const pageClient = createClient<PwCodePaths>({
  baseUrl: typeof location === 'undefined' ? '' : location.origin,
  fetch: pageFetch,
})

/** No feed of the listing here: the page reads it again on the package's own timer. */
export const pwCodePage = createPwCodeApi(pageClient, {
  hidden: () => document.hidden,
})

type Listing = NonNullable<ReturnType<typeof pwCodePage.useSessions>['data']>
/** A session and a machine as the page's listing gives them. */
export type PageSession = NonNullable<Listing['sessions']>[number]
export type PageMachine = NonNullable<Listing['machines']>[number]
