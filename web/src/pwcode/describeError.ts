/** Words a refusal or failure the way the platform's problems carry them. */
export function describeError(error: unknown): string {
  if (!error) return ''
  if (typeof error === 'string') return error
  const e = error as { detail?: unknown; title?: unknown; message?: unknown; error?: unknown }
  for (const v of [e.detail, e.error, e.message, e.title]) if (typeof v === 'string' && v) return v
  return String(error)
}
