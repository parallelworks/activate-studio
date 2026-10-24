import crypto from 'node:crypto'
import { execFile, execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { promisify } from 'node:util'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { parseNotes, readChangelog, type ChangeEntry, type ChangeVersion } from './changelog.js'
import { INDEX_BASE } from './config.js'
import { KbError } from './kb.js'
import { RELAUNCH_EXIT_CODE, requestRestart, runsWithoutAgent } from './lifecycle.js'
import { effectiveSettings, type UpdatePolicy } from './settings.js'
import { connectionProblem, networkFailure } from './net.js'
import { buildInfo } from './version.js'

/**
 * Updates from the project's GitHub releases. The macOS app installs them:
 * a release carries the app archive and a manifest (version, asset name,
 * size, SHA-256) signed with the release key, whose public half the app
 * carries. A verified archive is unpacked beside the app, swapped in with
 * the old copy kept as `.<name>.previous`, and the LaunchAgent restarted so
 * the run loop (studio-run.sh) starts the new copy. pending.json in the
 * updates folder marks the first starts of a new version: the run loop puts
 * the previous copy back if the new one keeps stopping, and the server
 * clears the mark once it has been up for a minute. A clone is told the
 * version and how to update; a hosted Studio never checks.
 */

const REPO = 'parallelworks/activate-studio'
const ASSET = 'activate-studio-macos-arm64.tar.gz'
const MANIFEST = 'activate-studio-macos-arm64.manifest.json'
const SIGNATURE = `${MANIFEST}.sig`
const APP_NAME = 'ACTIVATE Studio.app'
const BUNDLE_ID = 'com.parallelworks.activate-studio'
const LABEL = 'com.parallelworks.activate-studio'
const REQUEST_TIMEOUT_MS = 15_000
/** LaunchServices' own tool, which looks at an app bundle again. */
const LSREGISTER = '/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister'
/** How a clone of the repository updates itself, before a restart. */
const CLONE_COMMAND = 'git pull\npnpm install\npnpm build'

const execFileP = promisify(execFile)

export type UpdateMode = 'app' | 'clone' | 'hosted'
export type UpdateState = 'idle' | 'checking' | 'downloading' | 'verifying' | 'installing' | 'restarting' | 'error'

export interface UpdateStatus {
  mode: UpdateMode
  current: { version: string; builtAt: string | null }
  policy: UpdatePolicy
  latest: { version: string; publishedAt: string | null; notes: string; url: string } | null
  /** The releases newer than the running version, up to and including
   *  `latest`, newest first, each with its notes as entries. */
  releases: UpdateRelease[]
  available: boolean
  canInstall: boolean
  /** Why it cannot install, in plain words. */
  reason: string | null
  /** The commands that update a clone of the repository; null otherwise. */
  command: string | null
  state: UpdateState
  progress: { bytes: number; total: number } | null
  error: string | null
  checkedAt: string | null
  /** The last check could not reach the release server (no network, most
   *  likely). Not an error: the next check comes sooner, and nothing else
   *  depends on it. */
  offline: boolean
  /** A rollback is available. */
  previous: { version: string } | null
  /** The last update changed the app's own executable: the window has to
   *  be reopened to run it (the launcher clears this when the app opens). */
  reopen: boolean
}

export interface UpdateRelease {
  version: string
  publishedAt: string | null
  url: string
  entries: ChangeEntry[]
}

interface Release {
  tag: string
  prerelease: boolean
  publishedAt: string | null
  notes: string
  url: string
  assets: Record<string, { url: string; size: number }>
}

interface Saved { etag: string | null; releases: Release[]; checkedAt: string | null }

interface Manifest { version: string; asset: string; sha256: string; size: number; minMacos?: string; builtAt?: string }

interface Prepared { version: string; from: string; shellChanged: boolean }

/** A check the update failed, as opposed to a network or disk failure. */
class Refused extends Error {}

/** intervalMs and soonMs are measured by the clock between checks; pollMs
 *  is how often the server looks whether one is due. A Mac's timers stop
 *  while it sleeps, so a check is never left to a single long timer. Four
 *  checks an hour at most, most answered 304 against the stored ETag, is
 *  well inside GitHub's unauthenticated limit of 60 an hour per address. */
const TIMINGS = { firstCheckMs: 60_000, intervalMs: 15 * 60_000, soonMs: 5 * 60_000, pollMs: 2 * 60_000, idleMs: 10 * 60_000, idlePollMs: 30_000, healthyMs: 60_000 }
const timings = { ...TIMINGS }
const st: { state: UpdateState; error: string | null; progress: UpdateStatus['progress']; offline: boolean } = { state: 'idle', error: null, progress: null, offline: false }
let saved: Saved | null = null
let prepared: Prepared | null = null
let op: Promise<void> | null = null
/** A version that failed verification here, which auto install skips. */
let refusedVersion: string | null = null
/** What the run loop's rollback did, reported once after it. */
let rollbackNote: string | null = null
/** Update now was asked for while an automatic download was under way. */
let installWanted = false
let versionOverride: string | null = null
let busyCheck: () => boolean = () => false
let log: (msg: string) => void = () => {}
let checkTimer: NodeJS.Timeout | null = null
/** When the last check started, by the clock; a failed check counts, so a
 *  refusal is not retried at every poll. */
let lastAttemptAt = 0
/** startUpdates has run: the window asking for the status may start a due check. */
let scheduled = false
/** Bumped by a reset, so a first check still under way does not start the poll after it. */
let scheduleGen = 0
let idleTimer: NodeJS.Timeout | null = null
let healthyTimer: NodeJS.Timeout | null = null

// ---- versions ----

/** vMAJOR.MINOR[.PATCH]; a git describe or other suffix is ignored, so
 *  v1.137-6-g3140181 is v1.137. */
export function parseVersion(v: string): [number, number, number] | null {
  const m = /^v?(\d+)\.(\d+)(?:\.(\d+))?/.exec(String(v ?? '').trim())
  return m ? [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)] : null
}

export function compareVersions(a: string, b: string): number {
  const x = parseVersion(a) ?? [0, 0, 0]
  const y = parseVersion(b) ?? [0, 0, 0]
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] - y[i]
  return 0
}

/** Whether candidate is a release newer than running. A running build of
 *  no known version (dev) counts as older than any release. */
export function isNewer(candidate: string, running: string): boolean {
  return parseVersion(candidate) !== null && compareVersions(candidate, running) > 0
}

const sameVersion = (a: string, b: string) => parseVersion(a) !== null && compareVersions(a, b) === 0

function dottedAtMost(need: string, have: string): boolean {
  const a = need.split('.').map(Number)
  const b = have.split('.').map(Number)
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i] || 0, y = b[i] || 0
    if (x !== y) return x < y
  }
  return true
}

// ---- the app and its files ----

function running(): { version: string; builtAt: string | null } {
  if (versionOverride) return { version: versionOverride, builtAt: null }
  const b = buildInfo()
  return { version: b.version, builtAt: b.builtAt }
}

const hosted = () => authEnabled() || !!process.env.PW_PLATFORM_HOST

function appBundle(): string | null {
  const b = process.env.STUDIO_APP_BUNDLE
  return b ? path.resolve(b) : null
}

function bundleWritable(): boolean {
  const b = appBundle()
  if (!b) return false
  try {
    fs.accessSync(path.dirname(b), fs.constants.W_OK)
    return fs.statSync(b).isDirectory()
  } catch { return false }
}

export function updateMode(): UpdateMode {
  if (hosted()) return 'hosted'
  return bundleWritable() ? 'app' : 'clone'
}

/** `.ACTIVATE Studio.app.<suffix>` beside the app. */
function beside(suffix: 'new' | 'previous' | 'unpack' | 'failed'): string {
  const b = appBundle()!
  return path.join(path.dirname(b), `.${path.basename(b)}.${suffix}`)
}

function updatesDir(): string {
  const data = process.env.STUDIO_DATA_DIR || (process.env.STUDIO_CONFIG_FILE ? path.dirname(process.env.STUDIO_CONFIG_FILE) : '')
  return data ? path.join(data, 'updates') : path.join(INDEX_BASE, 'updates')
}

const inUpdates = (name: string) => path.join(updatesDir(), name)

function publicKey(): crypto.KeyObject | null {
  const b = appBundle()
  if (!b) return null
  try {
    const key = crypto.createPublicKey(fs.readFileSync(path.join(b, 'Contents', 'Resources', 'update-signing.pub')))
    return key.asymmetricKeyType === 'ed25519' ? key : null
  } catch { return null }
}

function versionOf(bundle: string): string | null {
  try {
    const v = JSON.parse(fs.readFileSync(path.join(bundle, 'Contents', 'Resources', 'app', 'build-info.json'), 'utf8')).version
    return v ? String(v) : null
  } catch { return null }
}

function readJson<T>(file: string): T | null {
  try { return JSON.parse(fs.readFileSync(file, 'utf8')) as T } catch { return null }
}

async function writeJson(file: string, value: unknown): Promise<void> {
  await fsp.mkdir(path.dirname(file), { recursive: true })
  const tmp = `${file}.${process.pid}.tmp`
  await fsp.writeFile(tmp, JSON.stringify(value, null, 1) + '\n')
  await fsp.rename(tmp, file)
}

let macos: string | null | undefined
function macosVersion(): string | null {
  if (process.platform !== 'darwin') return null
  if (macos === undefined) {
    try { macos = execFileSync('sw_vers', ['-productVersion'], { encoding: 'utf8', timeout: 5000 }).trim() || null } catch { macos = null }
  }
  return macos
}

// ---- GitHub ----

const userAgent = () => `activate-studio/${running().version}`

/** The host the check asks: GitHub's API, or STUDIO_UPDATE_URL's. */
function releaseHost(): string {
  try { return new URL(process.env.STUDIO_UPDATE_URL || 'https://api.github.com').host } catch { return 'api.github.com' }
}

function plain(e: unknown): string {
  const err = e as Error & { cause?: { code?: string; message?: string } }
  if (err?.name === 'TimeoutError') return 'no answer within 15 seconds'
  if (err?.message === 'terminated') return `the connection closed early (${err.cause?.message ?? 'no reason given'})`
  return String(err?.cause?.message ?? err?.message ?? e).slice(0, 300)
}

function loadSaved(): Saved {
  if (!saved) saved = readJson<Saved>(inUpdates('state.json')) ?? { etag: null, releases: [], checkedAt: null }
  if (!Array.isArray(saved.releases)) saved.releases = []
  return saved
}

function toRelease(r: Record<string, unknown>): Release {
  const assets: Release['assets'] = {}
  for (const a of Array.isArray(r.assets) ? r.assets as Record<string, unknown>[] : []) {
    if (typeof a?.name === 'string' && typeof a.browser_download_url === 'string') {
      assets[a.name] = { url: a.browser_download_url, size: Number(a.size) || 0 }
    }
  }
  return {
    tag: String(r.tag_name ?? ''),
    prerelease: !!r.prerelease,
    publishedAt: r.published_at ? String(r.published_at) : null,
    notes: String(r.body ?? '').slice(0, 20_000),
    url: String(r.html_url ?? ''),
    assets,
  }
}

async function fetchReleases(): Promise<void> {
  const s = loadSaved()
  const base = (process.env.STUDIO_UPDATE_URL || 'https://api.github.com').replace(/\/+$/, '')
  const headers: Record<string, string> = {
    'User-Agent': userAgent(),
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
  }
  if (s.etag) headers['If-None-Match'] = s.etag
  const res = await fetch(`${base}/repos/${REPO}/releases?per_page=30`, { headers, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) })
  const now = new Date().toISOString()
  if (res.status === 304) {
    saved = { ...s, checkedAt: now }
  } else if (res.ok) {
    const list = await res.json() as unknown
    if (!Array.isArray(list)) throw new Error('GitHub did not answer with a list of releases')
    const releases = (list as Record<string, unknown>[]).filter(r => r && !r.draft).map(toRelease).filter(r => parseVersion(r.tag))
    saved = { etag: res.headers.get('etag'), releases, checkedAt: now }
  } else if (res.status === 403 || res.status === 429) {
    throw new Error(`GitHub refused the request (HTTP ${res.status}, usually its hourly limit); it is tried again later`)
  } else {
    throw new Error(`GitHub answered HTTP ${res.status}`)
  }
  await writeJson(inUpdates('state.json'), saved)
}

/** Whether a release carries the signed macOS app the app installs from. */
const hasApp = (r: Release) => !!(r.assets[ASSET] && r.assets[MANIFEST] && r.assets[SIGNATURE])

/** The newest release the channel allows. For the app, the newest that
 *  carries the signed app: a release is published before its macOS build
 *  attaches the app, and one whose build failed never gets it. */
function latestRelease(channel: UpdatePolicy['channel'], needsApp = updateMode() === 'app'): Release | null {
  let best: Release | null = null
  for (const r of loadSaved().releases) {
    if (r.prerelease && channel !== 'prerelease') continue
    if (needsApp && !hasApp(r)) continue
    if (!best || compareVersions(r.tag, best.tag) > 0) best = r
  }
  return best
}

/** A release newer than any the app can install, published within the
 *  last PENDING_MS, may still get its macOS build, so the next check comes
 *  sooner. Older ones are releases the build was not asked for. */
const PENDING_MS = 2 * 3600_000
function appPending(channel: UpdatePolicy['channel'], now = Date.now()): boolean {
  const any = latestRelease(channel, false)
  const usable = latestRelease(channel, true)
  if (!any || hasApp(any) || (usable && compareVersions(any.tag, usable.tag) <= 0)) return false
  const at = any.publishedAt ? Date.parse(any.publishedAt) : NaN
  return Number.isFinite(at) && now - at < PENDING_MS
}

// ---- status ----

const inProgress = () => ['checking', 'downloading', 'verifying', 'installing', 'restarting'].includes(st.state)

function refusal(mode: UpdateMode, rel: Release | null, available: boolean): string | null {
  if (mode === 'hosted') return 'This Studio is updated by its deployment.'
  if (mode === 'clone') {
    const b = appBundle()
    if (b) return `The folder holding the app (${path.dirname(b)}) is not writable, so the app cannot replace itself. Move the app to ~/Applications, or install the new version from the releases page.`
    return `This Studio runs from a clone of the repository and does not update itself. To update it: ${CLONE_COMMAND.split('\n').join(' && ')}, then start it again.`
  }
  if (!publicKey()) return 'This copy of the app carries no update signing key, so it cannot verify a download. Install the new version from the releases page.'
  if (!rel || !available) return null
  if (!rel.assets[ASSET] || !rel.assets[MANIFEST] || !rel.assets[SIGNATURE]) return `The release ${rel.tag} has no signed macOS app.`
  if (inProgress()) return `An update is already ${st.state}.`
  return null
}

function previousVersion(): { version: string } | null {
  if (updateMode() !== 'app') return null
  const v = versionOf(beside('previous'))
  return v ? { version: v } : null
}

/** The releases an update to `rel` brings: those newer than `from` up to
 *  and including it, on the channel, newest first. A release the app cannot
 *  install (its macOS build failed, or has not finished) still counts, since
 *  its changes are in the later one. */
function releasesUpTo(rel: Release, channel: UpdatePolicy['channel'], from: string): UpdateRelease[] {
  return loadSaved().releases
    .filter(r => (!r.prerelease || channel === 'prerelease') && isNewer(r.tag, from) && compareVersions(r.tag, rel.tag) <= 0)
    .sort((a, b) => compareVersions(b.tag, a.tag))
    .map(r => ({ version: r.tag, publishedAt: r.publishedAt, url: r.url, entries: parseNotes(r.notes) }))
}

export function updateStatus(): UpdateStatus {
  const mode = updateMode()
  const policy = effectiveSettings().updates
  const cur = running()
  const rel = mode === 'hosted' ? null : latestRelease(policy.channel)
  const available = !!rel && isNewer(rel.tag, cur.version)
  const reason = refusal(mode, rel, available)
  return {
    mode,
    current: cur,
    policy,
    latest: rel ? { version: rel.tag, publishedAt: rel.publishedAt, notes: rel.notes, url: rel.url } : null,
    releases: rel && available ? releasesUpTo(rel, policy.channel, cur.version) : [],
    available,
    canInstall: mode === 'app' && available && reason === null,
    reason,
    command: mode === 'clone' && !appBundle() ? CLONE_COMMAND : null,
    state: st.state,
    progress: st.progress,
    error: st.error ?? rollbackNote,
    checkedAt: loadSaved().checkedAt,
    offline: st.offline,
    previous: previousVersion(),
    reopen: mode === 'app' && fs.existsSync(inUpdates('reopen')),
  }
}

// ---- the change log ----

/** The change log for this version: the one the build carries, and, in the
 *  app or a clone, the notes of releases up to the running version that it
 *  lacks. The change log is regenerated after each release, so a build
 *  carries it as of the version before its own; the app has the notes of
 *  its own version from the check that found it. Versions with no entries
 *  left (housekeeping only, or a tag with no pull requests) are left out.
 *  `after` keeps only the versions newer than it. */
export function changelog(after: string | null = null): { included: boolean; current: string; versions: ChangeVersion[] } {
  const cur = running().version
  const { included, versions } = readChangelog()
  const all = [...versions]
  if (updateMode() !== 'hosted' && parseVersion(cur)) {
    for (const r of loadSaved().releases) {
      if (isNewer(r.tag, cur) || all.some(v => sameVersion(v.version, r.tag))) continue
      all.push({ version: r.tag, date: r.publishedAt ? r.publishedAt.slice(0, 10) : null, entries: parseNotes(r.notes) })
    }
  }
  const since = after && parseVersion(after) ? after : null
  return {
    included,
    current: cur,
    versions: all
      .filter(v => v.entries.length && (!since || isNewer(v.version, since)))
      .sort((a, b) => compareVersions(b.version, a.version)),
  }
}

// ---- download ----

/** A timer that aborts a request when no data has come for a while. */
function stallGuard(ms: number) {
  const ac = new AbortController()
  let t: NodeJS.Timeout
  const arm = () => { t = setTimeout(() => ac.abort(new Error(`no data for ${ms / 1000} seconds`)), ms) }
  arm()
  return { signal: ac.signal, poke: () => { clearTimeout(t); arm() }, done: () => clearTimeout(t) }
}

async function fetchSmall(url: string, limit: number): Promise<Buffer> {
  const res = await fetch(url, { headers: { 'User-Agent': userAgent() }, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) })
  if (!res.ok) throw new Error(`${path.basename(new URL(url).pathname)}: HTTP ${res.status}`)
  const buf = Buffer.from(await res.arrayBuffer())
  if (buf.length > limit) throw new Refused(`${path.basename(new URL(url).pathname)} is larger than a manifest can be.`)
  return buf
}

/** The archive, into a .part file, checked against the manifest as it comes. */
async function fetchArchive(url: string, file: string, man: Manifest): Promise<void> {
  const guard = stallGuard(REQUEST_TIMEOUT_MS)
  const hash = crypto.createHash('sha256')
  let bytes = 0
  st.progress = { bytes: 0, total: man.size }
  const part = `${file}.part`
  const fh = await fsp.open(part, 'w')
  try {
    const res = await fetch(url, { headers: { 'User-Agent': userAgent() }, signal: guard.signal })
    if (!res.ok || !res.body) throw new Error(`the download answered HTTP ${res.status}`)
    for await (const chunk of res.body as unknown as AsyncIterable<Uint8Array>) {
      guard.poke()
      bytes += chunk.length
      if (bytes > man.size) throw new Refused(`The download is larger than the ${man.size} bytes the manifest gives.`)
      hash.update(chunk)
      await fh.write(chunk)
      st.progress = { bytes, total: man.size }
    }
  } finally {
    guard.done()
    await fh.close()
  }
  if (bytes !== man.size) throw new Refused(`The download is ${bytes} bytes; the manifest says ${man.size}.`)
  if (hash.digest('hex') !== man.sha256) throw new Refused('The download\'s SHA-256 does not match the manifest.')
  await fsp.rename(part, file)
}

function parseManifest(bytes: Buffer, rel: Release, from: string): Manifest {
  let m: Partial<Manifest>
  try { m = JSON.parse(bytes.toString('utf8')) } catch { throw new Refused('The manifest is not valid JSON.') }
  if (!m || typeof m !== 'object') throw new Refused('The manifest is not a JSON object.')
  if (m.asset !== ASSET) throw new Refused(`The manifest names the asset ${String(m.asset)}; this app installs ${ASSET}.`)
  const version = String(m.version ?? '')
  if (!parseVersion(version)) throw new Refused(`The manifest's version ${version || '(none)'} is not a version.`)
  if (!isNewer(version, from)) throw new Refused(`The manifest's version ${version} is not newer than the running ${from}.`)
  if (!sameVersion(version, rel.tag)) throw new Refused(`The manifest is for ${version}, but the release is ${rel.tag}.`)
  if (typeof m.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(m.sha256)) throw new Refused('The manifest has no valid SHA-256.')
  if (!Number.isSafeInteger(m.size) || (m.size as number) <= 0) throw new Refused('The manifest has no valid size.')
  const have = macosVersion()
  if (m.minMacos && have && !dottedAtMost(String(m.minMacos), have)) {
    throw new Refused(`${version} needs macOS ${m.minMacos}; this Mac has ${have}.`)
  }
  return { version, asset: m.asset, sha256: m.sha256, size: m.size as number, minMacos: m.minMacos, builtAt: m.builtAt }
}

function isFile(p: string): boolean {
  try { return fs.statSync(p).isFile() } catch { return false }
}

function executable(p: string): boolean {
  try { fs.accessSync(p, fs.constants.X_OK); return isFile(p) } catch { return false }
}

function plistString(plist: string, key: string): string | null {
  return new RegExp(`<key>${key}</key>\\s*<string>([^<]*)</string>`).exec(plist)?.[1] ?? null
}

/** The unpacked app is this app, complete, and the version the manifest says. */
function checkBundle(dir: string, version: string): void {
  let plist = ''
  try { plist = fs.readFileSync(path.join(dir, 'Contents', 'Info.plist'), 'utf8') } catch { throw new Refused('The new app has no Info.plist.') }
  const id = plistString(plist, 'CFBundleIdentifier')
  if (id !== BUNDLE_ID) throw new Refused(`The new app's bundle identifier is ${id ?? 'missing'}, not ${BUNDLE_ID}.`)
  const exe = plistString(plist, 'CFBundleExecutable')
  if (!exe || exe.includes('/') || !executable(path.join(dir, 'Contents', 'MacOS', exe))) throw new Refused('The new app has no executable.')
  for (const f of ['Resources/studio-run.sh', 'Resources/node/bin/node', 'Resources/app/server/dist/main.js']) {
    if (!isFile(path.join(dir, 'Contents', f))) throw new Refused(`The new app has no ${f}.`)
  }
  const built = versionOf(dir)
  if (!built || !sameVersion(built, version)) throw new Refused(`The new app is ${built ?? 'of no known version'}, not ${version}.`)
}

function shellHash(bundle: string): string | null {
  try {
    const plist = fs.readFileSync(path.join(bundle, 'Contents', 'Info.plist'), 'utf8')
    const exe = plistString(plist, 'CFBundleExecutable') ?? ''
    return crypto.createHash('sha256').update(fs.readFileSync(path.join(bundle, 'Contents', 'MacOS', exe))).digest('hex')
  } catch { return null }
}

/** Every entry under the one app folder, none outside it. */
async function unpack(archive: string): Promise<string> {
  const { stdout } = await execFileP('tar', ['-tzf', archive], { maxBuffer: 128 * 1024 * 1024 })
  for (const raw of stdout.split('\n')) {
    if (!raw) continue
    const e = raw.replace(/^\.\//, '')
    if (e === APP_NAME || e === `${APP_NAME}/`) continue
    if (!e.startsWith(`${APP_NAME}/`) || e.split('/').includes('..')) throw new Refused(`The archive holds ${raw}, outside ${APP_NAME}.`)
  }
  const work = beside('unpack')
  const next = beside('new')
  await fsp.rm(work, { recursive: true, force: true })
  await fsp.rm(next, { recursive: true, force: true })
  await fsp.mkdir(work)
  await execFileP('tar', ['-xzf', archive, '-C', work], { maxBuffer: 16 * 1024 * 1024 })
  await fsp.rename(path.join(work, APP_NAME), next)
  await fsp.rm(work, { recursive: true, force: true })
  return next
}

/** Everything a failed or abandoned update leaves behind. */
async function cleanUp(): Promise<void> {
  if (appBundle()) {
    for (const s of ['new', 'unpack', 'failed'] as const) await fsp.rm(beside(s), { recursive: true, force: true }).catch(() => {})
  }
  for (const f of [ASSET, `${ASSET}.part`]) await fsp.rm(inUpdates(f), { force: true }).catch(() => {})
}

/** Download and verify the release, and unpack it beside the app as
 *  `.<name>.new`, checked; nothing is swapped yet. */
async function prepare(rel: Release): Promise<Prepared> {
  const from = running().version
  const key = publicKey()
  if (!key) throw new Refused('This copy of the app carries no update signing key.')
  const urls = { archive: rel.assets[ASSET]?.url, manifest: rel.assets[MANIFEST]?.url, sig: rel.assets[SIGNATURE]?.url }
  if (!urls.archive || !urls.manifest || !urls.sig) throw new Refused(`The release ${rel.tag} has no signed macOS app.`)
  st.state = 'downloading'
  st.progress = null
  await fsp.mkdir(updatesDir(), { recursive: true })
  const bytes = await fetchSmall(urls.manifest, 64 * 1024)
  const sig = Buffer.from((await fetchSmall(urls.sig, 4096)).toString('utf8').trim(), 'base64')
  let good = false
  try { good = sig.length === 64 && crypto.verify(null, bytes, key, sig) } catch { good = false }
  if (!good) throw new Refused('The manifest\'s signature does not verify with this app\'s key.')
  const man = parseManifest(bytes, rel, from)
  const archive = inUpdates(ASSET)
  await fetchArchive(urls.archive, archive, man)
  st.state = 'verifying'
  st.progress = null
  const next = await unpack(archive)
  await fsp.rm(archive, { force: true })
  checkBundle(next, man.version)
  return { version: man.version, from, shellChanged: shellHash(next) !== shellHash(appBundle()!) }
}

// ---- install ----

/** The LaunchAgent restarted, so its run loop starts from the app now in
 *  place; detached, since the restart stops this process and its group.
 *  Where the app started the run loop itself (no LaunchAgent), the server
 *  exits with RELAUNCH_EXIT_CODE and the run loop hands over to the new
 *  copy's own. */
function restart(): void {
  st.state = 'restarting'
  if (runsWithoutAgent()) {
    if (!requestRestart(RELAUNCH_EXIT_CODE)) {
      st.state = 'error'
      st.error = 'The new version is in place, but the Studio could not restart itself. Quit and reopen the app.'
    }
    return
  }
  let fellBack = false
  const fallBack = () => {
    if (fellBack) return
    fellBack = true
    if (requestRestart()) return
    st.state = 'error'
    st.error = 'The new version is in place, but the Studio could not restart itself. Quit and reopen the app.'
  }
  try {
    const uid = process.getuid?.() ?? 0
    const child = spawn('launchctl', ['kickstart', '-k', `gui/${uid}/${LABEL}`], { detached: true, stdio: 'ignore' })
    child.on('error', fallBack)
    child.on('exit', code => { if (code) fallBack() })
    child.unref()
  } catch { fallBack() }
}

/** Finder and the Dock keep showing the icon of the copy they saw first,
 *  since the new one sits at the same path: the bundle is touched and
 *  LaunchServices told to look at it again, so the new icon shows once the
 *  app is reopened. Best effort, and macOS only (STUDIO_LSREGISTER, for
 *  tests, names another tool). */
function refreshIcon(bundle: string): void {
  const tool = process.env.STUDIO_LSREGISTER ?? (process.platform === 'darwin' ? LSREGISTER : '')
  if (!tool) return
  const now = new Date()
  fsp.utimes(bundle, now, now).catch(() => {}).finally(() => {
    execFile(tool, ['-f', bundle], { timeout: 30_000 }, err => { if (err) log(`update: LaunchServices did not take the new icon: ${plain(err)}`) })
  })
}

async function swapIn(p: Prepared): Promise<void> {
  const app = appBundle()!
  const next = beside('new')
  const prev = beside('previous')
  st.state = 'installing'
  await fsp.rm(prev, { recursive: true, force: true })
  await fsp.rename(app, prev)
  try { await fsp.rename(next, app) } catch (e) {
    await fsp.rename(prev, app).catch(() => {})
    throw e
  }
  refreshIcon(app)
  await writeJson(inUpdates('pending.json'), { from: p.from, to: p.version, at: new Date().toISOString() })
  await fsp.rm(inUpdates('rolled-back.json'), { force: true })
  if (p.shellChanged) await fsp.writeFile(inUpdates('reopen'), `${p.version}\n`)
}

function fail(e: unknown): void {
  st.state = 'error'
  st.progress = null
  installWanted = false
  // A certificate or proxy refusal is said as such; the downloads come
  // from github.com, which hands them on to its release storage.
  const conn = e instanceof Refused ? null : connectionProblem(e, 'github.com')
  const f = e instanceof Refused || conn ? null : networkFailure(e)
  st.error = e instanceof Refused ? e.message
    : conn ? `The update failed. ${conn}`
      : `The update failed: ${plain(e)}.${f ? ' The connection to the release server was lost; try again once this computer is online.' : ''}`
}

async function install(rel: Release): Promise<void> {
  try {
    const ready = prepared?.version === rel.tag && fs.existsSync(beside('new')) ? prepared : null
    const p = ready ?? await prepare(rel)
    prepared = null
    await swapIn(p)
    log(`update: ${p.from} replaced by ${p.version}; restarting`)
    restart()
  } catch (e) {
    prepared = null
    if (e instanceof Refused) refusedVersion = rel.tag
    await cleanUp()
    fail(e)
    log(`update to ${rel.tag} not installed: ${st.error}`)
  }
}

function startInstall(rel: Release): void {
  stopIdleWait()
  installWanted = false
  st.state = 'downloading'
  st.error = null
  rollbackNote = null
  op = install(rel).finally(() => { op = null })
}

async function check(): Promise<void> {
  if (op) return op
  lastAttemptAt = Date.now()
  st.state = 'checking'
  op = (async () => {
    try {
      await fetchReleases()
      st.state = 'idle'
      st.error = null
      st.offline = false
    } catch (e) {
      // A certificate this computer does not trust, or a proxy that will
      // not carry the request, is not being offline: it is said, and stays
      // until the setting is fixed.
      const conn = connectionProblem(e, releaseHost())
      if (conn) {
        st.state = 'error'
        st.error = `The check for updates failed. ${conn}`
        st.offline = false
        log(`update check: ${conn}`)
      } else if (networkFailure(e)) {
        // Offline is a state of the computer, not a fault of the check.
        st.state = 'idle'
        st.error = null
        st.offline = true
        log(`update check: release server not reachable (${networkFailure(e)!.code}); checking again later`)
      } else {
        st.state = 'error'
        st.error = `The check for updates failed: ${plain(e)}.`
        st.offline = false
      }
    }
  })().finally(() => { op = null })
  await op
  void autoInstall()
}

/** With install set to auto: a verified update waits until the Studio has
 *  been idle (no chat answering, no indexing) for ten minutes. */
async function autoInstall(): Promise<void> {
  if (op || effectiveSettings().updates.install !== 'auto') return
  const s = updateStatus()
  const rel = latestRelease(s.policy.channel)
  if (!rel || !s.canInstall || rel.tag === refusedVersion) return
  if (readJson<{ from?: string }>(inUpdates('rolled-back.json'))?.from === rel.tag) return
  if (!(prepared?.version === rel.tag && fs.existsSync(beside('new')))) {
    op = (async () => {
      try {
        prepared = await prepare(rel)
        st.state = 'idle'
        st.progress = null
        log(`update: ${rel.tag} verified; it installs once the Studio is idle`)
      } catch (e) {
        prepared = null
        installWanted = false
        if (e instanceof Refused) refusedVersion = rel.tag
        await cleanUp()
        fail(e)
      }
    })().finally(() => { op = null })
    await op
    if (!prepared) return
    if (installWanted) { startInstall(rel); return }
  }
  if (idleTimer) return
  let quietSince = Date.now()
  idleTimer = setInterval(() => {
    if (effectiveSettings().updates.install !== 'auto' || !prepared || op) { stopIdleWait(); return }
    if (busyCheck()) { quietSince = Date.now(); return }
    if (Date.now() - quietSince >= timings.idleMs) {
      stopIdleWait()
      startInstall(rel)
    }
  }, timings.idlePollMs)
  idleTimer.unref?.()
}

function stopIdleWait(): void {
  if (idleTimer) clearInterval(idleTimer)
  idleTimer = null
}

async function rollBack(): Promise<void> {
  const app = appBundle()!
  const prev = beside('previous')
  const failed = beside('failed')
  const from = versionOf(app) ?? running().version
  const to = versionOf(prev)
  const shellChanged = shellHash(prev) !== shellHash(app)
  st.state = 'installing'
  st.error = null
  rollbackNote = null
  await fsp.rm(failed, { recursive: true, force: true })
  await fsp.rename(app, failed)
  try { await fsp.rename(prev, app) } catch (e) {
    await fsp.rename(failed, app).catch(() => {})
    throw e
  }
  await fsp.rm(failed, { recursive: true, force: true })
  refreshIcon(app)
  await writeJson(inUpdates('rolled-back.json'), { from, to, at: new Date().toISOString(), reason: 'rolled back from Settings', noted: true })
  await fsp.rm(inUpdates('pending.json'), { force: true })
  if (shellChanged) await fsp.writeFile(inUpdates('reopen'), `${to}\n`)
  prepared = null
  await cleanUp()
  log(`update: rolled back from ${from} to ${to}; restarting`)
  restart()
}

// ---- schedule and routes ----

/** main.ts says what keeps the Studio busy, which an auto install waits out. */
export function setUpdateBusyCheck(fn: () => boolean): void { busyCheck = fn }

/** The time from one check to the next: shorter while a new release waits
 *  for its macOS build, or while the release server cannot be reached. */
function waitAfterCheck(): number {
  const policy = effectiveSettings().updates
  return (updateMode() === 'app' && appPending(policy.channel)) || st.offline ? timings.soonMs : timings.intervalMs
}

/** Whether a check is due by the clock: that long since the last one
 *  started or succeeded, whichever is later, time asleep included. */
function checkDue(now = Date.now()): boolean {
  if (!effectiveSettings().updates.check || updateMode() === 'hosted' || op || inProgress()) return false
  const at = Date.parse(loadSaved().checkedAt ?? '')
  return now - Math.max(Number.isFinite(at) ? at : 0, lastAttemptAt) >= waitAfterCheck()
}

/** Checks a minute after start, then whenever 15 minutes have passed by
 *  the clock (looked at every two minutes, and when the window asks for the
 *  status), while the policy allows; in the app, also the cleanup and
 *  bookkeeping after an update. */
export function startUpdates(logger: (msg: string) => void): void {
  log = logger
  const mode = updateMode()
  if (mode === 'hosted') return
  if (mode === 'app') {
    void cleanUp()
    const back = readJson<{ from?: string; to?: string; reason?: string; noted?: boolean }>(inUpdates('rolled-back.json'))
    if (back && !back.noted) {
      rollbackNote = `${back.from ?? 'The update'} did not start properly (${back.reason ?? 'it kept stopping'}), so ${back.to ?? 'the previous version'} was put back.`
      log(`update: ${rollbackNote}`)
      void writeJson(inUpdates('rolled-back.json'), { ...back, noted: true }).catch(() => {})
    }
    // Up for a minute after an update: the run loop no longer rolls back.
    if (fs.existsSync(inUpdates('pending.json'))) {
      healthyTimer = setTimeout(() => {
        const p = readJson<{ from?: string; to?: string }>(inUpdates('pending.json'))
        fs.rmSync(inUpdates('pending.json'), { force: true })
        log(`update: ${p?.to ?? running().version} is running; ${p?.from ?? 'the previous version'} stays available to roll back to`)
      }, timings.healthyMs)
      healthyTimer.unref?.()
    }
  }
  scheduled = true
  const gen = scheduleGen
  checkTimer = setTimeout(() => {
    void (async () => {
      // The first check, unless the window already started one.
      if (effectiveSettings().updates.check && updateMode() !== 'hosted' && (!lastAttemptAt || checkDue())) await check()
      if (gen !== scheduleGen) return
      checkTimer = setInterval(() => { if (checkDue()) void check() }, timings.pollMs)
      checkTimer.unref?.()
    })()
  }, timings.firstCheckMs)
  checkTimer.unref?.()
}

/** For tests: forget everything, and optionally change timings or the version that counts as running. */
export function resetUpdatesForTests(opts: Partial<typeof timings> & { version?: string | null } = {}): void {
  for (const t of [checkTimer, healthyTimer]) if (t) clearTimeout(t)
  stopIdleWait()
  checkTimer = healthyTimer = null
  scheduleGen++
  scheduled = false
  lastAttemptAt = 0
  const { version, ...rest } = opts
  Object.assign(timings, TIMINGS, rest)
  versionOverride = version ?? null
  Object.assign(st, { state: 'idle', error: null, progress: null, offline: false })
  saved = null
  prepared = null
  op = null
  installWanted = false
  refusedVersion = null
  rollbackNote = null
  busyCheck = () => false
}

export async function updateRoutes(app: FastifyInstance): Promise<void> {
  const refuseHosted = () => {
    if (updateMode() === 'hosted') throw new KbError(403, 'This Studio is updated by its deployment, not from here.')
  }
  const requireApp = () => {
    refuseHosted()
    const mode = updateMode()
    if (mode !== 'app') throw new KbError(409, refusal(mode, null, false) ?? 'Only the macOS app installs updates.')
  }

  app.get('/api/updates', async () => {
    refuseHosted()
    // The window opening after the Mac slept past a check starts it here,
    // without waiting for the next poll; the answer says it is checking.
    if (scheduled && checkDue()) void check()
    return updateStatus()
  })

  // Every mode, hosted included: What's new reads it.
  app.get('/api/changelog', async req => {
    const after = (req.query as { after?: unknown }).after
    return changelog(typeof after === 'string' && after ? after : null)
  })

  app.post('/api/updates/check', async () => {
    refuseHosted()
    if (!inProgress()) await check()
    return updateStatus()
  })

  app.post('/api/updates/install', async () => {
    requireApp()
    if (st.state === 'checking' && op) await op
    if (!inProgress() && !loadSaved().checkedAt) await check()
    // An automatic download under way installs as soon as it is verified.
    if (op || inProgress()) {
      if (st.state === 'downloading' || st.state === 'verifying') installWanted = true
      return updateStatus()
    }
    const s = updateStatus()
    const rel = latestRelease(s.policy.channel)
    if (!rel || !s.available) throw new KbError(409, `There is no version newer than ${s.current.version}.`)
    if (!s.canInstall) throw new KbError(409, s.reason ?? 'The update cannot be installed.')
    startInstall(rel)
    return updateStatus()
  })

  app.post('/api/updates/rollback', async () => {
    requireApp()
    if (inProgress()) throw new KbError(409, `An update is ${st.state}; wait for it to finish.`)
    if (!previousVersion()) throw new KbError(409, 'There is no previous version to go back to.')
    if (op) await op
    try { await rollBack() } catch (e) {
      fail(e)
      throw new KbError(500, st.error ?? 'The rollback failed.')
    }
    return updateStatus()
  })
}
