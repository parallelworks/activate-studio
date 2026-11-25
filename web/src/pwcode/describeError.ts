/** Words for the codes the package leaves when it leaves the server's out. */
const CODE_WORDS: Record<string, string> = {
  invalid_request: 'The request was not one the Studio takes.',
  unauthenticated: 'The sign-in has ended. Sign in again to go on.',
  forbidden: 'That is not allowed here.',
  not_found: 'It is no longer there.',
  conflict: 'It changed meanwhile. Reload and try again.',
  rate_limited: 'Too many requests. Wait a moment and try again.',
  internal: 'Something went wrong. Try again.',
  unavailable: 'Not available for now. Try again shortly.',
  network: 'The Studio did not answer.',
}

/** Words a refusal or failure the way the platform's problems carry them. */
export function describeError(error: unknown): string {
  if (!error) return ''
  if (typeof error === 'string') return error
  const e = error as { detail?: unknown; title?: unknown; message?: unknown; error?: unknown; code?: unknown }
  // An error by its code alone (an ApiError with no text) says the code.
  if (typeof e.code === 'string' && e.message === e.code && CODE_WORDS[e.code]) return CODE_WORDS[e.code]
  for (const v of [e.detail, e.error, e.message, e.title]) if (typeof v === 'string' && v) return v
  return String(error)
}
