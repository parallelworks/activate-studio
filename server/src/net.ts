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

// ---- certificates and proxies ----
//
// Not the network being away: the connection was made and refused for a
// reason the person can act on. A network that inspects HTTPS presents
// certificates from its own authority, which fail with "unable to get local
// issuer certificate" until that authority is trusted (the server adds the
// system's trusted certificates at start, netTrust.ts). A proxy that will not
// open the connection is told apart from an offline computer, which retries.

/** OpenSSL and Node codes for a certificate the client would not accept. */
const CERT_CODES: Record<string, 'untrusted' | 'expired' | 'name'> = {
  UNABLE_TO_GET_ISSUER_CERT_LOCALLY: 'untrusted', UNABLE_TO_GET_ISSUER_CERT: 'untrusted',
  UNABLE_TO_VERIFY_LEAF_SIGNATURE: 'untrusted', SELF_SIGNED_CERT_IN_CHAIN: 'untrusted',
  DEPTH_ZERO_SELF_SIGNED_CERT: 'untrusted', CERT_UNTRUSTED: 'untrusted', CERT_REJECTED: 'untrusted',
  CERT_SIGNATURE_FAILURE: 'untrusted', CERT_CHAIN_TOO_LONG: 'untrusted', INVALID_CA: 'untrusted',
  UNABLE_TO_DECRYPT_CERT_SIGNATURE: 'untrusted', UNABLE_TO_DECODE_ISSUER_PUBLIC_KEY: 'untrusted',
  CERT_HAS_EXPIRED: 'expired', CERT_NOT_YET_VALID: 'expired',
  ERR_TLS_CERT_ALTNAME_INVALID: 'name', HOSTNAME_MISMATCH: 'name',
}

export interface TrustFailure {
  code: string
  kind: 'untrusted' | 'expired' | 'name'
  /** OpenSSL's own words, without Node's advice about flags. */
  reason: string
}

/** The errors an error was caused by, outermost first. */
type Caused = { code?: unknown; message?: string; cause?: unknown }
function causes(e: unknown): Caused[] {
  const out: Caused[] = []
  const seen = new Set<unknown>()
  for (let cur = e, depth = 0; cur && depth < 6 && !seen.has(cur); depth++) {
    seen.add(cur)
    out.push(cur as Caused)
    cur = (cur as { cause?: unknown }).cause
  }
  return out
}

/** The certificate failure behind an error, or null. */
export function trustFailure(e: unknown): TrustFailure | null {
  for (const err of causes(e)) {
    const code = typeof err.code === 'string' ? err.code : ''
    const kind = CERT_CODES[code]
    if (kind) return { code, kind, reason: String(err.message ?? code).split('; if the root CA')[0].trim() }
  }
  return null
}

/** A sentence for a person about a certificate the connection was refused over. */
export function trustMessage(host: string, f: TrustFailure, platform: NodeJS.Platform = process.platform): string {
  const where = host || 'the server'
  if (f.kind === 'expired') return `The certificate ${where} presented is out of date (${f.reason}). Check this computer's date and time; if they are right, the certificate needs renewing.`
  if (f.kind === 'name') return `The certificate ${where} presented is for a different name (${f.reason}), so the connection was refused. A network that inspects HTTPS can cause this.`
  const trust = platform === 'darwin' ? 'trusted by macOS (in Keychain Access, or installed by the Mac\'s management)'
    : platform === 'win32' ? 'trusted by Windows'
      : 'in this computer\'s trusted certificates (the system store, or a file named by NODE_EXTRA_CA_CERTS)'
  return `The connection to ${where} used a certificate this computer does not trust (${f.reason}). If your network inspects HTTPS, its certificate authority has to be ${trust}; the Studio uses the system's trusted certificates.`
}

/** The proxy the environment names for a request, or null (none, or the host is excepted). */
export function proxyFor(host: string, env: NodeJS.ProcessEnv = process.env): URL | null {
  const raw = env.HTTPS_PROXY || env.https_proxy || env.HTTP_PROXY || env.http_proxy
  if (!raw) return null
  let proxy: URL
  try { proxy = new URL(raw.includes('://') ? raw : `http://${raw}`) } catch { return null }
  const name = host.replace(/:\d+$/, '').replace(/^\[|\]$/g, '').toLowerCase()
  for (const entry of String(env.NO_PROXY ?? env.no_proxy ?? '').split(',').map(s => s.trim().toLowerCase()).filter(Boolean)) {
    if (entry === '*') return null
    const bare = entry.replace(/^\*?\./, '').replace(/:\d+$/, '')
    if (name === bare || name.endsWith(`.${bare}`)) return null
  }
  return proxy
}

/** Failures to open a connection, as opposed to one lost once open. */
const PROXY_CONNECT_CODES = new Set(['ENOTFOUND', 'EAI_AGAIN', 'EAI_FAIL', 'ECONNREFUSED', 'ETIMEDOUT', 'EHOSTUNREACH', 'ENETUNREACH', 'UND_ERR_CONNECT_TIMEOUT'])

export interface ProxyFailure {
  /** The proxy, without any password. */
  proxy: string
  /** ENOTFOUND, ECONNREFUSED, ... for a proxy that could not be reached; 'refused' for one that would not open the connection. */
  code: string
  /** The proxy's answer to the tunnel request, when it refused one (407, 403, ...). */
  status?: number
}

/** The proxy failure behind an error for a request to `host`, or null. */
export function proxyFailure(e: unknown, host: string, env: NodeJS.ProcessEnv = process.env): ProxyFailure | null {
  const proxy = proxyFor(host, env)
  if (!proxy) return null
  const shown = `${proxy.hostname}${proxy.port ? `:${proxy.port}` : ''}`
  for (const err of causes(e)) {
    const code = typeof err.code === 'string' ? err.code : ''
    const text = String(err.message ?? '')
    // Through a proxy this computer only ever connects to the proxy, so a
    // failure to connect is the proxy's: its name not found, its port
    // refusing or silent.
    if (PROXY_CONNECT_CODES.has(code)) return { proxy: shown, code }
    // A proxy that answered the tunnel request with anything but 200 (407,
    // 403, 502, ...): "Proxy response (407) !== 200 when HTTP Tunneling".
    const answered = /Proxy response \((\d{3})\)/.exec(text)
    if (answered) return { proxy: shown, code: 'refused', status: Number(answered[1]) }
  }
  return null
}

/** A sentence for a person about a proxy that did not carry the request. */
export function proxyMessage(host: string, f: ProxyFailure): string {
  const to = host || 'the server'
  if (f.code === 'refused' && f.status === 407) return `The proxy at ${f.proxy} asks for a sign-in (407), so it did not open a connection to ${to}. The Studio signs in with a user name and password given in the proxy address (HTTPS_PROXY=http://user:password@host:port); other kinds of proxy sign-in are not supported.`
  if (f.code === 'refused') return `The proxy at ${f.proxy} would not open a connection to ${to} (it answered ${f.status ?? 'with a refusal'}).`
  return `The proxy at ${f.proxy} could not be reached (${f.code}), so ${to} could not be either. Check the proxy setting (HTTPS_PROXY, or the system's proxy settings in the macOS app).`
}

/** A sentence about a certificate or proxy problem behind an error for a
 *  request to `host`, or null when the error is neither (it may still be
 *  the network being away, which networkFailure tells). */
export function connectionProblem(e: unknown, host: string, env: NodeJS.ProcessEnv = process.env): string | null {
  const t = trustFailure(e)
  if (t) return trustMessage(host, t)
  const p = proxyFailure(e, host, env)
  if (p) return proxyMessage(host, p)
  return null
}
