import type { FastifyInstance, FastifyRequest } from 'fastify'
import { authEnabled } from './auth.js'
import { GATEWAY_BASE, GATEWAY_FROM_ENV, PW_API_KEY, choosePwCli, chosenPwCli, pwCliCommonDirs } from './config.js'
import { clearUserKey, getSharedKeyStatus, getUserKeyStatus, personalKeysDisabled, resolveSharedKey, resolveUserCred, SINGLE_OWNER } from './credentials.js'
import { gatewayKey, invalidateProviderProbes, sidecarConfigured } from './chat/gateway.js'
import {
  activeContextName, cancelCliLogin, chooseContext, chosenContext, cliCredential, cliInfo, credentialContextName,
  ensureFreshAccess, listCliContexts, loginState, startCliLogin,
} from './access.js'
import { KbError } from './kb.js'
import { networkFailure, unreachableMessage } from './net.js'

/**
 * Model access, as one picture: where model calls and platform calls get
 * their credential right now, the pw CLI's contexts and which one is used,
 * signing the CLI in to a platform, and subscriptions connected through the
 * platform (a ChatGPT plan, GitHub Copilot).
 *
 * The order of precedence is the one the rest of the server applies:
 * models use the person's own key or endpoint, then a key someone shared
 * with the deployment, then PW_API_KEY, then the pw CLI; platform calls use
 * the person's own platform key, then the same chain. This route only
 * reports it.
 *
 * The CLI's contexts belong to whoever runs the server. On a Studio with
 * sign-in off that is its one user, who may see and choose them; on a
 * deployment that signs people in, they are the deployment's, so viewers see
 * only that the deployment credential exists and which host it reaches.
 */

type Source = 'endpoint' | 'key' | 'shared' | 'env' | 'cli' | 'sidecar' | 'none'
interface SourceInfo { source: Source; host: string | null; context: string | null; baseUrl?: string | null; last4?: string | null; kind?: string | null }

const hostOf = (url: string | null | undefined) => { try { return new URL(String(url)).host } catch { return null } }

/** The deployment's own chain: a shared key, PW_API_KEY, the pw CLI. */
function deploymentSource(single: boolean): SourceInfo {
  const host = hostOf(GATEWAY_BASE)
  if (resolveSharedKey()) return { source: 'shared', host, context: null, last4: getSharedKeyStatus().last4 ?? null }
  if (PW_API_KEY) return { source: 'env', host, context: null }
  if (cliCredential()) return { source: 'cli', host, context: single ? credentialContextName() : null }
  return { source: 'none', host, context: null }
}

export function accessSources(userId: string | undefined): { models: SourceInfo; platform: SourceInfo } {
  const single = !authEnabled()
  const owner = userId ?? (single ? SINGLE_OWNER : null)
  const cred = resolveUserCred(userId)
  const status = owner && !personalKeysDisabled() ? getUserKeyStatus(owner) : null
  const dep = deploymentSource(single)
  const mine = (source: Source, host: string | null): SourceInfo =>
    ({ source, host, context: null, baseUrl: cred?.baseUrl ?? null, last4: status?.last4 ?? null, kind: status?.kind ?? null })
  const models: SourceInfo = cred?.baseUrl ? mine('endpoint', hostOf(cred.baseUrl))
    : cred ? mine('key', hostOf(GATEWAY_BASE))
    : dep.source !== 'none' ? dep
    : sidecarConfigured() ? { source: 'sidecar', host: null, context: null }
    : dep
  const platform: SourceInfo = cred && !cred.baseUrl ? mine('key', hostOf(GATEWAY_BASE)) : dep
  return { models, platform }
}

/** The platform credential that is the person's own: their platform key,
 *  or, with sign-in off, whatever the one user's Studio runs with. Never
 *  the deployment's on a shared deployment, since what it connects lands in
 *  that account. */
function ownPlatformKey(req: FastifyRequest): string | null {
  const cred = resolveUserCred(req.user?.id)
  if (cred && !cred.baseUrl) return cred.key
  if (!authEnabled()) return gatewayKey() || null
  return null
}

/* ---- the platform API, for subscriptions ---- */

async function platformCall(key: string, method: 'GET' | 'POST', path: string, body?: unknown): Promise<unknown> {
  let origin = ''
  try { origin = new URL(GATEWAY_BASE).origin } catch { throw new KbError(409, 'No platform is configured.') }
  let res: Response
  try {
    res = await fetch(`${origin}${path}`, {
      method,
      headers: { Authorization: `Bearer ${key}`, Accept: 'application/json', ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}) },
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(15_000),
    })
  } catch (e) {
    const f = networkFailure(e)
    throw new KbError(503, f ? unreachableMessage(new URL(origin).host, f) : String((e as Error).message ?? e).slice(0, 200))
  }
  const text = await res.text()
  let data: unknown = text
  try { data = JSON.parse(text) } catch { /* plain text, as whoami answers */ }
  if (!res.ok) {
    const said = (data && typeof data === 'object' ? String((data as { message?: string; error?: string; detail?: string }).message ?? (data as { error?: string }).error ?? (data as { detail?: string }).detail ?? '') : text).slice(0, 300)
    if (res.status === 401 || res.status === 403) throw new KbError(res.status, `The platform refused this credential (${res.status})${said ? `: ${said}` : ''}. A platform token or platform API key is needed; an AI gateway key is not enough.`)
    throw new KbError(res.status === 404 ? 404 : 502, `The platform answered ${res.status}${said ? `: ${said}` : ''}`)
  }
  return data
}

const accounts = new Map<string, { at: number; org: string; user: string }>()
async function accountOf(key: string): Promise<{ org: string; user: string }> {
  const k = key.slice(-12)
  const hit = accounts.get(k)
  if (hit && Date.now() - hit.at < 10 * 60_000) return hit
  const who = String(await platformCall(key, 'GET', '/api/auth/whoami')).trim()
  const org = String(await platformCall(key, 'GET', '/api/auth/whoami/organization')).trim()
  const user = who.replace(/^user:/, '')
  if (!user || !org || user === who && who.includes(':')) throw new KbError(409, 'This credential does not belong to a platform user.')
  const v = { at: Date.now(), org, user }
  accounts.set(k, v)
  return v
}

interface Integration { id: string; kind: string; displayName?: string; lifecycle?: string }
interface Provider { name: string; displayName?: string | null; catalogEntryId?: string; status?: string; accountEmail?: string; accountLogin?: string }

const enc = encodeURIComponent

export async function accessRoutes(app: FastifyInstance): Promise<void> {
  const requireSingle = () => {
    if (authEnabled()) throw new KbError(403, 'The pw CLI on this server belongs to the deployment; choose a credential of your own under Model access instead.')
  }

  app.get('/api/access', async req => {
    const single = !authEnabled()
    await ensureFreshAccess(5_000)
    const sources = accessSources(req.user?.id)
    const fresh = String((req.query as { fresh?: string } | undefined)?.fresh ?? '') === '1'
    const info = single ? await cliInfo(fresh) : null
    return {
      single,
      authEnabled: authEnabled(),
      ...sources,
      // What requests fall back to without a key of the person's own.
      deployment: deploymentSource(single),
      gatewayHost: hostOf(GATEWAY_BASE),
      platformOrigin: (() => { try { return new URL(GATEWAY_BASE).origin } catch { return null } })(),
      cli: single
        ? {
            available: info!.available, version: info!.version, path: info!.path, source: info!.source,
            chosenPath: chosenPwCli(), commonDirs: pwCliCommonDirs(),
            contexts: listCliContexts(),
            chosen: chosenContext(),
            active: activeContextName(),
            used: credentialContextName(),
            gatewayFromEnv: GATEWAY_FROM_ENV,
            envKey: !!PW_API_KEY,
          }
        : null,
      login: single ? loginState() : null,
    }
  })

  // Where the pw CLI is, when the Studio does not find it by itself: a full
  // path to the program, or null to forget the choice and look as before.
  // Only on a Studio with sign-in off, since the server runs what it names.
  app.put('/api/access/cli/path', async req => {
    requireSingle()
    const raw = ((req.body ?? {}) as { path?: unknown }).path
    const p = raw === null || raw === undefined || String(raw).trim() === '' ? null : String(raw).trim()
    try { choosePwCli(p) } catch (e) { throw new KbError(400, String((e as Error).message)) }
    const info = await cliInfo(true)
    if (p && (!info.available || !info.version)) {
      choosePwCli(null)
      throw new KbError(400, `${p} did not answer --version as the pw CLI does, so it was not kept.`)
    }
    return { cli: info }
  })

  // Which context the Studio uses: a name, or null to follow the CLI's own
  // current context. clearKey also removes a stored key or endpoint, which
  // would otherwise keep taking precedence over the context.
  app.put('/api/access/source', async req => {
    requireSingle()
    const body = (req.body ?? {}) as { context?: string | null; clearKey?: boolean }
    const name = body.context === null || body.context === undefined || body.context === '' ? null : String(body.context)
    try { chooseContext(name) } catch (e) { throw new KbError(400, String((e as Error).message)) }
    if (body.clearKey) clearUserKey(SINGLE_OWNER)
    invalidateProviderProbes()
    accounts.clear()
    return { ok: true, chosen: chosenContext(), active: activeContextName(), ...accessSources(req.user?.id) }
  })

  // Signing the pw CLI in to a platform with its device flow.
  app.post('/api/access/cli/login', async req => {
    requireSingle()
    const host = String(((req.body ?? {}) as { host?: string }).host ?? '')
    try { return { login: await startCliLogin(host) } } catch (e) { throw new KbError(400, String((e as Error).message ?? e)) }
  })
  app.get('/api/access/cli/login', async () => {
    requireSingle()
    return { login: loginState() }
  })
  app.post('/api/access/cli/login/cancel', async () => {
    requireSingle()
    return { login: cancelCliLogin() }
  })

  // Subscriptions connected through the platform. The platform holds the
  // connection and serves the models through its gateway (as
  // me:<provider>/<model>), so once connected they list like any other.
  app.get('/api/access/subscriptions', async req => {
    const key = ownPlatformKey(req)
    if (!key) return { available: false, reason: 'Subscriptions connect to your own ACTIVATE account, which needs your platform token or platform API key here first.' }
    await ensureFreshAccess(5_000)
    const { org, user } = await accountOf(key)
    const [integrations, providers, github] = await Promise.all([
      // What the platform offers decides everything below, so a failure here
      // is reported rather than read as "not offered".
      platformCall(key, 'GET', `/api/organizations/${enc(org)}/ai-integrations`).then(d => ((d as { integrations?: Integration[] })?.integrations ?? (Array.isArray(d) ? d as Integration[] : []))),
      platformCall(key, 'GET', `/api/organizations/${enc(org)}/users/${enc(user)}/ai-providers`).then(d => (Array.isArray(d) ? d as Provider[] : [])).catch(() => [] as Provider[]),
      platformCall(key, 'GET', '/api/integrations/github/oauth/status').then(d => d as { appConfigured?: boolean; tokensAllowed?: boolean; username?: string }).catch(() => null),
    ])
    const offered = (kind: string) => integrations.find(i => i.kind === kind && (i.lifecycle ?? 'active') === 'active') ?? null
    const ofKind = (id: string | undefined) => providers.filter(p => id && p.catalogEntryId === id)
      .map(p => ({ name: p.name, displayName: p.displayName ?? null, status: p.status ?? null, account: p.accountEmail ?? p.accountLogin ?? null }))
    const codex = offered('codex-subscription')
    const copilot = offered('github-copilot')
    return {
      available: true,
      user, org,
      codex: { offered: !!codex, providers: ofKind(codex?.id) },
      copilot: {
        offered: !!copilot,
        providers: ofKind(copilot?.id),
        github: github ? { linked: !!github.username, username: github.username ?? null, appConfigured: !!github.appConfigured, tokensAllowed: !!github.tokensAllowed } : null,
      },
    }
  })

  // A ChatGPT plan, connected with OpenAI's device sign-in as the platform
  // runs it: the platform starts it and hands back the address and code,
  // the person approves at OpenAI, and the platform stores the connection.
  app.post('/api/access/codex', async req => {
    const key = ownPlatformKey(req)
    if (!key) throw new KbError(409, 'Add your platform token or platform API key first; the subscription connects to your own ACTIVATE account.')
    const body = (req.body ?? {}) as { name?: string; reconnect?: boolean }
    const name = String(body.name ?? 'chatgpt').toLowerCase().replace(/[^a-z0-9]/g, '').slice(0, 40) || 'chatgpt'
    const { org, user } = await accountOf(key)
    const d = await platformCall(key, 'POST', `/api/organizations/${enc(org)}/users/${enc(user)}/codex-device-authorizations`,
      { name, ...(body.reconnect ? { reconnect: true } : {}) }) as { id?: string; verificationUrl?: string; userCode?: string; expiresAt?: string; pollIntervalSeconds?: number; providerName?: string }
    if (!d?.id || !d.userCode || !d.verificationUrl) throw new KbError(502, 'The platform did not return a sign-in code.')
    return { id: d.id, url: d.verificationUrl, code: d.userCode, expiresAt: d.expiresAt ?? null, intervalSec: Math.max(2, Number(d.pollIntervalSeconds) || 5), provider: d.providerName ?? name }
  })

  app.post<{ Params: { id: string } }>('/api/access/codex/:id', async req => {
    const key = ownPlatformKey(req)
    if (!key) throw new KbError(409, 'No platform credential of your own.')
    const { org, user } = await accountOf(key)
    const d = await platformCall(key, 'POST', `/api/organizations/${enc(org)}/users/${enc(user)}/codex-device-authorizations/${enc(req.params.id)}`) as { status?: string; provider?: Provider }
    const connected = d?.status === 'connected'
    if (connected) invalidateProviderProbes()
    return {
      status: connected ? 'connected' : 'pending',
      provider: d?.provider ? { name: d.provider.name, displayName: d.provider.displayName ?? null, account: d.provider.accountEmail ?? null } : null,
    }
  })

  // GitHub Copilot, as a provider on the platform. It uses the GitHub
  // account linked to the person's ACTIVATE account, which is linked on the
  // platform (its GitHub app, or a token where the platform allows one).
  app.post('/api/access/copilot', async req => {
    const key = ownPlatformKey(req)
    if (!key) throw new KbError(409, 'Add your platform token or platform API key first; Copilot connects to your own ACTIVATE account.')
    const body = (req.body ?? {}) as { name?: string }
    const name = String(body.name ?? 'copilot').toLowerCase().replace(/[^a-z0-9-]/g, '').slice(0, 40) || 'copilot'
    const { org, user } = await accountOf(key)
    const d = await platformCall(key, 'GET', `/api/organizations/${enc(org)}/ai-integrations`) as { integrations?: Integration[] }
    const entry = (d?.integrations ?? []).find(i => i.kind === 'github-copilot' && (i.lifecycle ?? 'active') === 'active')
    if (!entry) throw new KbError(409, 'This platform does not offer GitHub Copilot.')
    const p = await platformCall(key, 'POST', `/api/organizations/${enc(org)}/users/${enc(user)}/ai-providers`,
      { name, catalogEntryId: entry.id, displayName: 'GitHub Copilot' }) as Provider
    invalidateProviderProbes()
    return { provider: { name: p?.name ?? name, displayName: p?.displayName ?? null, account: p?.accountLogin ?? null } }
  })
}

/** For tests. */
export function resetAccessRoutesForTests(): void { accounts.clear() }
