/**
 * Settings, Accounts: where the Studio signs in for the person (ACTIVATE,
 * other model services). Every page that needs ACTIVATE and finds no
 * credential points here with the same words (NeedsActivate).
 */
export const ACCOUNTS_HREF = '#view=settings:accounts'

/** The accounts on the page, each a row that opens: an OpenAI-compatible
 *  endpoint, ACTIVATE, and the Claude Code accounts pw code signs in with. */
export type AccountRowId = 'endpoint' | 'activate' | 'claude-code'
const ROWS: readonly AccountRowId[] = ['endpoint', 'activate', 'claude-code']

/** Said on the window when a link asks for the row the address already
 *  names, which changes no address. */
export const OPEN_ACCOUNT = 'ade:open-account'

/** Settings, Accounts with one account's row open and brought into view,
 *  as `#view=settings:accounts?open=endpoint`; the page alone without one. */
export function accountsHref(row?: AccountRowId): string {
  return row ? `${ACCOUNTS_HREF}?open=${row}` : ACCOUNTS_HREF
}

/** The row an address of Settings, Accounts asks to open, if any. */
export function accountInHash(hash: string): AccountRowId | null {
  const m = hash.match(/^#view=settings:accounts\?(?:.*&)?open=([a-z-]+)(?:&|$)/)
  return m && (ROWS as readonly string[]).includes(m[1]) ? m[1] as AccountRowId : null
}

/** Opens Settings, Accounts, at one account's row when given one. */
export function openAccounts(row?: AccountRowId): void {
  const href = accountsHref(row)
  if (row && location.hash === href) window.dispatchEvent(new CustomEvent(OPEN_ACCOUNT, { detail: row }))
  else location.hash = href
}

/** What a feature that needs ACTIVATE says when the person has no credential for it. */
export const SIGN_IN_TO_USE = 'Sign in with ACTIVATE to use this'

/** What it says instead when the person's kept ACTIVATE sign-in stopped
 *  working (the platform no longer accepts it, or it could not be renewed). */
export const SIGN_IN_STOPPED = 'Your ACTIVATE sign-in stopped working'

/** What a place that asks for the sign-in offers when it stopped working. */
export const SIGN_IN_AGAIN = 'Sign in again'

/** Whether a server message is the one a feature gives for a missing
 *  ACTIVATE credential: those name Settings, Accounts as the remedy. */
export const needsActivate = (message: string | null | undefined): boolean => !!message && /Settings, Accounts/.test(message)
