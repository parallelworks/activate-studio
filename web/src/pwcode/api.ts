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

/** The Studio's address for a feed the package names by its platform path. */
export function feedURL(path: string, origin = location.origin): string {
  return origin.replace(/^http/, 'ws') + '/api/pwcode-feed' + path.replace(/^\/ws\/code/, '')
}

export const pwCode = createPwCodeApi(pwClient, {
  openSocket: path => new WebSocket(feedURL(path)),
  hidden: () => document.hidden,
})

/** The machine id a chat's session is shown under: its platform machine
 *  for a relayed session, else the Studio's own (directMachine). */
export function sessionMachine(link: { relay?: boolean; machine?: string }): string {
  return link.relay && link.machine ? link.machine : 'studio'
}
