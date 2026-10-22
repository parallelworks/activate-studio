/**
 * Telling a network failure from every other failure.
 *
 * A Studio on a laptop is often offline, and everything that works from
 * local files has to keep working then. What breaks is anything that calls
 * out: the update check, the model gateway, attached MCP servers, the
 * platform. Each of those reports a missing network in plain words, as a
 * state rather than an error, and none of them waits long for it.
 *
 * Node's fetch reports a lost connection as `TypeError: fetch failed` with
 * the system error as its cause (ENOTFOUND, ECONNRESET, ...); a timeout from
 * AbortSignal.timeout is a TimeoutError. The pw CLI, a Go program, prints
 * its own wording for the same causes.
 */

/** System and undici error codes that mean the other end could not be reached. */
const NETWORK_CODES = new Set([
  'ENOTFOUND', 'EAI_AGAIN', 'EAI_FAIL', 'EAI_NODATA', 'ECONNRESET', 'ECONNREFUSED', 'ECONNABORTED',
  'ETIMEDOUT', 'ENETUNREACH', 'ENETDOWN', 'EHOSTUNREACH', 'EHOSTDOWN', 'EPIPE',
  'UND_ERR_CONNECT_TIMEOUT', 'UND_ERR_HEADERS_TIMEOUT', 'UND_ERR_BODY_TIMEOUT', 'UND_ERR_SOCKET',
])

/** The CLI's wording for the same causes (Go's net package and its HTTP client). */
const CLI_NETWORK = /no such host|connection refused|connection reset|network is unreachable|host is unreachable|i\/o timeout|context deadline exceeded|TLS handshake timeout|dial tcp|server misbehaving|temporary failure in name resolution/i

export interface NetworkFailure {
  /** The code that identified it: ENOTFOUND, ECONNRESET, timeout, ... */
  code: string
  /** True when the cause is the name lookup failing, which is what no network at all looks like. */
  noDns: boolean
}

/** The network failure behind an error, or null for any other kind of error. */
export function networkFailure(e: unknown): NetworkFailure | null {
  const seen = new Set<unknown>()
  let cur: unknown = e
  for (let depth = 0; cur && depth < 5 && !seen.has(cur); depth++) {
    seen.add(cur)
    const err = cur as { name?: string; code?: string; message?: string; cause?: unknown }
    if (err.name === 'TimeoutError') return { code: 'timeout', noDns: false }
    const code = typeof err.code === 'string' ? err.code : ''
    if (NETWORK_CODES.has(code)) return { code, noDns: code.startsWith('EAI_') || code === 'ENOTFOUND' }
    // A code can also arrive only in the text ("read ECONNRESET", "getaddrinfo ENOTFOUND host").
    const inText = /\b(E[A-Z_]{4,}|UND_ERR_[A-Z_]+)\b/.exec(String(err.message ?? ''))?.[1]
    if (inText && NETWORK_CODES.has(inText)) return { code: inText, noDns: inText.startsWith('EAI_') || inText === 'ENOTFOUND' }
    cur = err.cause
  }
  return null
}

/** Whether a pw CLI failure, given as its output, is the network rather than the request. */
export function cliNetworkFailure(text: string): boolean {
  return CLI_NETWORK.test(text)
}

/** A host on this computer, where a refused connection means the server is
 *  not running, whatever the network is doing. */
export function isLoopbackHost(host: string): boolean {
  const name = host.replace(/:\d+$/, '').replace(/^\[|\]$/g, '').toLowerCase()
  return name === 'localhost' || name === '::1' || /^127\./.test(name)
}

/** A sentence for a person about a host that could not be reached. */
export function unreachableMessage(host: string, f: NetworkFailure | null): string {
  const why = !f ? '' : f.code === 'timeout' ? ' (no answer in time)' : ` (${f.code})`
  if (isLoopbackHost(host)) return `Nothing answers at ${host}${why}. Start the model server on this computer, or check its address.`
  return `Could not reach ${host || 'the server'}${why}. This computer may be offline, or the server is down. Local libraries and a local model keep working.`
}
