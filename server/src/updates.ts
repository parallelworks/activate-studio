import crypto from 'node:crypto'
import { execFile, execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { promisify } from 'node:util'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { parseNotes, readChangelog, type ChangeEntry, type ChangeVersion } from './changelog.js'
import { INDEX_BASE, PROJECT_ROOT } from './config.js'
import { KbError } from './kb.js'
import { RELAUNCH_EXIT_CODE, requestRestart, runsWithoutAgent } from './lifecycle.js'
import { effectiveSettings, type UpdateChannel, type UpdatePolicy } from './settings.js'
import { compatibleSnapshot, listSnapshots, restoreSnapshot, stateFormat, takeSnapshot, type Snapshot } from './stateFormat.js'
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
 *
 * The Linux payload (docs/LINUX-APP.md) updates the same way where it is
 * installed per user: the archive and manifest are the `linux` ones, the
 * install is the payload's directory, swapped beside itself, and the run
 * loop is restarted through exit 76. An install from a .deb or .rpm package
 * (the install-kind file in the payload's top directory) never replaces
 * itself: it is told the version and the package manager's command.
 *
 * Channels, the explicit install of another version (older included), the
 * state snapshot before every change of version, and the relaunch a release
 * can ask for follow docs/UPDATES.md, which the Go server implements alike.
 */

const REPO = 'parallelworks/activate-studio'
const APP_NAME = 'ACTIVATE Studio.app'
/** The directory the Linux archive unpacks to. */
const LINUX_TOP = 'activate-studio'
/** The file in the payload's top directory that says how it was installed. */
export const INSTALL_KIND_FILE = 'install-kind'
/** The package the .deb and .rpm are named, unless install-kind names another. */
const DEFAULT_PACKAGE = 'activate-studio'
const BUNDLE_ID = 'com.parallelworks.activate-studio'
const LABEL = 'com.parallelworks.activate-studio'
const REQUEST_TIMEOUT_MS = 15_000
/** LaunchServices' own tool, which looks at an app bundle again. */
const LSREGISTER = '/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister'
/** How a clone of the repository updates itself, before a restart. */
const CLONE_COMMAND = 'git pull\npnpm install\npnpm build'

const execFileP = promisify(execFile)

/** What differs between the macOS app and the Linux payload: the release
 *  assets, the folder the archive unpacks to, and where the app's own files
 *  are in it. */
interface Kit {
  flavor: 'macos' | 'linux'
  asset: string
  manifest: string
  signature: string
  /** The one entry an archive holds. */
  top: string
  /** What the person calls it, in messages. */
  label: string
  /** The folder that holds studio-run.sh, app/ and the update key. */
  resources: (root: string) => string
}

const MACOS_KIT: Kit = {
  flavor: 'macos',
  asset: 'activate-studio-macos-arm64.tar.gz',
  manifest: 'activate-studio-macos-arm64.manifest.json',
  signature: 'activate-studio-macos-arm64.manifest.json.sig',
  top: APP_NAME,
  label: 'macOS app',
  resources: root => path.join(root, 'Contents', 'Resources'),
}

/** The architecture in the Linux asset names, or null for one that has no build. */
function linuxArch(): string | null {
  return process.arch === 'x64' ? 'x86_64' : process.arch === 'arm64' ? 'aarch64' : null
}

/** The Linux archive's name for this computer: activate-studio-linux-x86_64.tar.gz. */
export function linuxAsset(): string { return `activate-studio-linux-${linuxArch() ?? process.arch}.tar.gz` }

function linuxKit(): Kit {
  const asset = linuxAsset()
  const manifest = asset.replace(/\.tar\.gz$/, '.manifest.json')
  return { flavor: 'linux', asset, manifest, signature: `${manifest}.sig`, top: LINUX_TOP, label: 'Linux app', resources: root => root }
}

export type UpdateMode = 'app' | 'clone' | 'hosted' | 'package'
/** relaunch: a release that replaces the whole app is in place, and the
 *  app has to be reopened to run it (docs/UPDATES.md, "Relaunch"). */
export type UpdateState = 'idle' | 'checking' | 'downloading' | 'verifying' | 'installing' | 'restarting' | 'relaunch' | 'error'

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
  /** The commands that update a clone of the repository, or the package
   *  manager's command for a packaged install once a newer version is
   *  known; null otherwise. */
  command: string | null
  /** The sentence that introduces `command`, for the modes that are shown
   *  one other than a clone's. */
  commandNote: string | null
  /** For a Linux payload installed from a package: how it was installed and
   *  who updates it. */
  package: PackageInfo | null
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
  /** The signed releases this Studio can install on its channel, newest
   *  first, the running one and older ones included (Install a version). */
  versions: UpdateVersion[]
  /** On Stable while running a newer Preview: the newest Stable, which
   *  Move to Stable installs although it is older. */
  moveToStable: { version: string } | null
  /** The format of the state here, and the snapshots kept of it. */
  stateFormat: number
  snapshots: Omit<Snapshot, 'files'>[]
}

export interface UpdateVersion {
  version: string
  publishedAt: string | null
  url: string
  channel: UpdateChannel
  current: boolean
  newer: boolean
}

/** What installing a given version would do, from its signed manifest. */
export interface VersionCheck {
  version: string
  older: boolean
  channel: UpdateChannel
  /** The state format that version reads, and the one here now. */
  stateFormat: number
  currentFormat: number
  /** It reads the state as it is. */
  readable: boolean
  /** When it does not: the snapshot it would be given, or null. */
  snapshot: Omit<Snapshot, 'files'> | null
  requiresRelaunch: boolean
}

export interface UpdateRelease {
  version: string
  publishedAt: string | null
  url: string
  entries: ChangeEntry[]
}

export interface PackageInfo {
  /** deb or rpm as the install-kind file says, or other where this copy
   *  cannot replace itself and nothing says why. */
  kind: 'deb' | 'rpm' | 'other'
  manager: 'apt' | 'dnf' | null
  name: string
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

interface Manifest {
  version: string; asset: string; sha256: string; size: number; minMacos?: string; minGlibc?: string; builtAt?: string
  /** The release's channel, signed; absent is stable unless the release is a GitHub prerelease. */
  channel?: UpdateChannel
  /** The newest state format the version reads; absent is 1. */
  stateFormat?: number
  /** The version replaces the whole app: reopen it, never restart the server into it. */
  requiresRelaunch?: boolean
}

interface Prepared { version: string; from: string; shellChanged: boolean; stateFormat: number; relaunch: boolean; older: boolean }

/** How an install was asked for: the newest newer version, or one the
 *  person picked, which may be older, with the state snapshot to restore
 *  when that version cannot read the state. */
interface InstallOpts { allowOlder: boolean; restoreState: boolean }
const NEWEST: InstallOpts = { allowOlder: false, restoreState: false }

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

// ---- the Linux payload ----

interface Payload {
  /** The payload's top directory (the one with studio-run.sh in it). */
  root: string
  /** What the install-kind file says; null when it is missing or not understood. */
  marker: 'user' | 'deb' | 'rpm' | null
  /** The package name the file gives, or the default. */
  packageName: string
  /** The kind of install it is taken to be, by the file or else by who owns the path. */
  kind: 'user' | 'deb' | 'rpm' | 'other'
  /** This copy may replace itself. */
  selfUpdates: boolean
}

/** The top directory of the Linux payload this server runs from, or null.
 *  The server is app/server/dist/main.js in it, so its project root is the
 *  payload's app/; STUDIO_PAYLOAD_ROOT names another (tests, and a launcher
 *  that wants to say). Linux only, unless that is given: the macOS app has
 *  the same shape one level down. */
function payloadRoot(): string | null {
  let root = process.env.STUDIO_PAYLOAD_ROOT || ''
  if (!root) {
    if (process.platform !== 'linux' || path.basename(PROJECT_ROOT) !== 'app') return null
    root = path.dirname(PROJECT_ROOT)
  }
  try { root = fs.realpathSync(root) } catch { return null }
  // A payload that replaced this one with no run loop (requiresRelaunch)
  // starts through bin/activate-studio; until the app reopens, this server
  // runs from the copy moved aside and still counts as the app.
  const started = isFile(path.join(root, 'studio-run.sh')) || isFile(path.join(root, 'bin', 'activate-studio'))
  return started && isFile(path.join(root, 'app', 'build-info.json')) ? root : null
}

/** What the install-kind file holds: the kind, and optionally the package
 *  name, as `deb activate-studio`. Anything else is not understood. */
function readMarker(root: string): { kind: 'user' | 'deb' | 'rpm'; name: string } | null {
  let text = ''
  try { text = fs.readFileSync(path.join(root, INSTALL_KIND_FILE), 'utf8') } catch { return null }
  const [kind, name] = text.trim().split(/\s+/)
  if (kind !== 'user' && kind !== 'deb' && kind !== 'rpm') return null
  return { kind, name: name && /^[a-z0-9][a-z0-9+.-]{0,63}$/.test(name) ? name : DEFAULT_PACKAGE }
}

/** Whether this user can swap a directory for another where it stands: the
 *  directory is a real directory, and the one holding it takes new entries. */
function replaceable(root: string): boolean {
  try {
    fs.accessSync(path.dirname(root), fs.constants.W_OK | fs.constants.X_OK)
    return fs.statSync(root).isDirectory()
  } catch { return false }
}

const inferred = new Map<string, 'deb' | 'rpm' | null>()
/** With no install-kind file, a copy this user cannot replace is looked up
 *  in the package databases: dpkg's file list for the package, or the owner
 *  rpm names for studio-run.sh. */
function packageOwner(root: string): 'deb' | 'rpm' | null {
  if (inferred.has(root)) return inferred.get(root)!
  let kind: 'deb' | 'rpm' | null = null
  try {
    if (fs.readFileSync(`/var/lib/dpkg/info/${DEFAULT_PACKAGE}.list`, 'utf8').split('\n').some(l => l === path.join(root, 'studio-run.sh'))) kind = 'deb'
  } catch { /* not dpkg, or not this package */ }
  if (!kind) {
    try {
      execFileSync('rpm', ['-qf', path.join(root, 'studio-run.sh')], { stdio: 'ignore', timeout: 3000 })
      kind = 'rpm'
    } catch { /* not rpm, or not owned by a package */ }
  }
  inferred.set(root, kind)
  return kind
}

/** How the Linux payload was installed, and so who updates it. A .deb or
 *  .rpm (the install-kind file, which the package writes) never replaces
 *  itself, even where the files are writable, as to a root user: the package
 *  database would no longer match. A per-user install replaces itself where
 *  it can write. A tree with no file is judged by the path: one this user
 *  can replace is theirs; one that is not belongs to whoever installed it,
 *  a package if a package database says so. */
function payload(): Payload | null {
  const root = payloadRoot()
  if (!root) return null
  const m = readMarker(root)
  if (m && m.kind !== 'user') return { root, marker: m.kind, packageName: m.name, kind: m.kind, selfUpdates: false }
  if (replaceable(root)) return { root, marker: m?.kind ?? null, packageName: m?.name ?? DEFAULT_PACKAGE, kind: 'user', selfUpdates: true }
  return { root, marker: m?.kind ?? null, packageName: m?.name ?? DEFAULT_PACKAGE, kind: packageOwner(root) ?? 'other', selfUpdates: false }
}

export function updateMode(): UpdateMode {
  if (hosted()) return 'hosted'
  if (appBundle()) return bundleWritable() ? 'app' : 'clone'
  const p = payload()
  if (!p) return 'clone'
  return p.selfUpdates ? 'app' : 'package'
}

/** The assets and layout of what this Studio installs. Null for a clone. */
function kit(): Kit | null {
  if (appBundle()) return MACOS_KIT
  return payload() ? linuxKit() : null
}

/** The folder an update replaces: the app bundle, or the payload's top directory. */
function installRoot(): string | null {
  return appBundle() ?? payload()?.root ?? null
}

/** `.ACTIVATE Studio.app.<suffix>` beside the app, or `.activate-studio.<suffix>` beside the payload. */
function beside(suffix: 'new' | 'previous' | 'unpack' | 'failed'): string {
  const b = installRoot()!
  return path.join(path.dirname(b), `.${path.basename(b)}.${suffix}`)
}

function updatesDir(): string {
  const data = process.env.STUDIO_DATA_DIR || (process.env.STUDIO_CONFIG_FILE ? path.dirname(process.env.STUDIO_CONFIG_FILE) : '')
  return data ? path.join(data, 'updates') : path.join(INDEX_BASE, 'updates')
}

const inUpdates = (name: string) => path.join(updatesDir(), name)

function publicKey(): crypto.KeyObject | null {
  const b = installRoot()
  const k = kit()
  if (!b || !k) return null
  try {
    const key = crypto.createPublicKey(fs.readFileSync(path.join(k.resources(b), 'update-signing.pub')))
    return key.asymmetricKeyType === 'ed25519' ? key : null
  } catch { return null }
}

function versionOf(bundle: string): string | null {
  try {
    const v = JSON.parse(fs.readFileSync(path.join(kit()!.resources(bundle), 'app', 'build-info.json'), 'utf8')).version
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

/** The C library the Linux payload runs on, as glibc reports it; null on a
 *  system with another (musl) or when node does not say. */
function glibcVersion(): string | null {
  if (process.platform !== 'linux') return null
  try {
    const v = (process.report.getReport() as unknown as { header?: { glibcVersionRuntime?: string } }).header?.glibcVersionRuntime
    return v ? String(v) : null
  } catch { return null }
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

/** Whether a release carries the signed app this Studio installs from: the
 *  macOS app's archive, or the Linux payload's. */
/** A release's channel as the list shows it: a GitHub prerelease is Preview. */
export const releaseChannel = (r: { prerelease: boolean }): UpdateChannel => r.prerelease ? 'preview' : 'stable'
/** Stable takes stable releases; Preview takes both. */
const onChannel = (r: Release, channel: UpdateChannel) => channel === 'preview' || releaseChannel(r) === 'stable'

const hasApp = (r: Release) => {
  const k = kit()
  return !!k && !!(r.assets[k.asset] && r.assets[k.manifest] && r.assets[k.signature])
}

/** The modes that go by what the project has built: the app, and a package
 *  install, which is told of a version only once a build of it exists. */
const needsBuild = () => { const m = updateMode(); return m === 'app' || m === 'package' }

/** The newest release the channel allows. For the app, the newest that
 *  carries the signed app: a release is published before its macOS build
 *  attaches the app, and one whose build failed never gets it. */
function latestRelease(channel: UpdateChannel, needsApp = needsBuild()): Release | null {
  let best: Release | null = null
  for (const r of loadSaved().releases) {
    if (!onChannel(r, channel)) continue
    if (needsApp && !hasApp(r)) continue
    if (!best || compareVersions(r.tag, best.tag) > 0) best = r
  }
  return best
}

/** A release newer than any the app can install, published within the
 *  last PENDING_MS, may still get its macOS build, so the next check comes
 *  sooner. Older ones are releases the build was not asked for. */
const PENDING_MS = 2 * 3600_000
function appPending(channel: UpdateChannel, now = Date.now()): boolean {
  const any = latestRelease(channel, false)
  const usable = latestRelease(channel, true)
  if (!any || hasApp(any) || (usable && compareVersions(any.tag, usable.tag) <= 0)) return false
  const at = any.publishedAt ? Date.parse(any.publishedAt) : NaN
  return Number.isFinite(at) && now - at < PENDING_MS
}

// ---- status ----

const inProgress = () => ['checking', 'downloading', 'verifying', 'installing', 'restarting', 'relaunch'].includes(st.state)

const MANAGERS = { deb: 'apt', rpm: 'dnf' } as const

/** What the package manager is asked to do: apt refreshes its lists by hand
 *  and upgrades one package without installing it where it is missing; dnf
 *  refreshes its metadata when told to. */
function packageCommand(info: PackageInfo): string | null {
  if (info.kind === 'deb') return `sudo apt update\nsudo apt install --only-upgrade ${info.name}`
  if (info.kind === 'rpm') return `sudo dnf upgrade --refresh ${info.name}`
  return null
}

function packageInfo(): PackageInfo | null {
  const p = payload()
  if (!p || p.selfUpdates) return null
  return { kind: p.kind === 'deb' || p.kind === 'rpm' ? p.kind : 'other', manager: p.kind === 'deb' || p.kind === 'rpm' ? MANAGERS[p.kind] : null, name: p.packageName }
}

/** What a package install, or a copy this user cannot write to, says. */
function packageMessage(info: PackageInfo, available: boolean): string {
  if (info.kind === 'other') {
    const where = payload()?.root ?? 'its folder'
    return `This copy is installed in ${where}, which you cannot write to, so it cannot update itself. Update it the way it was installed, or ask whoever manages this computer; or install ACTIVATE Studio in your home folder from the releases page, which then updates itself.`
  }
  const what = info.kind === 'deb' ? 'a .deb package' : 'an .rpm package'
  const how = info.kind === 'deb' ? 'apt' : 'dnf'
  return available
    ? `This copy was installed from ${what}, so it does not update itself. Update it with ${how} in a terminal, then quit and reopen ACTIVATE Studio.`
    : `This copy was installed from ${what}, so it does not update itself; updates come through ${how}.`
}

function refusal(mode: UpdateMode, rel: Release | null, available: boolean): string | null {
  if (mode === 'hosted') return 'This Studio is updated by its deployment.'
  if (mode === 'package') {
    const info = packageInfo()
    return info ? packageMessage(info, available) : 'This copy is managed by a package manager.'
  }
  if (mode === 'clone') {
    const b = appBundle()
    if (b) return `The folder holding the app (${path.dirname(b)}) is not writable, so the app cannot replace itself. Move the app to ~/Applications, or install the new version from the releases page.`
    return `This Studio runs from a clone of the repository and does not update itself. To update it: ${CLONE_COMMAND.split('\n').join(' && ')}, then start it again.`
  }
  const k = kit()!
  if (!publicKey()) return 'This copy of the app carries no update signing key, so it cannot verify a download. Install the new version from the releases page.'
  if (!rel || !available) return null
  if (!rel.assets[k.asset] || !rel.assets[k.manifest] || !rel.assets[k.signature]) return `The release ${rel.tag} has no signed ${k.label}.`
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
function releasesUpTo(rel: Release, channel: UpdateChannel, from: string): UpdateRelease[] {
  return loadSaved().releases
    .filter(r => onChannel(r, channel) && isNewer(r.tag, from) && compareVersions(r.tag, rel.tag) <= 0)
    .sort((a, b) => compareVersions(b.tag, a.tag))
    .map(r => ({ version: r.tag, publishedAt: r.publishedAt, url: r.url, entries: parseNotes(r.notes) }))
}

/** The releases on the channel that carry the signed app, newest first. */
function installable(channel: UpdateChannel): Release[] {
  if (updateMode() !== 'app') return []
  return loadSaved().releases.filter(r => onChannel(r, channel) && hasApp(r)).sort((a, b) => compareVersions(b.tag, a.tag))
}

/** The release of that version the person may pick on the channel. */
function pickable(version: string, channel: UpdateChannel): Release | null {
  return installable(channel).find(r => sameVersion(r.tag, version)) ?? null
}

/** On Stable while the running version is newer than every Stable release
 *  (it is a Preview): the newest Stable, to go back to on purpose. */
function stableBehind(policy: UpdatePolicy, cur: string): Release | null {
  if (policy.channel !== 'stable' || !parseVersion(cur)) return null
  const r = latestRelease('stable')
  return r && compareVersions(r.tag, cur) < 0 ? r : null
}

const brief = ({ files: _f, ...s }: Snapshot) => s

export function updateStatus(): UpdateStatus {
  const mode = updateMode()
  const policy = effectiveSettings().updates
  const cur = running()
  const rel = mode === 'hosted' ? null : latestRelease(policy.channel)
  const available = !!rel && isNewer(rel.tag, cur.version)
  const reason = refusal(mode, rel, available)
  const pkg = mode === 'package' ? packageInfo() : null
  const pkgCommand = pkg && available ? packageCommand(pkg) : null
  return {
    mode,
    current: cur,
    policy,
    latest: rel ? { version: rel.tag, publishedAt: rel.publishedAt, notes: rel.notes, url: rel.url } : null,
    releases: rel && available ? releasesUpTo(rel, policy.channel, cur.version) : [],
    available,
    canInstall: mode === 'app' && available && reason === null,
    reason,
    command: mode === 'clone' && !appBundle() ? CLONE_COMMAND : pkgCommand,
    commandNote: pkgCommand && pkg ? `This copy was installed from ${pkg.kind === 'deb' ? 'a .deb package' : 'an .rpm package'}, so it does not update itself. To update, run ${pkg.kind === 'deb' ? 'these' : 'this'} in a terminal, then quit and reopen ACTIVATE Studio:` : null,
    package: pkg,
    state: st.state,
    progress: st.progress,
    error: st.error ?? rollbackNote,
    checkedAt: loadSaved().checkedAt,
    offline: st.offline,
    previous: previousVersion(),
    reopen: mode === 'app' && fs.existsSync(inUpdates('reopen')),
    versions: installable(policy.channel).map(r => ({
      version: r.tag, publishedAt: r.publishedAt, url: r.url, channel: releaseChannel(r),
      current: sameVersion(r.tag, cur.version), newer: isNewer(r.tag, cur.version),
    })),
    moveToStable: mode === 'app' ? (r => r ? { version: r.tag } : null)(stableBehind(policy, cur.version)) : null,
    stateFormat: stateFormat(),
    snapshots: mode === 'app' ? listSnapshots().map(brief) : [],
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

function parseManifest(bytes: Buffer, rel: Release, from: string, channel: UpdateChannel, allowOlder = false): Manifest {
  let m: Partial<Manifest>
  try { m = JSON.parse(bytes.toString('utf8')) } catch { throw new Refused('The manifest is not valid JSON.') }
  if (!m || typeof m !== 'object') throw new Refused('The manifest is not a JSON object.')
  const k = kit()!
  if (m.asset !== k.asset) throw new Refused(`The manifest names the asset ${String(m.asset)}; this app installs ${k.asset}.`)
  const version = String(m.version ?? '')
  if (!parseVersion(version)) throw new Refused(`The manifest's version ${version || '(none)'} is not a version.`)
  if (allowOlder ? sameVersion(version, from) : !isNewer(version, from)) {
    throw new Refused(allowOlder ? `${version} is the version running.` : `The manifest's version ${version} is not newer than the running ${from}.`)
  }
  if (!sameVersion(version, rel.tag)) throw new Refused(`The manifest is for ${version}, but the release is ${rel.tag}.`)
  // The channel is signed: a release marked Preview that was published as
  // a stable release is refused, since older Studios go by the flag alone.
  if (m.channel !== undefined && m.channel !== 'stable' && m.channel !== 'preview') throw new Refused(`The manifest's channel ${String(m.channel)} is not stable or preview.`)
  if (m.channel === 'preview' && !rel.prerelease) throw new Refused(`${version} is a Preview release, but it was published as a stable release.`)
  const relChannel: UpdateChannel = m.channel === 'preview' || rel.prerelease ? 'preview' : 'stable'
  if (relChannel === 'preview' && channel !== 'preview') throw new Refused(`${version} is a Preview release; this Studio is on Stable.`)
  if (m.stateFormat !== undefined && (!Number.isSafeInteger(m.stateFormat) || m.stateFormat < 1)) throw new Refused('The manifest\'s stateFormat is not a whole number of 1 or more.')
  if (m.requiresRelaunch !== undefined && typeof m.requiresRelaunch !== 'boolean') throw new Refused('The manifest\'s requiresRelaunch is not true or false.')
  if (typeof m.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(m.sha256)) throw new Refused('The manifest has no valid SHA-256.')
  if (!Number.isSafeInteger(m.size) || (m.size as number) <= 0) throw new Refused('The manifest has no valid size.')
  if (k.flavor === 'macos') {
    const have = macosVersion()
    if (m.minMacos && have && !dottedAtMost(String(m.minMacos), have)) {
      throw new Refused(`${version} needs macOS ${m.minMacos}; this Mac has ${have}.`)
    }
  } else {
    const have = glibcVersion()
    if (m.minGlibc && have && !dottedAtMost(String(m.minGlibc), have)) {
      throw new Refused(`${version} needs glibc ${m.minGlibc}; this computer has ${have}.`)
    }
  }
  return {
    version, asset: m.asset, sha256: m.sha256, size: m.size as number, minMacos: m.minMacos, minGlibc: m.minGlibc, builtAt: m.builtAt,
    channel: relChannel, stateFormat: m.stateFormat ?? 1, requiresRelaunch: m.requiresRelaunch === true,
  }
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

/** The unpacked Linux payload is this app, complete, and the version the
 *  manifest says. One that replaces the whole app (requiresRelaunch) need
 *  not hold Node or the run loop: it is started by its own launcher. */
function checkPayload(dir: string, version: string, relaunch = false): void {
  if (relaunch) {
    if (!executable(path.join(dir, 'bin', 'activate-studio'))) throw new Refused('The new app has no bin/activate-studio.')
    for (const f of ['update-signing.pub', 'manifest.json']) if (!isFile(path.join(dir, f))) throw new Refused(`The new app has no ${f}.`)
    const v = versionOf(dir)
    if (!v || !sameVersion(v, version)) throw new Refused(`The new app is ${v ?? 'of no known version'}, not ${version}.`)
    return
  }
  if (!executable(path.join(dir, 'studio-run.sh'))) throw new Refused('The new app has no studio-run.sh.')
  for (const f of ['node/bin/node', 'app/server/dist/main.js', 'update-signing.pub', 'manifest.json']) {
    if (!isFile(path.join(dir, f))) throw new Refused(`The new app has no ${f}.`)
  }
  if (!executable(path.join(dir, 'node', 'bin', 'node'))) throw new Refused('The new app has no executable node.')
  const built = versionOf(dir)
  if (!built || !sameVersion(built, version)) throw new Refused(`The new app is ${built ?? 'of no known version'}, not ${version}.`)
}

/** The unpacked app is this app, complete, and the version the manifest says. */
function checkBundle(dir: string, version: string, relaunch = false): void {
  if (kit()!.flavor === 'linux') return checkPayload(dir, version, relaunch)
  let plist = ''
  try { plist = fs.readFileSync(path.join(dir, 'Contents', 'Info.plist'), 'utf8') } catch { throw new Refused('The new app has no Info.plist.') }
  const id = plistString(plist, 'CFBundleIdentifier')
  if (id !== BUNDLE_ID) throw new Refused(`The new app's bundle identifier is ${id ?? 'missing'}, not ${BUNDLE_ID}.`)
  const exe = plistString(plist, 'CFBundleExecutable')
  if (!exe || exe.includes('/') || !executable(path.join(dir, 'Contents', 'MacOS', exe))) throw new Refused('The new app has no executable.')
  // A copy that replaces the whole app (requiresRelaunch) starts through its
  // own launcher and need not hold the Node run loop.
  for (const f of relaunch ? ['Resources/update-signing.pub'] : ['Resources/studio-run.sh', 'Resources/node/bin/node', 'Resources/app/server/dist/main.js']) {
    if (!isFile(path.join(dir, 'Contents', f))) throw new Refused(`The new app has no ${f}.`)
  }
  const built = versionOf(dir)
  if (!built || !sameVersion(built, version)) throw new Refused(`The new app is ${built ?? 'of no known version'}, not ${version}.`)
}

/** The window's program, so an update that changes it can ask for the
 *  window to be reopened. For the Linux payload, manifest.json's `shell`
 *  names it (a path inside the payload); a payload with none has no window
 *  of its own to reopen. */
function shellHash(bundle: string): string | null {
  if (kit()!.flavor === 'linux') {
    try {
      const rel = String(readJson<{ shell?: unknown }>(path.join(bundle, 'manifest.json'))?.shell ?? '')
      if (!rel || path.isAbsolute(rel) || rel.split('/').includes('..')) return null
      return crypto.createHash('sha256').update(fs.readFileSync(path.join(bundle, rel))).digest('hex')
    } catch { return null }
  }
  try {
    const plist = fs.readFileSync(path.join(bundle, 'Contents', 'Info.plist'), 'utf8')
    const exe = plistString(plist, 'CFBundleExecutable') ?? ''
    return crypto.createHash('sha256').update(fs.readFileSync(path.join(bundle, 'Contents', 'MacOS', exe))).digest('hex')
  } catch { return null }
}

/** Every entry under the one app folder, none outside it. */
async function unpack(archive: string): Promise<string> {
  const top = kit()!.top
  const { stdout } = await execFileP('tar', ['-tzf', archive], { maxBuffer: 128 * 1024 * 1024 })
  for (const raw of stdout.split('\n')) {
    if (!raw) continue
    const e = raw.replace(/^\.\//, '')
    if (e === top || e === `${top}/`) continue
    if (!e.startsWith(`${top}/`) || e.split('/').includes('..')) throw new Refused(`The archive holds ${raw}, outside ${top}.`)
  }
  const work = beside('unpack')
  const next = beside('new')
  await rmTree(work)
  await rmTree(next)
  await fsp.mkdir(work)
  // Files belong to whoever unpacks them, whatever the archive records.
  await execFileP('tar', ['-xzf', archive, ...(kit()!.flavor === 'linux' ? ['--no-same-owner'] : []), '-C', work], { maxBuffer: 16 * 1024 * 1024 })
  await fsp.rename(path.join(work, top), next)
  await rmTree(work)
  return next
}

/** The directories of a tree writable by their owner, as a tree unpacked from
 *  an archive may not be (Python's standard library has read-only ones), so
 *  that it can be deleted. Links are not followed. */
async function makeWritable(dir: string): Promise<void> {
  // The failed delete may still be removing parts of the tree in the background.
  try { await fsp.chmod(dir, 0o700 | (await fsp.stat(dir)).mode) } catch { return }
  let entries: fs.Dirent[] = []
  try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
  for (const e of entries) if (e.isDirectory()) await makeWritable(path.join(dir, e.name))
}

/** Delete a tree, read-only directories in it included. */
async function rmTree(p: string): Promise<void> {
  try {
    await fsp.rm(p, { recursive: true, force: true })
  } catch (e) {
    const code = (e as NodeJS.ErrnoException).code
    if (code !== 'EACCES' && code !== 'EPERM') throw e
    await makeWritable(p)
    await fsp.rm(p, { recursive: true, force: true })
  }
}

/** Everything a failed or abandoned update leaves behind. */
async function cleanUp(): Promise<void> {
  if (installRoot()) {
    for (const s of ['new', 'unpack', 'failed'] as const) await rmTree(beside(s)).catch(() => {})
  }
  const k = kit()
  if (k) for (const f of [k.asset, `${k.asset}.part`]) await fsp.rm(inUpdates(f), { force: true }).catch(() => {})
}

/** Download and verify the release, and unpack it beside the app as
 *  `.<name>.new`, checked; nothing is swapped yet. */
/** The release's manifest, its signature verified with this app's key and
 *  its fields checked against the release, the channel and the running version. */
async function verifiedManifest(rel: Release, allowOlder: boolean): Promise<Manifest> {
  const from = running().version
  const key = publicKey()
  if (!key) throw new Refused('This copy of the app carries no update signing key.')
  const k = kit()!
  const urls = { manifest: rel.assets[k.manifest]?.url, sig: rel.assets[k.signature]?.url }
  if (!rel.assets[k.asset]?.url || !urls.manifest || !urls.sig) throw new Refused(`The release ${rel.tag} has no signed ${k.label}.`)
  const bytes = await fetchSmall(urls.manifest, 64 * 1024)
  const sig = Buffer.from((await fetchSmall(urls.sig, 4096)).toString('utf8').trim(), 'base64')
  let good = false
  try { good = sig.length === 64 && crypto.verify(null, bytes, key, sig) } catch { good = false }
  if (!good) throw new Refused('The manifest\'s signature does not verify with this app\'s key.')
  return parseManifest(bytes, rel, from, effectiveSettings().updates.channel, allowOlder)
}

/** What the state needs for a version reading formats up to `format`:
 *  nothing, a snapshot to put back, or a refusal when none is kept. */
function stateNeeds(format: number): { readable: boolean; snapshot: Snapshot | null } {
  if (stateFormat() <= format) return { readable: true, snapshot: null }
  return { readable: false, snapshot: compatibleSnapshot(format) }
}

async function prepare(rel: Release, opts: InstallOpts = NEWEST): Promise<Prepared> {
  const from = running().version
  const k = kit()!
  st.state = 'downloading'
  st.progress = null
  await fsp.mkdir(updatesDir(), { recursive: true })
  const man = await verifiedManifest(rel, opts.allowOlder)
  const need = stateNeeds(man.stateFormat ?? 1)
  if (!need.readable && !need.snapshot) {
    throw new Refused(`${man.version} reads state up to format ${man.stateFormat ?? 1}; the state here is in format ${stateFormat()}, and no snapshot in a format it reads is kept.`)
  }
  if (!need.readable && !opts.restoreState) {
    throw new Refused(`${man.version} cannot read the state as it is now; installing it puts back the state saved ${need.snapshot!.at}, which has to be asked for.`)
  }
  const archive = inUpdates(k.asset)
  await fetchArchive(rel.assets[k.asset].url, archive, man)
  st.state = 'verifying'
  st.progress = null
  const next = await unpack(archive)
  await fsp.rm(archive, { force: true })
  checkBundle(next, man.version, man.requiresRelaunch === true)
  if (k.flavor === 'linux') await markUser(next)
  return {
    version: man.version, from, shellChanged: shellHash(next) !== shellHash(installRoot()!),
    stateFormat: man.stateFormat ?? 1, relaunch: man.requiresRelaunch === true, older: compareVersions(man.version, from) < 0,
  }
}

// ---- install ----

/** The new payload says it was installed per user, as the one it replaces
 *  does (the archive carries no install-kind file: that is for the
 *  installer or the package to write). */
async function markUser(dir: string): Promise<void> {
  const name = readMarker(installRoot()!)?.name
  await fsp.writeFile(path.join(dir, INSTALL_KIND_FILE), `user${name && name !== DEFAULT_PACKAGE ? ` ${name}` : ''}\n`)
}

/** The LaunchAgent restarted, so its run loop starts from the app now in
 *  place; detached, since the restart stops this process and its group.
 *  Where the app started the run loop itself (no LaunchAgent), the server
 *  exits with RELAUNCH_EXIT_CODE and the run loop hands over to the new
 *  copy's own. */
function restart(): void {
  st.state = 'restarting'
  // The Linux payload has no agent: its run loop is the supervisor, and
  // exit 76 makes it run the new copy's own.
  if (kit()?.flavor === 'linux' || runsWithoutAgent()) {
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
  if (kit()?.flavor !== 'macos') return
  const tool = process.env.STUDIO_LSREGISTER ?? (process.platform === 'darwin' ? LSREGISTER : '')
  if (!tool) return
  const now = new Date()
  fsp.utimes(bundle, now, now).catch(() => {}).finally(() => {
    execFile(tool, ['-f', bundle], { timeout: 30_000 }, err => { if (err) log(`update: LaunchServices did not take the new icon: ${plain(err)}`) })
  })
}

async function swapIn(p: Prepared, restoreState = false): Promise<void> {
  const app = installRoot()!
  const next = beside('new')
  const prev = beside('previous')
  st.state = 'installing'
  // The state as it is, before anything changes (docs/UPDATES.md, "State").
  const need = stateNeeds(p.stateFormat)
  if (!need.readable && (!restoreState || !need.snapshot)) throw new Refused(`${p.version} cannot read the state here, and no snapshot it reads was chosen.`)
  try { takeSnapshot({ from: p.from, to: p.version, reason: p.older ? 'before installing an older version' : 'before an update' }) } catch (e) {
    throw new Refused(`The state could not be saved before the change, so nothing was changed: ${plain(e)}.`)
  }
  await rmTree(prev)
  // The payload is at no path for the time between the two moves. A process
  // cut off there leaves it that way, so the Linux swap is written down
  // first: the run loop, and the launcher, read this file at start and
  // finish the swap or undo it (deploy/linux/studio-recover.sh).
  const linux = kit()?.flavor === 'linux'
  const journal = inUpdates('swap.json')
  if (linux) {
    await writeJson(journal, { from: p.from, to: p.version, at: new Date().toISOString(), reopen: p.shellChanged ? 'yes' : 'no' })
  }
  try {
    await fsp.rename(app, prev)
  } catch (e) {
    await fsp.rm(journal, { force: true })
    throw e
  }
  try { await fsp.rename(next, app) } catch (e) {
    await fsp.rename(prev, app).catch(() => {})
    await fsp.rm(journal, { force: true })
    throw e
  }
  refreshIcon(app)
  await writeJson(inUpdates('pending.json'), { from: p.from, to: p.version, at: new Date().toISOString() })
  await fsp.rm(inUpdates('rolled-back.json'), { force: true })
  if (p.shellChanged || p.relaunch) await fsp.writeFile(inUpdates('reopen'), `${p.version}\n`)
  if (p.relaunch) await writeJson(inUpdates('relaunch.json'), { from: p.from, to: p.version, at: new Date().toISOString() })
  await fsp.rm(journal, { force: true })
  if (!need.readable && need.snapshot) {
    const s = restoreSnapshot(need.snapshot.id, p.from)
    log(`update: the state saved ${s.at} (format ${s.format}) was put back for ${p.version}`)
  }
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

async function install(rel: Release, opts: InstallOpts = NEWEST): Promise<void> {
  try {
    const ready = !opts.allowOlder && prepared?.version === rel.tag && fs.existsSync(beside('new')) ? prepared : null
    const p = ready ?? await prepare(rel, opts)
    prepared = null
    await swapIn(p, opts.restoreState)
    if (p.relaunch) {
      // The new copy replaces the whole app: this server keeps running from
      // the old copy until the app is reopened, and never restarts into it.
      st.state = 'relaunch'
      st.progress = null
      log(`update: ${p.from} replaced by ${p.version}, which runs once the app is reopened`)
      return
    }
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

function startInstall(rel: Release, opts: InstallOpts = NEWEST): void {
  stopIdleWait()
  installWanted = false
  st.state = 'downloading'
  st.error = null
  rollbackNote = null
  op = install(rel, opts).finally(() => { op = null })
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
  // A release that replaces the whole app waits for Update now: the app
  // has to be reopened, which only a person in front of it can do.
  if (prepared?.relaunch) return
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
  const app = installRoot()!
  const prev = beside('previous')
  const failed = beside('failed')
  const from = versionOf(app) ?? running().version
  const to = versionOf(prev)
  const shellChanged = shellHash(prev) !== shellHash(app)
  st.state = 'installing'
  st.error = null
  rollbackNote = null
  // The version rolled back to puts back a snapshot at its start if it
  // cannot read the state (stateFormat.ts, reconcileState).
  takeSnapshot({ from, to, reason: 'before a rollback' })
  await rmTree(failed)
  await fsp.rename(app, failed)
  try { await fsp.rename(prev, app) } catch (e) {
    await fsp.rename(failed, app).catch(() => {})
    throw e
  }
  await rmTree(failed)
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

/** What an automatic install would wait on now; the Go server in front
 *  reads it in /api/index/status while Node still answers chats and indexes. */
export function updateBusy(): boolean { return busyCheck() }

/** The time from one check to the next: shorter while a new release waits
 *  for its macOS build, or while the release server cannot be reached. */
function waitAfterCheck(): number {
  const policy = effectiveSettings().updates
  return (needsBuild() && appPending(policy.channel)) || st.offline ? timings.soonMs : timings.intervalMs
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
    // This copy runs: a relaunch an update asked for has happened.
    fs.rmSync(inUpdates('relaunch.json'), { force: true })
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
    if (mode !== 'app') throw new KbError(409, refusal(mode, null, false) ?? 'Only an installed app updates itself.')
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

  app.post('/api/updates/install', async req => {
    requireApp()
    const body = (req.body ?? {}) as { version?: unknown; restoreState?: unknown }
    if (body.version !== undefined && body.version !== null) {
      // A version the person picked: newer or older, on the channel.
      if (typeof body.version !== 'string' || !parseVersion(body.version)) throw new KbError(400, 'version must be a release version')
      if (op || inProgress()) throw new KbError(409, `An update is ${st.state}; wait for it to finish.`)
      const s = updateStatus()
      if (s.reason) throw new KbError(409, s.reason)
      const rel = pickable(body.version, s.policy.channel)
      if (!rel) throw new KbError(409, `${body.version} is not a release this Studio can install on the ${s.policy.channel === 'preview' ? 'Preview' : 'Stable'} channel.`)
      if (sameVersion(rel.tag, s.current.version)) throw new KbError(409, `${rel.tag} is the version running.`)
      startInstall(rel, { allowOlder: true, restoreState: body.restoreState === true })
      return updateStatus()
    }
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

  // What installing a version would do, from its signed manifest: older or
  // not, and whether it reads the state or needs a snapshot put back.
  app.post('/api/updates/inspect', async req => {
    requireApp()
    const v = (req.body as { version?: unknown } | null)?.version
    if (typeof v !== 'string' || !parseVersion(v)) throw new KbError(400, 'version must be a release version')
    const s = updateStatus()
    const rel = pickable(v, s.policy.channel)
    if (!rel) throw new KbError(409, `${v} is not a release this Studio can install on the ${s.policy.channel === 'preview' ? 'Preview' : 'Stable'} channel.`)
    let man: Manifest
    try { man = await verifiedManifest(rel, true) } catch (e) {
      throw new KbError(e instanceof Refused ? 409 : 502, e instanceof Refused ? e.message : `The release could not be read: ${plain(e)}.`)
    }
    const need = stateNeeds(man.stateFormat ?? 1)
    const out: VersionCheck = {
      version: man.version, older: compareVersions(man.version, s.current.version) < 0, channel: man.channel ?? 'stable',
      stateFormat: man.stateFormat ?? 1, currentFormat: stateFormat(), readable: need.readable,
      snapshot: need.snapshot ? brief(need.snapshot) : null, requiresRelaunch: man.requiresRelaunch === true,
    }
    return out
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
