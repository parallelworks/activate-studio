import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GATEWAY_BASE, INDEX_BASE, PROJECT_ROOT, PW_CLI, TEXT_SUFFIXES } from './config.js'
import { authEnabled } from './auth.js'
import { gatewayKey } from './chat/gateway.js'
import { resolveUserCred } from './credentials.js'
import { classify, KbError, type FileContent, type KbEntry } from './kb.js'
import { remoteRun, siteName, SSH_DESTINATION, sshSite, type RemoteResult } from './remoteExec.js'
import { noteGufi } from './gufiTrace.js'

/**
 * Libraries a user adds from one of their clusters. The index workflow
 * (deploy/gufi-index.yaml) builds a GUFI index of a directory there,
 * `$WORKDIR` by default, in the user's own space; the Studio registers it
 * in the user's account on first use and runs it with the user's key. When
 * the run finishes, the Studio reads the index summary the run prints and
 * keeps the library for that user alone. Browsing and search then run
 * gufi on the cluster as that user, over their remote session
 * (remoteExec.ts), so they see what their account there can read.
 *
 * A library can also be an existing GUFI index on a host the user reaches
 * with plain ssh (their keys, agent, and ~/.ssh/config): no workflow and
 * no build, ready once the connection test passes, always read only. The
 * Studio's own ssh identity is the server account's, so these are offered
 * only on a Studio with sign-in off, which has one user.
 *
 * An existing index on one of the user's clusters is connected the same
 * way over `pw ssh` (transport 'pw', `existing`): the same test, nothing
 * built, read only, read with the user's platform key or, with sign-in
 * off, the pw CLI's own sign-in on this computer.
 *
 * A user library's index root, as the rest of the Studio sees it, is an
 * opaque reference ("remote:<owner>:<id>"); gufi.ts hands any query on one
 * to remoteQuery here. Paths and keys never reach the browser.
 */

export interface UserLibraryDef {
  id: string
  label: string
  /** How the host is reached: `pw ssh` to a cluster on the platform (the
   *  default, and what every library saved before ssh has), or plain ssh. */
  transport?: 'pw' | 'ssh'
  /** The cluster; over ssh, the destination, as the library is labeled. */
  site: string
  /** Over ssh: user@host or an ~/.ssh/config alias, and a port other than ssh's default. */
  destination?: string | null
  port?: number | null
  /** An index that was there already (over ssh always): connected after a
   *  connection test rather than built by the index workflow. */
  existing?: boolean
  /** Over ssh, from the last connection test: the host's `uname -s` and gufi_query's version. */
  os?: string | null
  gufiVersion?: string | null
  /** The cluster as a workflow input: pw://<owner>/<site>. */
  resource: string
  /** The directory as asked for, e.g. $WORKDIR. */
  sourceDir: string
  sourceRoot: string | null
  indexRoot: string | null
  gufiBin: string | null
  state: 'building' | 'ready' | 'updating' | 'failed'
  run: { slug: string; mode: 'build' | 'update'; startedAt: string; checkedAt?: string } | null
  error: string | null
  builtAt: string | null
  directories: number | null
  files: number | null
  scheduler: { partition?: string; account?: string; qos?: string; time?: string } | null
}

export const REMOTE_PREFIX = 'remote:'
const ID_RE = /^u-[a-z0-9][a-z0-9-]{0,38}$/
const SAFE_DIR = /^(?:~|\$HOME|\$WORKDIR)?(?:\/[A-Za-z0-9_.@+-]+)*\/?$/
const ABS = /^\/(?:[A-Za-z0-9_.@+-]+\/?)*$/
const PREVIEW_BYTES = 512 * 1024
export const WORKFLOW_FILE = path.join(PROJECT_ROOT, 'deploy', 'gufi-index.yaml')

const ownerHash = (owner: string) => createHash('sha256').update(owner).digest('hex').slice(0, 16)
const fileFor = (owner: string) => path.join(INDEX_BASE, 'users', ownerHash(owner), 'libraries.json')

/** Who owns a viewer's libraries: their id, or "single" when sign-in is off. */
export function ownerOf(viewerId: string | null | undefined): string | null {
  if (viewerId) return viewerId
  return authEnabled() ? null : 'single'
}

/** The key a library's owner acts with: their own; the deployment's only
 *  on a one-person Studio. */
export function keyOf(owner: string): string | null {
  const cred = owner === 'single' ? null : resolveUserCred(owner)
  if (cred && !cred.baseUrl && cred.key) return cred.key
  return authEnabled() ? null : gatewayKey() || null
}

export function loadUserLibraries(owner: string): UserLibraryDef[] {
  try {
    const raw = JSON.parse(fs.readFileSync(fileFor(owner), 'utf8')) as unknown
    return Array.isArray(raw) ? raw.filter((d): d is UserLibraryDef => !!d && typeof d === 'object' && ID_RE.test(String((d as UserLibraryDef).id))) : []
  } catch { return [] }
}

function saveUserLibraries(owner: string, defs: UserLibraryDef[]): void {
  const f = fileFor(owner)
  fs.mkdirSync(path.dirname(f), { recursive: true })
  // The owner's id is kept beside the list, so a query on an opaque root
  // can find whose key to act with.
  fs.writeFileSync(path.join(path.dirname(f), 'owner'), owner)
  fs.writeFileSync(f, JSON.stringify(defs, null, 2))
}

function updateDef(owner: string, id: string, patch: Partial<UserLibraryDef>): UserLibraryDef | null {
  const defs = loadUserLibraries(owner)
  const d = defs.find(x => x.id === id)
  if (!d) return null
  Object.assign(d, patch)
  saveUserLibraries(owner, defs)
  return d
}

export const remoteRootOf = (owner: string, id: string) => `${REMOTE_PREFIX}${ownerHash(owner)}:${id}`

const SSH_SINGLE_USER = 'Libraries over ssh use the ssh keys of the account the Studio runs as, so they are offered only on a Studio with sign-in off.'
/** Whether a viewer may use libraries over ssh: only the one user of a
 *  Studio with sign-in off. */
export function sshAllowed(viewerId: string | null | undefined): boolean {
  return !authEnabled() && ownerOf(viewerId) === 'single'
}

/** What a remote root resolves to, or null for a local path. `dyldLib` is
 *  set for a macOS host: where its GUFI tools' libraries are. */
export function remoteTarget(root: string | undefined | null): { site: string; key: string; gufiBin: string; indexRoot: string; sourceRoot: string | null; builtAt: string | null; dyldLib: string | null } | null {
  if (!root || !root.startsWith(REMOTE_PREFIX)) return null
  const [hash, id] = root.slice(REMOTE_PREFIX.length).split(':')
  let owner = ''
  try { owner = fs.readFileSync(path.join(INDEX_BASE, 'users', hash, 'owner'), 'utf8') } catch { throw new KbError(404, 'no such library') }
  const d = loadUserLibraries(owner).find(x => x.id === id)
  if (!d || !d.indexRoot || !d.gufiBin) throw new KbError(409, 'this library is not built yet')
  const dyldLib = d.os === 'Darwin' ? `${path.posix.dirname(d.gufiBin)}/lib` : null
  if (d.transport === 'ssh') {
    if (owner !== 'single' || authEnabled()) throw new KbError(403, SSH_SINGLE_USER)
    return { site: sshSite(String(d.destination ?? ''), d.port ?? null), key: '', gufiBin: d.gufiBin, indexRoot: d.indexRoot, sourceRoot: d.sourceRoot, builtAt: d.builtAt, dyldLib }
  }
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'This library is read with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  return { site: d.site, key, gufiBin: d.gufiBin, indexRoot: d.indexRoot, sourceRoot: d.sourceRoot, builtAt: d.builtAt, dyldLib }
}

/** Runs gufi_query on the library's cluster, as gufi.ts would locally. */
export async function remoteQuery(root: string, args: string[], subdir?: string): Promise<string> {
  const t = remoteTarget(root)!
  const target = subdir ? path.posix.join(t.indexRoot, cleanRel(subdir)) : t.indexRoot
  const t0 = Date.now()
  const r = await remoteRun(t.site, t.key, [`${t.gufiBin}/gufi_query`, ...args, target])
  noteGufi([`${t.gufiBin}/gufi_query`, ...args, target], { site: siteName(t.site), ms: Date.now() - t0 })
  // Like the local runner: a nonzero exit with rows still means rows.
  if (r.code !== 0 && !r.stdout) throw new KbError(502, `gufi_query on ${siteName(t.site)} failed: ${r.stderr.trim().split('\n').pop() ?? r.code}`)
  return r.stdout
}

/** The remote index's own root, to strip from the paths gufi prints. */
export function remoteIndexRoot(root: string): string { return remoteTarget(root)!.indexRoot }

function cleanRel(rel: string): string {
  const parts = String(rel ?? '').split('/').filter(p => p && p !== '.')
  if (parts.some(p => p === '..' || !/^[^\0]+$/.test(p))) throw new KbError(400, 'invalid path')
  return parts.join('/')
}

/** One directory of a library on a cluster, from its index: subdirectories
 *  first, then files, as the Library tree lists a local folder. */
export async function remoteTree(root: string, rel: string): Promise<KbEntry[]> {
  const t = remoteTarget(root)!
  const sub = cleanRel(rel)
  const target = sub ? path.posix.join(t.indexRoot, sub) : t.indexRoot
  // One round trip for both listings; the SQL is fixed text and the paths
  // arrive as positional arguments, so nothing user-typed meets a shell.
  // On macOS the library path is set inside the script: the system bash
  // starts without any DYLD_* variable it was given.
  const script = `[ -z "$4" ] || export DYLD_LIBRARY_PATH="$4"; "$1"/gufi_query -d "$3" -n 1 --min-level 0 --max-level 0 -E 'SELECT name, type, size, mtime FROM vrpentries;' "$2"; printf '\\n__DIRS__\\n'; "$1"/gufi_query -d "$3" -n 1 --min-level 1 --max-level 1 -S 'SELECT name, mtime FROM vrsummary;' "$2"`
  const r = await remoteRun(t.site, t.key, ['bash', '-c', script, '_', t.gufiBin, target, '\x1e', t.dyldLib ?? ''])
  const [filesPart, dirsPart = ''] = r.stdout.split('\n__DIRS__\n')
  const rows = (s: string) => s.split('\n').filter(Boolean).map(l => l.split('\x1e'))
  const at = (name: string) => (sub ? `${sub}/${name}` : name)
  const dirs: KbEntry[] = rows(dirsPart).filter(x => x[0]).map(x => ({ name: x[0], path: at(x[0]), type: 'dir', size: 0, mtime: Number(x[1]) || 0, kind: 'dir' }))
  const files: KbEntry[] = rows(filesPart).filter(x => x[0] && x[1] !== 'd').map(x => ({ name: x[0], path: at(x[0]), type: 'file', size: Number(x[2]) || 0, mtime: Number(x[3]) || 0, kind: classify(x[0], false) }))
  const byName = (a: KbEntry, b: KbEntry) => a.name.localeCompare(b.name)
  return [...dirs.sort(byName), ...files.sort(byName)]
}

// stat's format option differs: GNU and busybox take -c, macOS and BSD -f.
// Defines `st <gnu format> <bsd format> <file>` in a bash script.
const STAT = 'st() { if stat -c %s / >/dev/null 2>&1; then stat -c "$1" -- "$3"; else stat -f "$2" -- "$3"; fi; }; st'

/** A file of a library on a cluster: text files are previewed, others say
 *  where they are. */
export async function remoteFile(root: string, rel: string): Promise<FileContent> {
  const t = remoteTarget(root)!
  if (!t.sourceRoot) throw new KbError(409, 'the source directory of this library is not known')
  const sub = cleanRel(rel)
  const abs = path.posix.join(t.sourceRoot, sub)
  const kind = classify(path.posix.basename(sub), false)
  const text = TEXT_SUFFIXES.has(path.posix.extname(sub).toLowerCase())
  // Size and modified time from the file itself, then the text when it is
  // text, in one round trip.
  const r = await remoteRun(t.site, t.key, ['bash', '-c', `${STAT} "%s %Y" "%z %m" "$1" || exit 3; [ "$2" = 1 ] && head -c "$3" -- "$1"; exit 0`, '_', abs, text ? '1' : '0', String(PREVIEW_BYTES + 1)])
  if (r.code !== 0) throw new KbError(404, `${siteName(t.site)}: ${r.stderr.trim().split('\n').pop() ?? 'cannot read the file'}`)
  const nl = r.stdout.indexOf('\n')
  const [size, mtime] = r.stdout.slice(0, nl < 0 ? undefined : nl).split(' ').map(Number)
  const base = { path: sub, kind, size: size || 0, mtime: mtime || 0 }
  if (!text) return { ...base, truncated: false, content: null, source: 'none' }
  const body = nl < 0 ? '' : r.stdout.slice(nl + 1)
  return { ...base, truncated: body.length > PREVIEW_BYTES, content: body.slice(0, PREVIEW_BYTES), source: 'raw' }
}

/** Largest file the Studio fetches from a cluster for download. */
export const DOWNLOAD_MAX = 50 * 1024 * 1024

/** A file of a library on a cluster, whole, for download. */
export async function remoteDownload(root: string, rel: string): Promise<{ name: string; body: Buffer }> {
  const t = remoteTarget(root)!
  if (!t.sourceRoot) throw new KbError(409, 'the source directory of this library is not known')
  const sub = cleanRel(rel)
  const abs = path.posix.join(t.sourceRoot, sub)
  const r = await remoteRun(t.site, t.key, ['bash', '-c', `s=$(${STAT} %s %z "$1") || exit 3; [ "$s" -le "$2" ] || exit 4; cat -- "$1"`, '_', abs, String(DOWNLOAD_MAX)], 300_000)
  if (r.code === 4) throw new KbError(413, `This file is over ${DOWNLOAD_MAX / 1024 / 1024} MB; copy it from ${siteName(t.site)} directly.`)
  if (r.code !== 0) throw new KbError(404, `${siteName(t.site)}: ${r.stderr.trim().split('\n').pop() ?? 'cannot read the file'}`)
  return { name: path.posix.basename(sub), body: r.raw }
}

// ---- building and refreshing with the index workflow ----

function pw(args: string[], key: string, timeoutMs = 120_000): Promise<string> {
  let host = ''
  try { host = new URL(GATEWAY_BASE).hostname } catch { /* the CLI's own context */ }
  return new Promise((resolve, reject) => {
    execFile(PW_CLI, [...args, ...(host ? ['--platform-host', host] : [])], { timeout: timeoutMs, maxBuffer: 16 * 1024 * 1024, env: { ...process.env, PW_API_KEY: key } },
      (err, out, stderr) => (err ? reject(new KbError(502, String(stderr || err.message).replace(/\x1b\[[0-9;]*m/g, '').trim().split('\n').pop()?.slice(0, 300) ?? 'pw failed')) : resolve(String(out))))
  })
}

function firstJson<T>(text: string): T {
  const start = text.search(/[[{]/)
  if (start < 0) throw new KbError(502, 'no JSON from the platform CLI')
  const open = text[start], close = open === '[' ? ']' : '}'
  let depth = 0, inStr = false, esc = false
  for (let i = start; i < text.length; i++) {
    const c = text[i]
    if (inStr) { if (esc) esc = false; else if (c === '\\') esc = true; else if (c === '"') inStr = false; continue }
    if (c === '"') inStr = true
    else if (c === open) depth++
    else if (c === close && --depth === 0) return JSON.parse(text.slice(start, i + 1)) as T
  }
  return JSON.parse(text.slice(start)) as T
}

/** The workflow's name in the user's account, versioned by its content so
 *  a changed workflow registers afresh and no stale copy runs. */
function workflowName(): string {
  const yaml = fs.readFileSync(WORKFLOW_FILE)
  return `studio-gufi-index-${createHash('sha256').update(yaml).digest('hex').slice(0, 8)}`
}

async function ensureWorkflow(key: string): Promise<string> {
  const name = workflowName()
  try { await pw(['workflows', 'get', name, '-o', 'json'], key, 60_000); return name } catch { /* absent: create it */ }
  await pw(['workflows', 'create', '--yaml', WORKFLOW_FILE, '--display-name', 'Studio library index', name], key, 60_000)
  return name
}

async function launch(d: UserLibraryDef, key: string, mode: 'build' | 'update'): Promise<string> {
  const name = await ensureWorkflow(key)
  const s = d.scheduler
  const inputs = {
    resource: d.resource,
    cluster: { scheduler: !!s?.partition, ...(s?.partition ? { slurm: { partition: s.partition, ...(s.account ? { account: s.account } : {}), ...(s.qos ? { qos: s.qos } : {}), time: s.time || '01:00:00' } } : {}) },
    index: { source_dir: d.sourceDir, index_dir: d.indexRoot ?? '', mode, threads: 4 },
  }
  const out = await pw(['workflows', 'run', '-i', JSON.stringify(inputs), name, '-o', 'json'], key, 180_000)
  const doc = firstJson<{ run?: { slug?: string; id?: string }; slug?: string; id?: string }>(out)
  const slug = String(doc.run?.slug ?? doc.slug ?? doc.run?.id ?? doc.id ?? '')
  if (!slug) throw new KbError(502, 'the platform returned no run for the index workflow')
  return slug
}

/** The index summary the workflow prints between its markers. */
export function manifestFrom(log: string): Record<string, unknown> | null {
  const m = /STUDIO_INDEX_BEGIN\s*\n([\s\S]*?)\n\s*STUDIO_INDEX_END/.exec(log)
  if (!m) return null
  try { return JSON.parse(m[1]) as Record<string, unknown> } catch { return null }
}

const TERMINAL = new Set(['completed', 'error', 'failed', 'canceled', 'cancelled'])

/** Follows a library's run one step: on completion it reads the summary
 *  and makes the library ready; on failure it says so. */
async function checkRun(owner: string, d: UserLibraryDef, key: string): Promise<void> {
  if (!d.run || (d.state !== 'building' && d.state !== 'updating')) return
  if (d.run.checkedAt && Date.now() - Date.parse(d.run.checkedAt) < 15_000) return
  updateDef(owner, d.id, { run: { ...d.run, checkedAt: new Date().toISOString() } })
  let status = ''
  try { status = String(firstJson<{ status?: string }>(await pw(['workflows', 'runs', 'view', d.run.slug, '-o', 'json'], key, 60_000)).status ?? '') } catch { return }
  if (!TERMINAL.has(status.toLowerCase())) return
  if (status.toLowerCase() !== 'completed') { updateDef(owner, d.id, { state: d.indexRoot ? 'ready' : 'failed', error: `the index run ${d.run.slug} ended ${status}` }); return }
  const m = manifestFrom(await pw(['workflows', 'runs', 'logs', d.run.slug], key, 120_000).catch(() => ''))
  if (!m || typeof m.index !== 'string') { updateDef(owner, d.id, { state: d.indexRoot ? 'ready' : 'failed', error: `run ${d.run.slug} finished without an index summary` }); return }
  let indexRoot = String(m.index), sourceRoot = typeof m.source === 'string' ? m.source : d.sourceRoot
  const commit = String(m.gufiCommit ?? '').replace(/[^0-9a-f]/g, '')
  // The summary may use ~; the remote shell resolves it, and the GUFI
  // install it ran from.
  const r = await remoteRun(d.site, key, ['bash', '-c', 'cd "$HOME" && echo "$HOME"; ls -d "$HOME"/.activate-studio/gufi/"$1"*/bin 2>/dev/null | head -1', '_', commit])
  const [home = '', bin = ''] = r.stdout.trim().split('\n')
  const expand = (p: string | null) => (p && home ? p.replace(/^~(?=\/|$)/, home).replace(/^\$HOME(?=\/|$)/, home) : p)
  indexRoot = expand(indexRoot) ?? indexRoot; sourceRoot = expand(sourceRoot)
  if (!ABS.test(indexRoot) || !bin.startsWith('/')) { updateDef(owner, d.id, { state: 'failed', error: 'the index summary named a location the Studio cannot use' }); return }
  updateDef(owner, d.id, {
    state: 'ready', error: null, indexRoot, sourceRoot: sourceRoot && ABS.test(sourceRoot) ? sourceRoot : null, gufiBin: bin,
    builtAt: String(m.builtAt ?? new Date().toISOString()), directories: Number(m.directories) || null, files: Number(m.files) || null,
  })
}

/** A viewer's libraries, with any running build followed one step. */
export async function listUserLibraries(viewerId: string | null | undefined): Promise<UserLibraryDef[]> {
  const owner = ownerOf(viewerId)
  if (!owner) return []
  const key = keyOf(owner)
  if (key) for (const d of loadUserLibraries(owner)) await checkRun(owner, d, key).catch(() => {})
  return loadUserLibraries(owner)
}

export interface AddFromCluster { site: string; resource?: string; sourceDir?: string; label?: string; scheduler?: UserLibraryDef['scheduler'] }

export async function addFromCluster(viewerId: string | null | undefined, a: AddFromCluster): Promise<UserLibraryDef> {
  const owner = ownerOf(viewerId)
  if (!owner) throw new KbError(401, 'sign in to add your own libraries')
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'Libraries on your clusters are built with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  const site = String(a.site ?? '').trim()
  if (!/^[A-Za-z0-9_.-]{1,120}$/.test(site)) throw new KbError(400, 'choose one of your clusters')
  const sourceDir = String(a.sourceDir ?? '$WORKDIR').trim() || '$WORKDIR'
  if (!SAFE_DIR.test(sourceDir)) throw new KbError(400, 'directory: an absolute path, or one under ~, $HOME, or $WORKDIR, in plain path characters')
  const resource = a.resource && /^pw:\/\/[A-Za-z0-9_.@-]+\/[A-Za-z0-9_.-]+$/.test(a.resource) ? a.resource : await resourceOf(site, key)
  const defs = loadUserLibraries(owner)
  const label = (String(a.label ?? '').trim() || `${site} ${sourceDir}`).slice(0, 60)
  const base = `u-${`${site}-${sourceDir}`.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'library'}`
  let id = base
  for (let i = 2; defs.some(d => d.id === id); i++) id = `${base}-${i}`
  const s = a.scheduler
  const scheduler = s?.partition ? {
    partition: String(s.partition).replace(/[^\w.-]/g, '').slice(0, 64),
    ...(s.account ? { account: String(s.account).replace(/[^\w.-]/g, '').slice(0, 64) } : {}),
    ...(s.qos ? { qos: String(s.qos).replace(/[^\w.-]/g, '').slice(0, 64) } : {}),
    ...(s.time && /^\d{1,3}:\d{2}:\d{2}$/.test(String(s.time)) ? { time: String(s.time) } : {}),
  } : null
  const def: UserLibraryDef = { id, label, site, resource, sourceDir, sourceRoot: null, indexRoot: null, gufiBin: null, state: 'building', run: null, error: null, builtAt: null, directories: null, files: null, scheduler }
  const slug = await launch(def, key, 'build')
  def.run = { slug, mode: 'build', startedAt: new Date().toISOString() }
  saveUserLibraries(owner, [...defs, def])
  return def
}

async function resourceOf(site: string, key: string): Promise<string> {
  const rows = firstJson<{ name?: string; user?: string | { username?: string } }[]>(await pw(['clusters', 'ls', '--status', 'active', '-o', 'json'], key, 90_000))
  const hit = (Array.isArray(rows) ? rows : []).find(r => r?.name === site)
  if (!hit) throw new KbError(404, `${site} is not one of your active clusters`)
  const user = typeof hit.user === 'string' ? hit.user : hit.user?.username
  return user ? `pw://${user}/${site}` : site
}

export async function refreshUserLibrary(viewerId: string | null | undefined, id: string): Promise<UserLibraryDef> {
  const owner = ownerOf(viewerId)
  const d = owner ? loadUserLibraries(owner).find(x => x.id === id) : null
  if (!owner || !d) throw new KbError(404, 'no such library')
  if (d.transport === 'ssh' || d.existing) return recheckExisting(viewerId, owner, d)
  if (d.state === 'building' || d.state === 'updating') return d
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'add your ACTIVATE API key under Settings, Model access')
  const mode = d.indexRoot ? 'update' : 'build'
  const slug = await launch(d, key, mode)
  return updateDef(owner, id, { state: d.indexRoot ? 'updating' : 'building', error: null, run: { slug, mode, startedAt: new Date().toISOString() } })!
}

// ---- libraries over plain ssh ----

export interface SshLibraryInput { label?: unknown; destination?: unknown; port?: unknown; indexRoot?: unknown; gufiBin?: unknown; sourceRoot?: unknown }
export interface SshSpec { destination: string; port: number | null; indexRoot: string; gufiBin: string | null; sourceRoot: string | null }
export interface SshStep { name: 'ssh' | 'gufi' | 'index' | 'source'; ok: boolean; detail: string }
export interface SshProbe {
  ok: boolean
  steps: SshStep[]
  gufiBin?: string
  gufiVersion?: string
  directories?: number
  /** For adding the library: the host's `uname -s`, the paths with ~
   *  resolved, the file count, and when the index root was last written. */
  os?: string
  indexRoot?: string
  sourceRoot?: string | null
  files?: number
  builtAt?: string | null
}

/** A path on the remote host: absolute, or under ~, which the test
 *  resolves there. */
function remotePath(v: unknown, what: string): string | null {
  const s = String(v ?? '').trim()
  if (!s) return null
  if (s.length > 1024 || /[\0-\x1f\x7f]/.test(s) || !/^(?:\/|~(?:\/|$))/.test(s) || s.split('/').includes('..')) {
    throw new KbError(400, `${what}: an absolute path on that host, or one under ~`)
  }
  return s.length > 1 ? s.replace(/\/+$/, '') : s
}

/** A request's fields, checked: nothing here reaches a shell, but ssh
 *  would take a destination starting with "-" as an option. */
export function sshSpec(a: SshLibraryInput): SshSpec {
  const destination = String(a.destination ?? '').trim()
  if (!SSH_DESTINATION.test(destination)) {
    throw new KbError(400, 'destination: user@host, or a Host alias from your ~/.ssh/config, in letters, digits, dots, dashes, and underscores (for an IPv6 address, use an alias)')
  }
  const port = a.port === '' || a.port == null ? null : Number(a.port)
  if (port != null && !(Number.isInteger(port) && port >= 1 && port <= 65535)) throw new KbError(400, 'port: a number from 1 to 65535')
  const indexRoot = remotePath(a.indexRoot, 'index directory')
  if (!indexRoot) throw new KbError(400, 'index directory: the top of the GUFI index on that host, the directory with its db.db')
  return { destination, port, indexRoot, gufiBin: remotePath(a.gufiBin, 'GUFI bin directory'), sourceRoot: remotePath(a.sourceRoot, 'source directory') }
}

const lastLine = (s: string) => s.trim().split('\n').filter(Boolean).pop() ?? ''

/** What a failed ssh session means, and what the user can do about it. */
function sshAdvice(message: string, spec: SshSpec): string {
  const dest = spec.destination
  const said = message.replace(`${siteName(sshSite(dest, spec.port))}: `, '')
  if (/ENOENT/.test(said)) return 'There is no ssh client on the machine the Studio runs on.'
  if (/Permission denied/i.test(said)) return `${dest} did not accept a key and asked for a password or another prompt, which the Studio cannot answer. Set up key access (for example ssh-copy-id ${dest}), load the key into your ssh agent or name it in ~/.ssh/config, then test again.`
  if (/Host key verification failed|IDENTIFICATION HAS CHANGED/i.test(said)) return `The host key of ${dest} is not known here yet, or has changed. Connect once from a terminal (ssh ${dest}) and check and accept the key, then test again.`
  if (/Could not resolve hostname|not known|nodename nor servname/i.test(said)) return `The host name in ${dest} does not resolve. Check the spelling, or the Host entry in ~/.ssh/config.`
  if (/Connection refused/i.test(said)) return `Nothing accepted the connection on port ${spec.port ?? 22}. Check the port, and that the ssh server runs on ${dest}.`
  if (/timed out|No route to host|Network is unreachable|did not finish/i.test(said)) return `${dest} did not answer. Check the address, and that this machine can reach it (a VPN, for example).`
  if (/bash: .*not found|bash: No such file/i.test(said)) return `bash is not installed on ${dest}; the Studio runs its commands there through bash.`
  return `The ssh connection failed: ${said}`
}

// The index root: a directory with a db.db, and that file's modified time.
// Without one, the directories below that have one, since gufi_dir2index
// writes the index one level down, named after the indexed directory.
const INDEX_CHECK = `[ -d "$1" ] || exit 3
if [ ! -f "$1/db.db" ]; then for f in "$1"/*/db.db; do [ -f "$f" ] && printf '%s\\n' "\${f%/db.db}"; done | head -n 3; exit 4; fi
[ -r "$1/db.db" ] || exit 5
if stat -c %Y / >/dev/null 2>&1; then stat -c %Y -- "$1/db.db"; else stat -f %m -- "$1/db.db"; fi`

// The first gufi_query among the directories given.
const FIND_GUFI = 'for d in "$@"; do [ -f "$d/gufi_query" ] && [ -x "$d/gufi_query" ] && { printf "%s\\n" "$d"; exit 0; }; done; exit 1'

/** Where a connection test runs: the session's site and key, the host as
 *  messages name it, and what a failed session means there. */
interface ProbeHost {
  site: string
  key: string
  name: string
  /** How the first step reads once connected, e.g. "me@box on port 2222". */
  reached: string
  advice: (message: string) => string
}

/**
 * The connection test, step by step: ssh (or pw ssh) reaches the host;
 * gufi_query is there (the directory given, else the PATH a non-interactive
 * login has, then where GUFI's packages and the Studio's index workflow
 * install it) and runs; the index directory is a GUFI index, counted with
 * one aggregate gufi_query; the source directory, when given, is readable.
 * Each step says what it found or what to change.
 */
async function probeIndex(h: ProbeHost, spec: { indexRoot: string; gufiBin: string | null; sourceRoot: string | null }): Promise<SshProbe> {
  const dest = h.name
  const steps: SshStep[] = []
  const done = (extra: Partial<SshProbe> = {}): SshProbe => ({ ok: steps.every(s => s.ok), steps, ...extra })
  const run = (argv: string[], ms = 60_000): Promise<RemoteResult> =>
    remoteRun(h.site, h.key, argv, ms).catch((e: Error) => ({ code: -1, stdout: '', stderr: e.message, raw: Buffer.alloc(0) }))

  const hello = await run(['sh', '-c', 'uname -s; printf "%s\\n" "$HOME"'], 45_000)
  if (hello.code !== 0) {
    steps.push({ name: 'ssh', ok: false, detail: h.advice(hello.stderr || `exit ${hello.code}`) })
    return done()
  }
  const [os = '', home = ''] = hello.stdout.trim().split('\n')
  const mac = os === 'Darwin'
  steps.push({ name: 'ssh', ok: true, detail: `Connected to ${h.reached}, a ${mac ? 'macOS' : os || 'Unix'} host.` })
  const expand = (p: string | null) => (p && /^~(?=\/|$)/.test(p) && home.startsWith('/') ? home.replace(/\/+$/, '') + p.slice(1) : p)

  const given = expand(spec.gufiBin)
  // The PATH first, then the GUFI the index workflow installs in the
  // account (newest first, the one its indexes were built with), then the
  // usual places.
  const search = given ? FIND_GUFI : `c=$(command -v gufi_query 2>/dev/null); s=$(ls -dt "$HOME"/.activate-studio/gufi/*/bin 2>/dev/null); set -- $s "$@"; case "$c" in /*) set -- "\${c%/*}" "$@" ;; esac; ${FIND_GUFI}`
  const found = await run(['sh', '-c', search, '_', ...(given ? [given] : ['/usr/local/bin', `${home}/usr/local/bin`, '/opt/gufi/bin'])])
  const bin = found.code === 0 ? lastLine(found.stdout) : ''
  let version = ''
  if (!bin.startsWith('/')) {
    steps.push({ name: 'gufi', ok: false, detail: given
      ? `There is no gufi_query in ${given} on ${dest}. Give the directory that holds it, such as /usr/local/bin.`
      : `gufi_query was not found on ${dest}: not on the PATH an ssh login gets, nor in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, or /opt/gufi/bin. Install GUFI there, or give the directory that holds gufi_query as the GUFI bin directory.` })
  } else {
    const v = await run([`${bin}/gufi_query`, '--version'])
    version = v.code === 0 ? lastLine(v.stdout).slice(0, 80) : ''
    steps.push(v.code === 0
      ? { name: 'gufi', ok: true, detail: `gufi_query ${version || '(no version)'} in ${bin}.` }
      : { name: 'gufi', ok: false, detail: `gufi_query in ${bin} did not run: ${lastLine(v.stderr) || `exit ${v.code}`}.${mac ? ` On macOS its libraries are expected in ${path.posix.dirname(bin)}/lib.` : ''}` })
  }

  const indexRoot = expand(spec.indexRoot)!
  const at = await run(['sh', '-c', INDEX_CHECK, '_', indexRoot])
  let directories: number | undefined, files: number | undefined, builtAt: string | null = null
  if (at.code === 0) {
    const mtime = Number(lastLine(at.stdout))
    builtAt = mtime > 0 ? new Date(mtime * 1000).toISOString() : null
    if (!version) steps.push({ name: 'index', ok: true, detail: `${indexRoot} has a db.db; its directories are counted once gufi_query runs.` })
    else {
      const c = await run([`${bin}/gufi_query`, '-n', '4', '-d', '|', '-a', '1',
        '-I', 'CREATE TABLE t (d INT64, f INT64);', '-K', 'CREATE TABLE agg (d INT64, f INT64);',
        '-E', 'INSERT INTO t SELECT count(*), sum(totfiles) FROM vrsummary;', '-J', 'INSERT INTO agg SELECT * FROM t;',
        '-G', 'SELECT sum(d), sum(f) FROM agg;', indexRoot], 60_000)
      const [d, f] = lastLine(c.stdout).split('|').map(Number)
      if (c.stdout.trim() && Number.isFinite(d)) {
        directories = d; files = Number.isFinite(f) ? f : undefined
        steps.push({ name: 'index', ok: true, detail: `A GUFI index of ${d.toLocaleString('en-US')} directories${files != null ? ` and ${files.toLocaleString('en-US')} files` : ''}.` })
      } else if (/did not finish/.test(c.stderr)) {
        steps.push({ name: 'index', ok: true, detail: `${indexRoot} has a db.db; counting its directories took over a minute, so the count is left out.` })
      } else {
        steps.push({ name: 'index', ok: false, detail: `gufi_query could not read the index in ${indexRoot}: ${lastLine(c.stderr) || `exit ${c.code}`}.` })
      }
    }
  } else {
    const below = at.code === 4 ? at.stdout.trim().split('\n').filter(Boolean) : []
    steps.push({ name: 'index', ok: false, detail:
      at.code === 3 ? `${indexRoot} is not a directory on ${dest}.`
      : at.code === 4 ? `${indexRoot} has no db.db, so it is not the top of a GUFI index. ${below.length ? `The index may be one level down: ${below.join(', ')}.` : 'Give the directory gufi_dir2index created, the one holding db.db.'}`
      : at.code === 5 ? `The ssh login cannot read ${indexRoot}/db.db.`
      : `The index could not be checked: ${lastLine(at.stderr) || `exit ${at.code}`}.` })
  }

  const sourceRoot = expand(spec.sourceRoot)
  if (sourceRoot) {
    const s = await run(['sh', '-c', '[ -d "$1" ] || exit 3; [ -r "$1" ] && [ -x "$1" ] || exit 4', '_', sourceRoot])
    steps.push(s.code === 0
      ? { name: 'source', ok: true, detail: `${sourceRoot} is readable; files open from there.` }
      : { name: 'source', ok: false, detail: s.code === 3 ? `${sourceRoot} is not a directory on ${dest}. Leave the source directory empty to browse and search names only.`
        : s.code === 4 ? `${sourceRoot} is there, but the ssh login cannot read it.` : `The source directory could not be checked: ${lastLine(s.stderr) || `exit ${s.code}`}.` })
  }
  return done({ gufiBin: bin.startsWith('/') ? bin : undefined, gufiVersion: version || undefined, directories, files, os, indexRoot, sourceRoot, builtAt })
}

/** The connection test over plain ssh. */
export function probeSsh(spec: SshSpec): Promise<SshProbe> {
  return probeIndex({
    site: sshSite(spec.destination, spec.port), key: '', name: spec.destination,
    reached: `${spec.destination}${spec.port ? ` on port ${spec.port}` : ''}`,
    advice: m => sshAdvice(m, spec),
  }, spec)
}

// ---- existing indexes on a cluster, over pw ssh ----

export interface PwLibraryInput { label?: unknown; cluster?: unknown; indexRoot?: unknown; gufiBin?: unknown; sourceRoot?: unknown }
export interface PwSpec { cluster: string; indexRoot: string; gufiBin: string | null; sourceRoot: string | null }

/** A cluster's name as pw ssh takes it: nothing it would read as an option. */
const CLUSTER = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,119}$/

export function pwSpec(a: PwLibraryInput): PwSpec {
  const cluster = String(a.cluster ?? '').trim()
  if (!CLUSTER.test(cluster)) throw new KbError(400, 'cluster: the name of one of your running clusters')
  const indexRoot = remotePath(a.indexRoot, 'index directory')
  if (!indexRoot) throw new KbError(400, 'index directory: the top of the GUFI index on that cluster, the directory with its db.db')
  return { cluster, indexRoot, gufiBin: remotePath(a.gufiBin, 'GUFI bin directory'), sourceRoot: remotePath(a.sourceRoot, 'source directory') }
}

/** What a failed pw ssh session means, and what the user can do about it. */
function pwAdvice(message: string, cluster: string): string {
  const said = message.replace(`${cluster}: `, '')
  if (/ENOENT/.test(said)) return 'The pw CLI is not installed on the machine the Studio runs on.'
  if (/authenticat|unauthori[sz]ed|\b401\b|expired|not logged in|sign in/i.test(said)) return 'The pw CLI was refused: its sign-in is missing or has expired. Run pw auth on the machine the Studio runs on, then test again.'
  if (/not found|no such|does not exist|not running|not active|unknown (?:resource|cluster)|could not find/i.test(said)) return `${cluster} is not one of your running clusters. Start it on ACTIVATE, or choose another.`
  if (/timed out|did not finish|No route to host|Network is unreachable/i.test(said)) return `${cluster} did not answer. Check that it is running.`
  if (/bash: .*not found|bash: No such file/i.test(said)) return `bash is not installed on ${cluster}; the Studio runs its commands there through bash.`
  return `pw ssh to ${cluster} failed: ${said}`
}

/** The connection test over pw ssh, with the owner's platform key. */
export function probePw(spec: PwSpec, key: string): Promise<SshProbe> {
  return probeIndex({ site: spec.cluster, key, name: spec.cluster, reached: spec.cluster, advice: m => pwAdvice(m, spec.cluster) }, spec)
}

/** Whether the pw CLI is on this machine, at PW_CLI or on the PATH. */
export function pwCliFound(): boolean {
  const runnable = (f: string) => { try { fs.accessSync(f, fs.constants.X_OK); return fs.statSync(f).isFile() } catch { return false } }
  if (PW_CLI.includes('/')) return runnable(PW_CLI)
  return (process.env.PATH ?? '').split(path.delimiter).some(d => !!d && runnable(path.join(d, PW_CLI)))
}

/** The key a viewer's pw ssh acts with, or why there is none. */
function pwKey(viewerId: string | null | undefined): { owner: string; key: string } {
  const owner = ownerOf(viewerId)
  if (!owner) throw new KbError(401, 'sign in to add your own libraries')
  const key = keyOf(owner)
  if (!key) {
    throw new KbError(409, owner === 'single'
      ? 'The pw CLI on this computer is not signed in. Run pw auth, then test again.'
      : 'Indexes on your clusters are read with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  }
  return { owner, key }
}

/** Whether a viewer can connect an index on a cluster: the pw CLI is here
 *  and there is a key to act with. */
export function pwAllowed(viewerId: string | null | undefined): boolean {
  const owner = ownerOf(viewerId)
  return !!owner && !!keyOf(owner) && pwCliFound()
}

/** Adds an existing GUFI index on a cluster over pw ssh, after the
 *  connection test passes here again: ready at once, read only. */
export async function addFromPw(viewerId: string | null | undefined, a: PwLibraryInput): Promise<UserLibraryDef> {
  const { owner, key } = pwKey(viewerId)
  const spec = pwSpec(a)
  const p = await probePw(spec, key)
  if (!p.ok) throw new KbError(400, `The connection test did not pass. ${p.steps.find(s => !s.ok)?.detail ?? ''}`.trim())
  const indexRoot = p.indexRoot!
  const defs = loadUserLibraries(owner)
  const same = defs.find(d => d.transport !== 'ssh' && d.site === spec.cluster && d.indexRoot === indexRoot)
  if (same) throw new KbError(409, `This index is already a library: ${same.label}.`)
  const name = path.posix.basename(indexRoot) || 'index'
  const label = (String(a.label ?? '').trim() || `${name} on ${spec.cluster}`).slice(0, 60)
  const base = `u-${`${spec.cluster}-${name}`.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'library'}`
  let id = base
  for (let i = 2; defs.some(d => d.id === id); i++) id = `${base}-${i}`
  const def: UserLibraryDef = {
    id, label, transport: 'pw', existing: true, site: spec.cluster, destination: null, port: null, os: p.os || null, gufiVersion: p.gufiVersion ?? null,
    resource: spec.cluster, sourceDir: p.sourceRoot ?? '', sourceRoot: p.sourceRoot ?? null, indexRoot, gufiBin: p.gufiBin!,
    state: 'ready', run: null, error: null, builtAt: p.builtAt ?? new Date().toISOString(), directories: p.directories ?? null, files: p.files ?? null, scheduler: null,
  }
  saveUserLibraries(owner, [...defs, def])
  return def
}

/** Adds an existing GUFI index on a host over ssh, after the connection
 *  test passes here again: ready at once, read only, nothing built. */
export async function addFromSsh(viewerId: string | null | undefined, a: SshLibraryInput): Promise<UserLibraryDef> {
  if (!sshAllowed(viewerId)) throw new KbError(403, SSH_SINGLE_USER)
  const owner = ownerOf(viewerId)!
  const spec = sshSpec(a)
  const p = await probeSsh(spec)
  if (!p.ok) throw new KbError(400, `The connection test did not pass. ${p.steps.find(s => !s.ok)?.detail ?? ''}`.trim())
  const indexRoot = p.indexRoot!
  const defs = loadUserLibraries(owner)
  const same = defs.find(d => d.transport === 'ssh' && d.destination === spec.destination && (d.port ?? null) === spec.port && d.indexRoot === indexRoot)
  if (same) throw new KbError(409, `This index is already a library: ${same.label}.`)
  const host = spec.destination.replace(/^.*@/, '')
  const name = path.posix.basename(indexRoot) || 'index'
  const label = (String(a.label ?? '').trim() || `${name} on ${host}`).slice(0, 60)
  const base = `u-${`${host}-${name}`.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'library'}`
  let id = base
  for (let i = 2; defs.some(d => d.id === id); i++) id = `${base}-${i}`
  const def: UserLibraryDef = {
    id, label, transport: 'ssh', site: spec.destination, destination: spec.destination, port: spec.port, os: p.os || null, gufiVersion: p.gufiVersion ?? null,
    resource: sshSite(spec.destination, spec.port), sourceDir: p.sourceRoot ?? '', sourceRoot: p.sourceRoot ?? null, indexRoot, gufiBin: p.gufiBin!,
    state: 'ready', run: null, error: null, builtAt: p.builtAt ?? new Date().toISOString(), directories: p.directories ?? null, files: p.files ?? null, scheduler: null,
  }
  saveUserLibraries(owner, [...defs, def])
  return def
}

/** Refresh, for an index that was there already: nothing to rebuild, so
 *  the connection test runs again and the counts and index time are
 *  brought up to date. */
async function recheckExisting(viewerId: string | null | undefined, owner: string, d: UserLibraryDef): Promise<UserLibraryDef> {
  let p: SshProbe
  if (d.transport === 'ssh') {
    if (!sshAllowed(viewerId)) throw new KbError(403, SSH_SINGLE_USER)
    p = await probeSsh(sshSpec({ destination: d.destination, port: d.port, indexRoot: d.indexRoot, gufiBin: d.gufiBin, sourceRoot: d.sourceRoot }))
  } else {
    p = await probePw(pwSpec({ cluster: d.site, indexRoot: d.indexRoot, gufiBin: d.gufiBin, sourceRoot: d.sourceRoot }), pwKey(viewerId).key)
  }
  const failed = p.steps.find(s => !s.ok)
  return updateDef(owner, d.id, failed ? { error: failed.detail } : {
    error: null, os: p.os || d.os || null, gufiVersion: p.gufiVersion ?? d.gufiVersion ?? null,
    directories: p.directories ?? d.directories, files: p.files ?? d.files, builtAt: p.builtAt ?? d.builtAt,
  })!
}

export function removeUserLibrary(viewerId: string | null | undefined, id: string): void {
  const owner = ownerOf(viewerId)
  if (!owner) throw new KbError(404, 'no such library')
  const defs = loadUserLibraries(owner)
  if (!defs.some(d => d.id === id)) throw new KbError(404, 'no such library')
  saveUserLibraries(owner, defs.filter(d => d.id !== id))
}

/** The shape Settings shows: no keys, and the remote paths, which are the user's own. */
export function publicUserLibrary(d: UserLibraryDef) {
  return { id: d.id, label: d.label, transport: d.transport ?? 'pw', existing: d.transport === 'ssh' || !!d.existing, site: d.site, destination: d.destination ?? null, port: d.port ?? null, gufiVersion: d.gufiVersion ?? null, sourceDir: d.sourceDir, sourceRoot: d.sourceRoot, indexRoot: d.indexRoot, state: d.state,
    run: d.run ? { slug: d.run.slug, mode: d.run.mode, startedAt: d.run.startedAt } : null, error: d.error, builtAt: d.builtAt,
    directories: d.directories, files: d.files, scheduler: d.scheduler }
}

export async function userLibraryRoutes(app: FastifyInstance): Promise<void> {
  const viewer = (req: { user?: { id?: string } }) => req.user?.id ?? null
  app.get('/api/user-libraries', async req => ({
    libraries: (await listUserLibraries(viewer(req))).map(publicUserLibrary), available: !!ownerOf(viewer(req)), ssh: sshAllowed(viewer(req)), pw: pwAllowed(viewer(req)),
  }))
  app.post('/api/user-libraries', async req => publicUserLibrary(await addFromCluster(viewer(req), (req.body ?? {}) as AddFromCluster)))
  app.post('/api/user-libraries/ssh/test', async req => {
    if (!sshAllowed(viewer(req))) throw new KbError(403, SSH_SINGLE_USER)
    const p = await probeSsh(sshSpec((req.body ?? {}) as SshLibraryInput))
    return { ok: p.ok, steps: p.steps, gufiBin: p.gufiBin, gufiVersion: p.gufiVersion, directories: p.directories }
  })
  app.post('/api/user-libraries/ssh', async req => publicUserLibrary(await addFromSsh(viewer(req), (req.body ?? {}) as SshLibraryInput)))
  // The same test and library for an existing index on a cluster, over pw ssh.
  app.post('/api/user-libraries/pw/test', async req => {
    const { key } = pwKey(viewer(req))
    const p = await probePw(pwSpec((req.body ?? {}) as PwLibraryInput), key)
    return { ok: p.ok, steps: p.steps, gufiBin: p.gufiBin, gufiVersion: p.gufiVersion, directories: p.directories }
  })
  app.post('/api/user-libraries/pw', async req => publicUserLibrary(await addFromPw(viewer(req), (req.body ?? {}) as PwLibraryInput)))
  app.post('/api/user-libraries/:id/refresh', async req => publicUserLibrary(await refreshUserLibrary(viewer(req), (req.params as { id: string }).id)))
  app.delete('/api/user-libraries/:id', async req => { removeUserLibrary(viewer(req), (req.params as { id: string }).id); return { ok: true } })
}
