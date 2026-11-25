import { linkStart } from './itemLinks'

/** Inside the platform's embedded session view a provider's sign-in does
 *  not complete, so there it opens in a tab of its own. */
export function embedded(): boolean {
  try { return window.self !== window.top } catch { return true }
}

export function signInHref(loginUrl: string): string {
  return `${loginUrl}?returnTo=${encodeURIComponent(location.pathname + location.search + location.hash)}`
}

/** Where someone who has not signed in does so, back to this place after:
 *  the Studio's own page, else the Studio's address on the platform, which
 *  asks first. '' where neither is known, or they are there already. */
export function signInPlace(cfg: { signInUrl?: string; linkBase?: string }): string {
  if (cfg.signInUrl) return signInHref(cfg.signInUrl)
  if (!cfg.linkBase || linkStart(cfg.linkBase) === linkStart('')) return ''
  return `${linkStart(cfg.linkBase)}/${location.hash}`
}

/** Sent where a chat is refused for want of a sign-in. */
export const SIGN_IN_TO_CHAT = 'ade:sign-in-to-chat'
