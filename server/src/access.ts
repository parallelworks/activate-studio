import { execFile, spawn, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { GATEWAY_BASE, GATEWAY_FROM_ENV, INDEX_BASE, PW_CLI, gatewayFor, pwCredentialsFile, setGatewayBase } from './config.js'
import { cliNetworkFailure, networkFailure, unreachableMessage } from './net.js'

/**
 * The pw CLI as a source of model and platform access.
 *
 * Without a key of its own, the Studio uses the credential the pw CLI holds
 * on this computer: a context in ~/.config/pw/credentials (or the file under
 * PW_CREDENTIALS_DIR, as the CLI reads it). A person can be signed in to
 * several platforms, or as several users of one, and each sign-in is a
 * context. This module lists them, remembers which one the Studio uses, and
 * hands out its credential.
 *
 * The choice is either a named context or none, which follows the CLI's own
 * current context (PW_CONTEXT, then `pw context use`). It is applied in two
 * places, so that every path agrees: the model gateway's host moves to the
 * context's platform (config.ts), and PW_CONTEXT is set in this process's
 * environment, which every pw command the Studio runs inherits.
 *
 * A context made by `pw auth login` holds a short-lived access token that the
 * CLI renews. The Studio never renews it itself: it asks the CLI with
 * `pw auth token --print --context <name>`, which renews when the token is
 * about to expire and prints the current one. Keys never leave the server.
 */

export type ContextKind = 'sign-in' | 'api-key' | 'token'

export interface CliContext {
  name: string
  server: string
  organization: string | null
  /** How the context authenticates; null when the file holds no credential
   *  for it (the CLI keeps it in the operating system's credential store). */
  kind: ContextKind | null
  /** When a platform token stops working. A sign-in renews, so it has none. */
  expiresAt: string | null
  expired: boolean
  /** The CLI's own current context. */
  current: boolean
  /** The last renewal of a sign-in failed, and why. */
  renewError: string | null
}

interface Identity {
  token?: string
  apikey?: string
  server?: string
  organization?: string
  oauth?: { expiresAt?: string; refreshToken?: string }
}
interface Store { identities?: Record<string, Identity>; currentIdentity?: string }

let storeCache: { file: string; mtimeMs: number; data: Store | null } | null = null

function readStore(): Store | null {
  const file = pwCredentialsFile()
  let mtimeMs = -1
  try { mtimeMs = fs.statSync(file).mtimeMs } catch { storeCache = { file, mtimeMs, data: null }; return null }
  if (storeCache && storeCache.file === file && storeCache.mtimeMs === mtimeMs) return storeCache.data
  let data: Store | null = null
  try { data = JSON.parse(fs.readFileSync(file, 'utf8')) as Store } catch { data = null }
  storeCache = { file, mtimeMs, data }
  return data
}

function jwtExpiry(token: string): string | null {
  const parts = token.split('.')
  if (parts.length !== 3) return null
  try {
    const payload = JSON.parse(Buffer.from(parts[1].replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8'))
    return typeof payload.exp === 'number' ? new Date(payload.exp * 1000).toISOString() : null
  } catch { return null }
}

const kindOf = (i: Identity): ContextKind | null => i.oauth ? 'sign-in' : i.apikey ? 'api-key' : i.token ? 'token' : null

/** Every context in the CLI's credential store, without any key material. */
export function listCliContexts(): CliContext[] {
  const store = readStore()
  return Object.entries(store?.identities ?? {})
    .filter(([, i]) => i && typeof i.server === 'string' && i.server)
    .map(([name, i]) => {
      const kind = kindOf(i)
      const expiresAt = kind === 'token' ? jwtExpiry(String(i.token)) : null
      return {
        name,
        server: String(i.server),
        organization: i.organization ? String(i.organization) : null,
        kind,
        expiresAt,
        expired: !!expiresAt && Date.parse(expiresAt) < Date.now(),
        current: store?.currentIdentity === name,
        renewError: renewErrors.get(name)?.message ?? null,
      }
    })
}

/* ---- the choice ---- */

const choiceFile = () => path.join(INDEX_BASE, 'access.json')
/** PW_CONTEXT as the Studio was started with it, which "follow the CLI" restores. */
const STARTUP_CONTEXT = process.env.PW_CONTEXT || null

function loadChoice(): string | null {
  try {
    const v = (JSON.parse(fs.readFileSync(choiceFile(), 'utf8')) as { pwContext?: unknown }).pwContext
    return typeof v === 'string' && v ? v : null
  } catch { return null }
}

let chosen: string | null = loadChoice()

/** The context chosen in Model access, while the CLI still has it. */
export function chosenContext(): string | null {
  return chosen && readStore()?.identities?.[chosen] ? chosen : null
}

/** The context the Studio works with: the chosen one, else the CLI's own. */
export function activeContextName(): string | null {
  return chosenContext() ?? STARTUP_CONTEXT ?? readStore()?.currentIdentity ?? null
}

function gatewayHost(): string {
  try { return new URL(GATEWAY_BASE).host } catch { return '' }
}

/** Move the gateway to the active context's platform, unless the
 *  environment fixed it. Cheap enough to run on every credential lookup:
 *  one stat of the credential file. */
export function syncGateway(): void {
  if (GATEWAY_FROM_ENV) return
  const name = activeContextName()
  const server = name ? readStore()?.identities?.[name]?.server : null
  if (server && gatewayHost() !== server) setGatewayBase(gatewayFor(server))
}

function applyEnv(): void {
  const name = chosenContext() ?? STARTUP_CONTEXT
  if (name) process.env.PW_CONTEXT = name
  else delete process.env.PW_CONTEXT
}

/** Choose a context, or null to follow the CLI's current one. */
export function chooseContext(name: string | null): void {
  if (name !== null && !readStore()?.identities?.[name]) throw new Error(`the pw CLI has no context named ${name}`)
  chosen = name
  fs.mkdirSync(INDEX_BASE, { recursive: true })
  fs.writeFileSync(choiceFile(), JSON.stringify({ pwContext: name ?? undefined }, null, 1))
  applyEnv()
  syncGateway()
  reach = null
}

applyEnv()
syncGateway()

/* ---- credentials ---- */

/** The context whose credential the gateway gets: the active one when it is
 *  for the gateway's host, else the first context for that host (a gateway
 *  set in the environment may name a host the active context is not on). */
export function credentialContextName(): string | null {
  syncGateway()
  const ids = readStore()?.identities
  if (!ids) return null
  const host = gatewayHost()
  const active = activeContextName()
  if (active && ids[active]?.server === host) return active
  for (const [name, i] of Object.entries(ids)) if (i?.server === host) return name
  return null
}

/** Tokens the CLI printed, newer than the file's when the CLI keeps its
 *  renewals elsewhere. */
const renewed = new Map<string, { token: string; until: number }>()
const renewing = new Map<string, Promise<string | null>>()
const renewErrors = new Map<string, { at: number; message: string; offline: boolean }>()
const RENEW_AHEAD_MS = 2 * 60_000

function fileExpiry(i: Identity): number {
  const t = Date.parse(i.oauth?.expiresAt ?? '')
  return Number.isFinite(t) ? t : NaN
}

/** Whether a context's credential has to come from the CLI right now. */
function needsRenewal(name: string): boolean {
  const i = readStore()?.identities?.[name]
  if (!i) return false
  const r = renewed.get(name)
  if (r && r.until - Date.now() > RENEW_AHEAD_MS) return false
  if (!i.token && !i.apikey) return true
  if (!i.oauth) return false
  const exp = fileExpiry(i)
  return Number.isFinite(exp) && exp - Date.now() < RENEW_AHEAD_MS
}

/** The env for the CLI asking for a token: a PW_API_KEY in the server's
 *  environment would otherwise be printed back instead. */
function tokenEnv(): NodeJS.ProcessEnv {
  const env = { ...process.env }
  delete env.PW_API_KEY
  return env
}

/** Ask the CLI for a context's current token, renewing it if it is about to
 *  expire. Concurrent askers share one CLI run. */
export function renewContext(name: string, timeoutMs = 20_000): Promise<string | null> {
  const inFlight = renewing.get(name)
  if (inFlight) return inFlight
  const p = new Promise<string | null>(resolve => {
    execFile(PW_CLI, ['auth', 'token', '--print', '-o', 'json', '--context', name], { timeout: timeoutMs, env: tokenEnv() }, (err, stdout, stderr) => {
      const text = String(stdout ?? '')
      const start = text.indexOf('{')
      const end = text.lastIndexOf('}')
      let token = ''
      let expiresIn: number | null = null
      if (!err && start >= 0 && end > start) {
        try {
          const d = JSON.parse(text.slice(start, end + 1)) as { access_token?: string; expires_in?: number }
          token = String(d.access_token ?? '').trim()
          expiresIn = typeof d.expires_in === 'number' ? d.expires_in : null
        } catch { token = '' }
      }
      if (token) {
        renewed.set(name, { token, until: Date.now() + (expiresIn !== null ? expiresIn * 1000 : 10 * 60_000) })
        renewErrors.delete(name)
        resolve(token)
        return
      }
      const said = String(stderr || (err as Error | null)?.message || 'no token printed').replace(/\x1b\[[0-9;]*m/g, '').trim()
      const offline = (err as NodeJS.ErrnoException | null)?.code !== 'ENOENT' && cliNetworkFailure(said)
      renewErrors.set(name, {
        at: Date.now(),
        offline,
        message: (err as NodeJS.ErrnoException | null)?.code === 'ENOENT'
          ? 'The pw CLI is not installed here, so this sign-in cannot be renewed.'
          : offline
            ? unreachableMessage(readStore()?.identities?.[name]?.server ?? '', null)
            : said.split('\n').filter(Boolean).slice(-1)[0]?.slice(0, 300) ?? 'the CLI could not renew this sign-in',
      })
      resolve(null)
    })
  }).finally(() => renewing.delete(name))
  renewing.set(name, p)
  return p
}

/** A context's credential, at once. A sign-in about to expire is renewed in
 *  the background and its current token returned meanwhile; callers that can
 *  wait use ensureFreshAccess first. */
export function contextToken(name: string): string {
  const i = readStore()?.identities?.[name]
  if (!i) return ''
  const fileToken = i.token || i.apikey || ''
  const r = renewed.get(name)
  const fileExp = fileExpiry(i)
  const useRenewed = !!r && (!fileToken || !Number.isFinite(fileExp) || r.until > fileExp)
  if (needsRenewal(name)) void renewContext(name)
  return (useRenewed ? r!.token : fileToken) || r?.token || ''
}

/** The pw CLI's credential for the gateway's host, or ''. */
export function cliCredential(): string {
  const name = credentialContextName()
  return name ? contextToken(name) : ''
}

/** Renew the credential in use when it is about to expire, waiting at most
 *  timeoutMs. Called before a request that would otherwise go out with a
 *  token minutes from expiry. Offline, the renewal fails fast and the
 *  current token is used as it is. */
export async function ensureFreshAccess(timeoutMs = 15_000): Promise<void> {
  const name = credentialContextName()
  if (!name || !needsRenewal(name)) return
  // A renewal that failed for lack of network is not retried on every request.
  const err = renewErrors.get(name)
  if (err?.offline && Date.now() - err.at < 30_000) return
  await Promise.race([renewContext(name), new Promise(r => setTimeout(r, timeoutMs).unref?.())])
}

/* ---- the CLI itself ---- */

let cliInfoCache: { at: number; v: { available: boolean; version: string | null } } | null = null

/** Whether the pw CLI runs here, and its version. */
export function cliInfo(): Promise<{ available: boolean; version: string | null }> {
  if (cliInfoCache && Date.now() - cliInfoCache.at < 5 * 60_000) return Promise.resolve(cliInfoCache.v)
  return new Promise(resolve => {
    execFile(PW_CLI, ['--version'], { timeout: 8_000, env: tokenEnv() }, (err, stdout) => {
      const missing = (err as NodeJS.ErrnoException | null)?.code === 'ENOENT'
      const version = /v?\d+\.\d+\.\d+/.exec(String(stdout ?? ''))?.[0] ?? null
      const v = { available: !missing && (!err || !!version), version }
      cliInfoCache = { at: Date.now(), v }
      resolve(v)
    })
  })
}

/* ---- signing in with the CLI ---- */

export interface LoginState {
  host: string
  state: 'starting' | 'waiting' | 'done' | 'failed' | 'canceled'
  /** Where to enter the code, and the code. */
  url: string | null
  code: string | null
  /** The context the sign-in made, once done. */
  context: string | null
  message: string | null
  startedAt: string
}

let login: { st: LoginState; child: ChildProcess | null; timer: NodeJS.Timeout | null } | null = null

/** A platform host as typed: a name, an address, or a URL to take one from. */
export function normalizeHost(input: string): string | null {
  const h = String(input ?? '').trim().replace(/^https?:\/\//i, '').replace(/\/.*$/, '').toLowerCase()
  return /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:\d{1,5})?$/.test(h) ? h : null
}

/** The address and code the CLI prints for a device sign-in. */
export function parseDevicePrompt(text: string): { url: string | null; code: string | null } {
  const clean = text.replace(/\x1b\[[0-9;]*m/g, '')
  const url = /(https?:\/\/[^\s"'<>]+)/.exec(clean)?.[1] ?? null
  const code = /^\s*([A-Z0-9]{3,}(?:-[A-Z0-9]{3,})+)\s*$/m.exec(clean)?.[1] ?? null
  return { url, code }
}

export function loginState(): LoginState | null { return login?.st ?? null }

export function cancelCliLogin(): LoginState | null {
  if (!login) return null
  if (login.timer) clearTimeout(login.timer)
  if (login.child && login.child.exitCode === null) login.child.kill()
  if (login.st.state === 'starting' || login.st.state === 'waiting') login.st = { ...login.st, state: 'canceled', message: 'Sign-in canceled.' }
  login.child = null
  return login.st
}

/**
 * Sign the CLI in to a platform with its device flow, `pw auth login
 * --device <host>`: the CLI prints an address and a code, the person
 * approves on any device, and the CLI saves the sign-in as a context and
 * makes it its current one. The Studio relays the address and code and,
 * once the CLI reports success, uses the new context.
 *
 * The device flow is the CLI's own (the platform registers it for the CLI,
 * with the CLI's client), so the Studio runs the CLI rather than speaking
 * the protocol itself.
 */
export function startCliLogin(hostInput: string, opts: { waitMs?: number; expireMs?: number } = {}): Promise<LoginState> {
  const host = normalizeHost(hostInput)
  if (!host) return Promise.reject(new Error('Enter a platform host, such as platform.example.com.'))
  cancelCliLogin()
  const st: LoginState = { host, state: 'starting', url: null, code: null, context: null, message: null, startedAt: new Date().toISOString() }
  let child: ChildProcess
  try {
    child = spawn(PW_CLI, ['auth', 'login', '--device', host], { env: tokenEnv(), stdio: ['ignore', 'pipe', 'pipe'] })
  } catch (e) {
    return Promise.reject(e)
  }
  const mine = { st, child: child as ChildProcess | null, timer: null as NodeJS.Timeout | null }
  login = mine
  let out = ''
  return new Promise<LoginState>(resolve => {
    let answered = false
    const answer = () => { if (!answered) { answered = true; resolve(mine.st) } }
    const onData = (b: Buffer) => {
      out += b.toString('utf8')
      if (mine.st.state !== 'starting') return
      const p = parseDevicePrompt(out)
      if (p.url && p.code) {
        mine.st = { ...mine.st, state: 'waiting', url: p.url, code: p.code }
        answer()
      }
    }
    child.stdout?.on('data', onData)
    child.stderr?.on('data', onData)
    child.on('error', e => {
      const missing = (e as NodeJS.ErrnoException).code === 'ENOENT'
      mine.st = { ...mine.st, state: 'failed', message: missing ? 'The pw CLI is not installed here. Install it, or add a key or endpoint instead.' : String(e.message) }
      mine.child = null
      answer()
    })
    child.on('close', code => {
      mine.child = null
      if (mine.timer) clearTimeout(mine.timer)
      if (mine.st.state === 'canceled') return answer()
      if (code === 0) {
        const store = readStore()
        const ids = store?.identities ?? {}
        const made = store?.currentIdentity && ids[store.currentIdentity]?.server === host
          ? store.currentIdentity
          : Object.keys(ids).find(n => ids[n]?.server === host) ?? null
        // The CLI made the sign-in its current context. Following the CLI
        // already picks it up; a Studio pinned to a context (or started with
        // PW_CONTEXT) is moved to it, since signing in here asks for it.
        if (made && (chosenContext() !== null || STARTUP_CONTEXT)) {
          try { chooseContext(made) } catch { /* listed below either way */ }
        } else {
          syncGateway()
        }
        mine.st = { ...mine.st, state: 'done', context: made, message: made ? `Signed in. The Studio now uses the pw CLI context ${made}.` : 'Signed in.' }
      } else {
        const said = out.replace(/\x1b\[[0-9;]*m/g, '').trim().split('\n').filter(l => l.trim() && !/^waiting/i.test(l.trim())).slice(-1)[0] ?? ''
        mine.st = {
          ...mine.st, state: 'failed',
          message: cliNetworkFailure(out) ? unreachableMessage(host, null) : (said.slice(0, 300) || `The sign-in did not finish (the CLI exited with ${code}).`),
        }
      }
      answer()
    })
    // The CLI prints the code within a second or two; one that has not after
    // waitMs is reported as it stands rather than holding the request.
    setTimeout(answer, opts.waitMs ?? 20_000).unref?.()
    // Device codes expire after ten minutes.
    mine.timer = setTimeout(() => {
      if (mine.child && mine.child.exitCode === null) mine.child.kill()
      if (mine.st.state === 'waiting' || mine.st.state === 'starting') mine.st = { ...mine.st, state: 'failed', message: 'The code expired before it was approved. Start again.' }
    }, opts.expireMs ?? 11 * 60_000)
    mine.timer.unref?.()
  })
}

/* ---- reaching the platform ---- */

let reach: { origin: string; at: number; ok: boolean; message: string | null } | null = null
let reaching: Promise<{ ok: boolean; message: string | null }> | null = null

/**
 * Whether the platform answers at all, asked before a pw command that would
 * otherwise wait out its own long timeout on a dead network. Any HTTP answer
 * counts; only a network failure (no route, no name, reset, no answer in
 * four seconds) counts against it. Kept for 30 seconds, 10 when down.
 */
export function platformReachability(): Promise<{ ok: boolean; message: string | null }> {
  let origin: string
  let host: string
  try { const u = new URL(GATEWAY_BASE); origin = u.origin; host = u.host } catch { return Promise.resolve({ ok: true, message: null }) }
  if (reach && reach.origin === origin && Date.now() - reach.at < (reach.ok ? 30_000 : 10_000)) return Promise.resolve(reach)
  if (reaching) return reaching
  reaching = (async () => {
    try {
      await fetch(origin, { method: 'HEAD', redirect: 'manual', signal: AbortSignal.timeout(4_000) })
      reach = { origin, at: Date.now(), ok: true, message: null }
    } catch (e) {
      const f = networkFailure(e)
      reach = { origin, at: Date.now(), ok: !f, message: f ? unreachableMessage(host, f) : null }
    }
    return reach
  })().finally(() => { reaching = null })
  return reaching
}

/** For tests: forget cached state and read the choice and environment again. */
export function resetAccessForTests(): void {
  storeCache = null
  renewed.clear(); renewErrors.clear(); renewing.clear()
  cliInfoCache = null
  reach = null
  cancelCliLogin()
  login = null
  chosen = loadChoice()
  applyEnv()
  syncGateway()
}
