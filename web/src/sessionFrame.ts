/**
 * Whether the browser can show a platform session inside the Studio.
 *
 * The platform signs a browser in to each session host with a cookie set
 * on that host alone, SameSite=Lax. A frame receives it only when every
 * page around the frame is on the same site as the session. That holds
 * when the Studio, itself a session, is open in its own tab at an address
 * under the same domain as the session. It does not hold for a session
 * served under the platform's own address (/me/session/...), whose pages
 * also refuse to be framed, or for a Studio opened inside the platform's
 * page, whose top frame is the platform's site. Those open in a new tab.
 *
 * A first visit still needs the session's own tab once: the sign-in goes
 * through the platform's login page, which cannot load in a frame. The
 * viewer says so beside the frame.
 */

export interface FramePlace { host: string; topIsSelf: boolean }
export type FrameVerdict = 'frame' | 'platform-address' | 'inside-platform' | 'other-site' | 'no-address'

/** A host's site, approximated by its last two labels; null for a bare
 *  name or an address, which share a site with no session host. */
export function siteOf(host: string): string | null {
  if (!host || !host.includes('.') || host.includes(':') || /^[\d.]+$/.test(host)) return null
  return host.toLowerCase().split('.').slice(-2).join('.')
}

export function frameVerdict(session: { url: string | null; ownHost: boolean }, here: FramePlace): FrameVerdict {
  if (!session.url) return 'no-address'
  if (!session.ownHost) return 'platform-address'
  if (!here.topIsSelf) return 'inside-platform'
  let host = ''
  try { host = new URL(session.url).hostname } catch { return 'no-address' }
  const a = siteOf(host)
  return a && a === siteOf(here.host) ? 'frame' : 'other-site'
}

/** Where this page is being viewed: its host, and whether the top frame is
 *  the Studio itself, as a chat embed's parent is. */
export function currentPlace(): FramePlace {
  let topIsSelf = false
  try { topIsSelf = window.top === window || window.top?.location.origin === window.location.origin } catch { topIsSelf = false }
  return { host: window.location.hostname, topIsSelf }
}

/**
 * Whether this browser has signed in to a session host, as far as the
 * Studio can tell. The sign-in itself is a cookie the Studio cannot read,
 * so the Studio remembers when it last sent the browser through it, and
 * asks again after SIGNIN_TTL_MS. The cookie lasts as long as the
 * platform login, so a repeat sign-in is a redirect that takes a moment.
 */
const SIGNIN_TTL_MS = 12 * 3600_000
const signinKey = (host: string) => `ade-session-signin:${host}`

export function signedInTo(host: string, now = Date.now()): boolean {
  try {
    const at = Number(localStorage.getItem(signinKey(host)))
    return !!at && now - at < SIGNIN_TTL_MS
  } catch { return false }
}

export function markSignedIn(host: string, signed: boolean): void {
  try {
    if (signed) localStorage.setItem(signinKey(host), String(Date.now()))
    else localStorage.removeItem(signinKey(host))
  } catch { /* storage off: the sign-in step shows each time */ }
}
