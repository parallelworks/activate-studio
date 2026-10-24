/**
 * Outbound HTTPS from the server on a network that inspects TLS with its own
 * certificate authority, and through a proxy.
 *
 * Node trusts its own bundled list of certificate authorities, not the
 * operating system's. A network that inspects HTTPS presents certificates
 * from its own authority, which the organisation adds to the system (a
 * keychain on a Mac, the system bundle on Linux), so every call out failed
 * with "unable to get local issuer certificate": the update check and its
 * download, model providers, the platform. At start the server adds the
 * system's trusted certificates to Node's own: the set `--use-system-ca`
 * reads, which on macOS includes certificates an administrator or the Mac's
 * management installed. Certificates named by NODE_EXTRA_CA_CERTS are kept.
 * STUDIO_SYSTEM_CA=0 leaves Node's own list alone.
 *
 * Node's fetch also goes straight out unless told about a proxy. With
 * HTTPS_PROXY or HTTP_PROXY set, the server sends its requests through it,
 * except to this computer (localhost, 127.0.0.1 and ::1 are added to
 * NO_PROXY). The macOS app's run loop sets those variables from the system's
 * proxy settings when none are given (deploy/mac/studio-run.sh).
 *
 * Done at runtime, in the server's first import, rather than with Node
 * flags, so the macOS app, the container and a clone started with
 * `pnpm start` all get it the same way.
 */
import http from 'node:http'
import tls from 'node:tls'

export interface OutboundSetup {
  /** How many certificates the system added to Node's own, or null when not added. */
  systemCertificates: number | null
  /** The proxy requests go through, without any password, or null. */
  proxy: string | null
  /** Hosts that go direct, as NO_PROXY gives them. */
  noProxy: string | null
  /** What went wrong setting either up, or null. */
  error: string | null
}

export const outbound: OutboundSetup = { systemCertificates: null, proxy: null, noProxy: null, error: null }

const LOOPBACK = ['localhost', '127.0.0.1', '::1']

/** Trust the system's certificate authorities as well as Node's bundled ones. */
export function trustSystemCertificates(env: NodeJS.ProcessEnv = process.env): void {
  if (env.STUDIO_SYSTEM_CA === '0') return
  try {
    // 'default' is Node's bundled list plus NODE_EXTRA_CA_CERTS.
    const own = tls.getCACertificates('default')
    const system = tls.getCACertificates('system')
    tls.setDefaultCACertificates([...new Set([...own, ...system])])
    outbound.systemCertificates = system.length
  } catch (e) {
    outbound.error = `could not read the system's trusted certificates: ${String((e as Error).message ?? e)}`
  }
}

/** A proxy address with any password left out, for logs and messages. */
export function redactProxy(raw: string): string {
  try {
    const u = new URL(raw.includes('://') ? raw : `http://${raw}`)
    if (u.password) u.password = '***'
    return u.toString().replace(/\/$/, '')
  } catch {
    return '(an address that does not parse)'
  }
}

/** Send requests through the proxy the environment names, if any. */
export function useProxyFromEnv(env: NodeJS.ProcessEnv = process.env): void {
  const proxy = env.HTTPS_PROXY || env.https_proxy || env.HTTP_PROXY || env.http_proxy
  if (!proxy) return
  const given = env.NO_PROXY ?? env.no_proxy ?? ''
  const have = new Set(given.split(',').map(s => s.trim().toLowerCase()).filter(Boolean))
  const noProxy = [given.trim(), ...LOOPBACK.filter(h => !have.has(h))].filter(Boolean).join(',')
  // Both spellings, so a child program reads the same list whichever it uses.
  env.NO_PROXY = noProxy
  env.no_proxy = noProxy
  const set = (http as unknown as { setGlobalProxyFromEnv?: (proxyEnv?: NodeJS.ProcessEnv) => void }).setGlobalProxyFromEnv
  if (typeof set !== 'function') {
    outbound.error = 'this Node has no built-in proxy support, so HTTPS_PROXY is not followed'
    return
  }
  set(env)
  outbound.proxy = redactProxy(proxy)
  outbound.noProxy = noProxy
}

/** One line for the server's log about how it reaches out. */
export function outboundLine(): string {
  const parts = [outbound.systemCertificates === null
    ? 'outbound HTTPS trusts Node\'s own certificate list'
    : `outbound HTTPS trusts the system's certificates (${outbound.systemCertificates}) as well as Node's own`]
  if (outbound.proxy) parts.push(`through the proxy ${outbound.proxy} (not for ${outbound.noProxy})`)
  if (outbound.error) parts.push(`(${outbound.error})`)
  return parts.join(', ')
}
