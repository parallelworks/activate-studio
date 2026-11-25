/** Inside the platform's embedded session view a provider's sign-in does
 *  not complete, so there it opens in a tab of its own. */
export function embedded(): boolean {
  try { return window.self !== window.top } catch { return true }
}

export function signInHref(loginUrl: string): string {
  return `${loginUrl}?returnTo=${encodeURIComponent(location.pathname + location.search + location.hash)}`
}
