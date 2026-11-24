/**
 * Settings, Accounts: where the Studio signs in for the person (ACTIVATE,
 * other model services). Every page that needs ACTIVATE and finds no
 * credential points here with the same words (NeedsActivate).
 */
export const ACCOUNTS_HREF = '#view=settings:accounts'

/** Opens Settings, Accounts. */
export function openAccounts(): void { location.hash = ACCOUNTS_HREF }

/** What a feature that needs ACTIVATE says when the person has no credential for it. */
export const SIGN_IN_TO_USE = 'Sign in with ACTIVATE to use this'

/** Whether a server message is the one a feature gives for a missing
 *  ACTIVATE credential: those name Settings, Accounts as the remedy. */
export const needsActivate = (message: string | null | undefined): boolean => !!message && /Settings, Accounts/.test(message)
