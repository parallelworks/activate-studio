import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { INDEX_BASE } from './config.js'

/**
 * Per-user model credentials, keyed by the platform-verified JWT subject.
 *
 * Trust model, stated plainly: a key pasted into this app is available to
 * the server process and therefore to the deployment operator. Encryption
 * at rest protects the vault file against casual reads and backups, not
 * against whoever runs the server. The platform's delegated-credential
 * flow is the eventual replacement; this is the bridge.
 *
 * Storage discipline:
 *  - vault and its encryption secret live beside the index (never in the
 *    repository; the index directory is gitignored), both written 0600.
 *  - key material never leaves the server: status APIs expose only the
 *    last four characters and timestamps.
 *  - keys are never logged and never included in error messages.
 *  - "session only" keys are held in memory with a TTL and are never
 *    written to disk at all.
 */

/** Env-only kill switch (deliberately not a runtime setting: a security
 *  posture should not be flippable from the UI). With it set, this module
 *  refuses to store or resolve personal keys at all. */
export function personalKeysDisabled(): boolean {
  return process.env.DISABLE_PERSONAL_KEYS === '1'
}

const VAULT_FILE = path.join(INDEX_BASE, 'user-credentials.json')
const SECRET_FILE = path.join(INDEX_BASE, '.credentials-secret')
const SESSION_TTL_MS = 12 * 3600 * 1000

interface VaultEntry { blob: string; last4: string; addedAt: string; kind?: 'api-key' | 'token'; expiresAt?: string | null; baseUrl?: string | null; sharedBy?: string; sharedBySub?: string }

/** Reserved vault subject holding a key a user has promoted for everyone.
 *  A platform subject is a uuid, so it cannot collide, and setUserKey
 *  refuses it outright. */
const SHARED_SUB = '__shared__'

/** A JWT-shaped credential is a short-term token; surface its expiry so
 *  the UI can say when it dies instead of failing mysteriously. */
function inspect(key: string): { kind: 'api-key' | 'token'; expiresAt: string | null } {
  const parts = key.split('.')
  if (parts.length === 3) {
    try {
      const payload = JSON.parse(Buffer.from(parts[1].replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8'))
      const exp = typeof payload.exp === 'number' ? new Date(payload.exp * 1000).toISOString() : null
      return { kind: 'token', expiresAt: exp }
    } catch { /* not a JWT after all */ }
  }
  return { kind: 'api-key', expiresAt: null }
}

let secretCache: Buffer | null = null

function secret(): Buffer {
  if (secretCache) return secretCache
  try {
    const raw = fs.readFileSync(SECRET_FILE)
    if (raw.length === 32) { secretCache = raw; return raw }
  } catch { /* generate below */ }
  const fresh = crypto.randomBytes(32)
  fs.writeFileSync(SECRET_FILE, fresh, { mode: 0o600 })
  secretCache = fresh
  return fresh
}

function encrypt(plain: string): string {
  const iv = crypto.randomBytes(12)
  const cipher = crypto.createCipheriv('aes-256-gcm', secret(), iv)
  const data = Buffer.concat([cipher.update(plain, 'utf8'), cipher.final()])
  return `v1:${iv.toString('base64')}:${cipher.getAuthTag().toString('base64')}:${data.toString('base64')}`
}

function decrypt(blob: string): string | null {
  try {
    const [v, iv, tag, data] = blob.split(':')
    if (v !== 'v1') return null
    const decipher = crypto.createDecipheriv('aes-256-gcm', secret(), Buffer.from(iv, 'base64'))
    decipher.setAuthTag(Buffer.from(tag, 'base64'))
    return Buffer.concat([decipher.update(Buffer.from(data, 'base64')), decipher.final()]).toString('utf8')
  } catch {
    return null
  }
}

function loadVault(): Record<string, VaultEntry> {
  try { return JSON.parse(fs.readFileSync(VAULT_FILE, 'utf8')) } catch { return {} }
}

function saveVault(v: Record<string, VaultEntry>): void {
  fs.writeFileSync(VAULT_FILE, JSON.stringify(v, null, 1), { mode: 0o600 })
}

const sessionKeys = new Map<string, { key: string; expiresAt: number; addedAt: string; last4: string; kind: 'api-key' | 'token'; credExpiresAt: string | null; baseUrl: string | null }>()

function liveSession(sub: string) {
  const s = sessionKeys.get(sub)
  if (!s) return null
  if (Date.now() > s.expiresAt) { sessionKeys.delete(sub); return null }
  return s
}

export function setUserKey(sub: string, key: string, persist: boolean, baseUrl?: string | null): void {
  if (personalKeysDisabled()) throw new Error('personal keys are disabled on this deployment')
  if (sub === SHARED_SUB) throw new Error('reserved subject')
  const trimmed = key.trim()
  if (!trimmed || trimmed.length > 8192) throw new Error('invalid key')
  let url: string | null = String(baseUrl ?? '').trim().replace(/\/$/, '') || null
  if (url) {
    try { new URL(url) } catch { throw new Error('invalid provider base URL') }
    if (!/^https?:\/\//.test(url)) throw new Error('provider base URL must be http(s)')
  }
  const last4 = trimmed.slice(-4)
  const addedAt = new Date().toISOString()
  const { kind, expiresAt } = inspect(trimmed)
  // Personal keys live in memory only; the browser's sealed cookie is the
  // durable copy (persist governs whether the route sets it). The vault
  // file now holds nothing but the deployment shared key, and any personal
  // entry from before this change is scrubbed.
  void persist
  sessionKeys.set(sub, { key: trimmed, expiresAt: Date.now() + SESSION_TTL_MS, addedAt, last4, kind, credExpiresAt: expiresAt, baseUrl: url })
  const vault = loadVault()
  if (vault[sub]) { delete vault[sub]; saveVault(vault) }
}

/** One-time scrub: personal entries predating browser-held keys leave the
 *  vault so nothing personal stays at rest. The shared key remains. */
export function scrubPersonalVaultEntries(): number {
  const vault = loadVault()
  const doomed = Object.keys(vault).filter(k => k !== SHARED_SUB)
  if (!doomed.length) return 0
  for (const k of doomed) delete vault[k]
  saveVault(vault)
  return doomed.length
}

export function clearUserKey(sub: string): void {
  sessionKeys.delete(sub)
  const vault = loadVault()
  if (vault[sub]) { delete vault[sub]; saveVault(vault) }
}

/**
 * A key one user promotes for everyone else on the deployment.
 *
 * The case this answers: a deployment with no credential of its own, where
 * one person has a key and wants the app usable by the group without each
 * of them pasting one. It stands in for the deployment credential, so every
 * request that would otherwise have none runs as the sharer, and their
 * usage and quota carry the whole deployment. The UI says so plainly.
 *
 * A key bound to a custom provider is refused: the deployment-credential
 * paths call the configured gateway, so such a key would work in some
 * places and not others.
 */
export function shareUserKey(sub: string, sharedBy: string): void {
  const cred = resolveUserCred(sub)
  if (!cred) throw new Error('add your own key first, then share it')
  if (cred.baseUrl) throw new Error('a key bound to a custom provider cannot be shared; the deployment credential always calls this deployment\'s gateway')
  const status = getUserKeyStatus(sub)
  const vault = loadVault()
  vault[SHARED_SUB] = {
    blob: encrypt(cred.key),
    last4: status.last4 ?? cred.key.slice(-4),
    addedAt: new Date().toISOString(),
    kind: status.kind ?? 'api-key',
    expiresAt: status.credExpiresAt ?? null,
    baseUrl: null,
    sharedBy,
    sharedBySub: sub,
  }
  saveVault(vault)
}

export function clearSharedKey(): void {
  const vault = loadVault()
  if (vault[SHARED_SUB]) { delete vault[SHARED_SUB]; saveVault(vault) }
}

export interface SharedKeyStatus {
  active: boolean
  last4?: string
  sharedBy?: string
  sharedAt?: string
  kind?: 'api-key' | 'token'
  credExpiresAt?: string | null
  credExpired?: boolean
  /** True when the promoted key is the caller's own. */
  mine?: boolean
}

export function getSharedKeyStatus(sub?: string | null): SharedKeyStatus {
  const entry = loadVault()[SHARED_SUB]
  if (!entry) return { active: false }
  return {
    active: true,
    last4: entry.last4,
    sharedBy: entry.sharedBy,
    sharedAt: entry.addedAt,
    kind: entry.kind ?? 'api-key',
    credExpiresAt: entry.expiresAt ?? null,
    credExpired: expired(entry.expiresAt),
    mine: !!sub && entry.sharedBySub === sub,
  }
}

/** The promoted key, if one is active and still valid. */
export function resolveSharedKey(): string | null {
  const entry = loadVault()[SHARED_SUB]
  if (!entry || expired(entry.expiresAt)) return null
  return decrypt(entry.blob)
}

export interface UserKeyStatus {
  mode: 'stored' | 'session' | 'none'
  last4?: string
  addedAt?: string
  sessionExpiresAt?: string
  kind?: 'api-key' | 'token'
  credExpiresAt?: string | null
  credExpired?: boolean
  baseUrl?: string | null
}

function expired(iso: string | null | undefined): boolean {
  return !!iso && Date.parse(iso) < Date.now()
}

export function getUserKeyStatus(sub: string): UserKeyStatus {
  const s = liveSession(sub)
  if (s) {
    return {
      mode: 'session', last4: s.last4, addedAt: s.addedAt,
      sessionExpiresAt: new Date(s.expiresAt).toISOString(),
      kind: s.kind, credExpiresAt: s.credExpiresAt, credExpired: expired(s.credExpiresAt),
      baseUrl: s.baseUrl,
    }
  }
  return { mode: 'none' }
}

/** The caller's own key when they have one; null means use the
 *  deployment credential (current shared-session default). */
export interface UserCred { key: string; baseUrl: string | null }

export function resolveUserCred(sub: string | null | undefined): UserCred | null {
  if (!sub || sub === SHARED_SUB || personalKeysDisabled()) return null
  const s = liveSession(sub)
  if (s) return expired(s.credExpiresAt) ? null : { key: s.key, baseUrl: s.baseUrl }
  return null
}

export function resolveUserKey(sub: string | null | undefined): string | null {
  return resolveUserCred(sub)?.key ?? null
}

/**
 * Browser-held personal keys. The sealed value is the key and provider URL
 * encrypted with the server secret, split across numbered cookies to stay
 * under the per-cookie size limit. The browser is the only place the key
 * rests; the server holds it in memory with a TTL and rehydrates from the
 * cookie on request, so nothing personal is written to disk and the raw
 * key never appears in a header.
 */
export const KEY_COOKIE = 'ade_mk'
/** The CHIPS-partitioned twin of KEY_COOKIE. The two are written together
 *  and either one is enough; see keyCookieJar for why both exist. */
export const KEY_COOKIE_P = 'ade_mkp'
const COOKIE_CHUNK = 3800

export function sealKeyCookie(sub: string): string[] | null {
  const s = liveSession(sub)
  if (!s) return null
  const blob = encrypt(JSON.stringify({ k: s.key, u: s.baseUrl ?? null }))
  const chunks: string[] = []
  for (let i = 0; i < blob.length; i += COOKIE_CHUNK) chunks.push(blob.slice(i, i + COOKIE_CHUNK))
  return chunks
}

export function hydrateFromCookie(sub: string, cookieHeader: string | undefined): void {
  if (!sub || !cookieHeader || personalKeysDisabled()) return
  if (liveSession(sub)) return
  const jar = new Map<string, string>()
  for (const part of cookieHeader.split(';')) {
    const eq = part.indexOf('=')
    if (eq > 0) jar.set(part.slice(0, eq).trim(), part.slice(eq + 1).trim())
  }
  const assemble = (name: string): string => {
    let blob = ''
    for (let i = 0; ; i++) {
      const c = jar.get(`${name}${i}`)
      if (!c) break
      blob += c
    }
    return blob
  }
  // Whichever form the browser offered. Only one arrives in most contexts,
  // and when both do they carry the same sealed value.
  for (const blob of [assemble(KEY_COOKIE), assemble(KEY_COOKIE_P)]) {
    if (!blob) continue
    try {
      const opened = decrypt(blob)
      if (!opened) continue
      const { k, u } = JSON.parse(opened) as { k?: string; u?: string | null }
      if (typeof k === 'string' && k.trim()) { setUserKey(sub, k, false, u ?? null); return }
    } catch { /* stale cookie sealed under a rotated secret: try the other */ }
  }
}
