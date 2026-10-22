import { createHash } from 'node:crypto'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GATEWAY_BASE, INDEX_BASE, PROJECT_ROOT, PW_CLI, TEXT_SUFFIXES, findPwCli } from './config.js'
import { authEnabled } from './auth.js'
import { gatewayKey } from './chat/gateway.js'
import { resolveUserCred } from './credentials.js'
import { classify, KbError, type FileContent, type KbEntry } from './kb.js'
import { remoteRun, siteName, SSH_DESTINATION, sshSite, type RemoteResult } from './remoteExec.js'
import { noteGufi } from './gufiTrace.js'
import { getJob, startJob, type Job } from './jobs.js'
import { forgetIndexSettings, indexSettings, meaningArgs, saveIndexSettings, type MeaningLevel } from './indexSettings.js'
import { BUILD_CHECK, BUILD_RUN, BUILD_START, BUILD_STATUS, indexerScript, keyValues, parseStatus, PHASE_LABEL } from './remoteBuild.js'

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
 * Over either session the Studio can also build the index itself (`build`):
 * after a check of GUFI and the folders, gufi_dir2index runs on the remote
 * system as a process of its own (remoteBuild.ts), with text extraction
 * where Python is there, and the Studio follows it as an index job until
 * the library is ready. No workflow and no scheduler are involved.
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
  /** Whether the index carries the Studio's full-text and vector tables. */
  fullText?: boolean
  vectors?: boolean
  /** An index the Studio builds on the remote system over the session. */
  build?: RemoteBuild | null
}

/** A build the Studio runs on a remote system (remoteBuild.ts). */
export interface RemoteBuild {
  /** Its state folder there: ~/.activate-studio/builds/<id>. */
  id: string
  /** The folder the index is written in, absolute there. */
  indexDir: string
  /** The folder indexed, absolute there. */
  sourceRoot: string
  /** GUFI's tools there. */
  gufiBin: string
  python: string | null
  model: string | null
  startedAt: string
  progress?: BuildProgress | null
}
export interface BuildProgress { phase: string; done: number; total: number; current: string | null }

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
  // A build on a remote system goes on without the Studio; one not being
  // followed (after a restart, say) is followed again.
  for (const d of loadUserLibraries(owner)) if (d.build && (d.state === 'building' || d.state === 'updating')) follow(owner, d.id)
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
  if (d.build) return rebuildRemote(viewerId, owner, d)
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
export interface SshStep {
  name: 'ssh' | 'gufi' | 'index' | 'source' | 'extract'
  ok: boolean
  detail: string
  /** Passed, with something the user should know (a build without text extraction). */
  warn?: boolean
}
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

/** The host a request names: user@host or an alias, with a port given
 *  apart or written after it (user@host:2222). Nothing here reaches a
 *  shell, but ssh would take a destination starting with "-" as an option. */
function sshDest(a: { destination?: unknown; port?: unknown }): { destination: string; port: number | null } {
  let destination = String(a.destination ?? '').trim()
  let given = a.port
  const m = /^(.+):(\d{1,5})$/.exec(destination)
  if (m && (given === '' || given == null)) { destination = m[1]; given = m[2] }
  if (!SSH_DESTINATION.test(destination)) {
    throw new KbError(400, 'destination: user@host, or a Host alias from your ~/.ssh/config, in letters, digits, dots, dashes, and underscores (for an IPv6 address, use an alias)')
  }
  const port = given === '' || given == null ? null : Number(given)
  if (port != null && !(Number.isInteger(port) && port >= 1 && port <= 65535)) throw new KbError(400, 'port: a number from 1 to 65535')
  return { destination, port }
}

/** A request's fields, checked. */
export function sshSpec(a: SshLibraryInput): SshSpec {
  const { destination, port } = sshDest(a)
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

// The first directory among those given that holds every tool in $1.
const FIND_TOOLS = 'need=$1; shift; for d in "$@"; do ok=1; for t in $need; do { [ -f "$d/$t" ] && [ -x "$d/$t" ]; } || ok=0; done; [ "$ok" = 1 ] && { printf "%s\\n" "$d"; exit 0; }; done; exit 1'

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

type Run = (argv: string[], ms?: number) => Promise<RemoteResult>
const runner = (h: ProbeHost): Run => (argv, ms = 60_000) =>
  remoteRun(h.site, h.key, argv, ms).catch((e: Error) => ({ code: -1, stdout: '', stderr: e.message, raw: Buffer.alloc(0) }))

/** The first step: the session reaches the host. Its system, its home,
 *  and a function that resolves ~ there; null when it failed. */
async function connectStep(h: ProbeHost, run: Run, steps: SshStep[]) {
  const hello = await run(['sh', '-c', 'uname -s; printf "%s\\n" "$HOME"'], 45_000)
  if (hello.code !== 0) {
    steps.push({ name: 'ssh', ok: false, detail: h.advice(hello.stderr || `exit ${hello.code}`) })
    return null
  }
  const [os = '', home = ''] = hello.stdout.trim().split('\n')
  const mac = os === 'Darwin'
  steps.push({ name: 'ssh', ok: true, detail: `Connected to ${h.reached}, a ${mac ? 'macOS' : os || 'Unix'} host.` })
  const expand = (p: string | null) => (p && /^~(?=\/|$)/.test(p) && home.startsWith('/') ? home.replace(/\/+$/, '') + p.slice(1) : p)
  return { os, home, mac, expand }
}

/**
 * The second step: GUFI is there, in the directory given, else on the PATH
 * a non-interactive login has, then where the Studio installs it (newest
 * first, the one its indexes were built with), then the usual places; and
 * gufi_query runs. A build needs gufi_dir2index beside it.
 */
async function gufiStep(h: ProbeHost, run: Run, steps: SshStep[], at: { given: string | null; home: string; mac: boolean }, build = false) {
  const dest = h.name
  const need = build ? 'gufi_query gufi_dir2index' : 'gufi_query'
  const search = at.given ? FIND_TOOLS : `need=$1; shift; c=$(command -v gufi_query 2>/dev/null); s=$(ls -dt "$HOME"/.activate-studio/gufi/*/bin 2>/dev/null); set -- $s "$@"; case "$c" in /*) set -- "\${c%/*}" "$@" ;; esac; set -- "$need" "$@"; ${FIND_TOOLS}`
  const found = await run(['sh', '-c', search, '_', need, ...(at.given ? [at.given] : ['/usr/local/bin', `${at.home}/usr/local/bin`, '/opt/gufi/bin'])])
  const bin = found.code === 0 ? lastLine(found.stdout) : ''
  let version = ''
  if (!bin.startsWith('/')) {
    const tools = build ? 'gufi_dir2index and gufi_query' : 'gufi_query'
    steps.push({ name: 'gufi', ok: false, detail: build
      ? `GUFI is not installed on ${dest}: ${at.given ? `there is no ${tools} in ${at.given}` : `${tools} were not found on the PATH an ssh login gets, nor in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, or /opt/gufi/bin`}. Install GUFI there (github.com/mar-file-system/GUFI), or give the directory that holds its tools as the GUFI bin directory.`
      : at.given
        ? `There is no gufi_query in ${at.given} on ${dest}. Give the directory that holds it, such as /usr/local/bin.`
        : `gufi_query was not found on ${dest}: not on the PATH an ssh login gets, nor in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, or /opt/gufi/bin. Install GUFI there, or give the directory that holds gufi_query as the GUFI bin directory.` })
    return { bin: '', version: '', ran: false }
  }
  const v = await run([`${bin}/gufi_query`, '--version'])
  version = v.code === 0 ? lastLine(v.stdout).slice(0, 80) : ''
  steps.push(v.code === 0
    ? { name: 'gufi', ok: true, detail: `gufi_query ${version || '(no version)'} in ${bin}.` }
    : { name: 'gufi', ok: false, detail: `gufi_query in ${bin} did not run: ${lastLine(v.stderr) || `exit ${v.code}`}.${at.mac ? ` On macOS its libraries are expected in ${path.posix.dirname(bin)}/lib.` : ''}` })
  return { bin, version, ran: v.code === 0 }
}

/**
 * The connection test, step by step: ssh (or pw ssh) reaches the host;
 * gufi_query is there and runs; the index directory is a GUFI index,
 * counted with one aggregate gufi_query; the source directory, when given,
 * is readable. Each step says what it found or what to change.
 */
async function probeIndex(h: ProbeHost, spec: { indexRoot: string; gufiBin: string | null; sourceRoot: string | null }): Promise<SshProbe> {
  const dest = h.name
  const steps: SshStep[] = []
  const done = (extra: Partial<SshProbe> = {}): SshProbe => ({ ok: steps.every(s => s.ok), steps, ...extra })
  const run = runner(h)

  const c = await connectStep(h, run, steps)
  if (!c) return done()
  const { os, home, mac, expand } = c
  const g = await gufiStep(h, run, steps, { given: expand(spec.gufiBin), home, mac })
  const bin = g.bin, version = g.version

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

// ---- building an index on the remote system ----

export interface BuildSpec { sourceRoot: string; indexRoot: string | null; gufiBin: string | null }
export interface BuildProbe extends SshProbe {
  /** The folders, absolute there: what is indexed, and where the index is written. */
  sourceRoot?: string
  indexDir?: string
  /** Folders that do not exist yet and are made when the build starts. */
  create?: string[]
  python?: string | null
  model?: string | null
}

const inside = (child: string, parent: string) => child === parent || child.startsWith(parent.replace(/\/+$/, '') + '/')

/** Where the index goes when no folder is given: under the login's own
 *  ~/.activate-studio, named after the folder, as the index workflow does. */
export function defaultIndexDir(home: string, source: string): string {
  const name = (path.posix.basename(source) || 'root').replace(/[^A-Za-z0-9._-]/g, '_').slice(0, 60)
  return `${home.replace(/\/+$/, '')}/.activate-studio/indexes/${name}-${createHash('sha256').update(source).digest('hex').slice(0, 8)}`
}

/**
 * The check before a build, step by step: the session reaches the host;
 * GUFI's gufi_dir2index and gufi_query are there; the source folder is
 * readable, or can be made; the index folder is new, empty, or one the
 * Studio wrote, outside the source folder, and can be made; and whether
 * text extraction can run there (Python 3.10 or later with SQLite's
 * full-text module), which only adds a note when it cannot.
 */
async function probeBuild(h: ProbeHost, spec: BuildSpec): Promise<BuildProbe> {
  const dest = h.name
  const steps: SshStep[] = []
  const run = runner(h)
  const done = (extra: Partial<BuildProbe> = {}): BuildProbe => ({ ok: steps.every(s => s.ok), steps, ...extra })
  const c = await connectStep(h, run, steps)
  if (!c) return done()
  const g = await gufiStep(h, run, steps, { given: c.expand(spec.gufiBin), home: c.home, mac: c.mac }, true)
  if (!g.ran) return done({ os: c.os })

  const source = c.expand(spec.sourceRoot)!
  const indexDir = c.expand(spec.indexRoot) ?? defaultIndexDir(c.home, source)
  if (!source.startsWith('/') || !indexDir.startsWith('/')) {
    steps.push({ name: 'source', ok: false, detail: `The home folder of the login on ${dest} could not be read, so ~ cannot be resolved; give absolute paths.` })
    return done({ os: c.os })
  }
  const kv = keyValues((await run(['sh', '-c', BUILD_CHECK, '_', source, indexDir])).stdout)
  const create: string[] = []
  const src = kv.source_real || path.posix.normalize(source)
  const idx = kv.index_real || path.posix.normalize(indexDir)

  if (src === '/') steps.push({ name: 'source', ok: false, detail: 'Indexing / is not supported; choose a folder.' })
  else if (kv.source === 'ok') steps.push({ name: 'source', ok: true, detail: `${source} is readable.` })
  else if (kv.source === 'create') { create.push(source); steps.push({ name: 'source', ok: true, detail: `${source} does not exist yet; it is created, empty, when the build starts.` }) }
  else steps.push({ name: 'source', ok: false, detail:
    kv.source === 'unreadable' ? `${source} is there, but the login on ${dest} cannot read it.`
    : kv.source === 'notdir' ? `${source} is a file, not a folder.`
    : kv.source === 'nocreate' ? `${source} does not exist, and it cannot be made: ${kv.source_at || 'its parent'} is not writable by the login.`
    : `The source folder could not be checked on ${dest}.` })

  if (inside(idx, src)) steps.push({ name: 'index', ok: false, detail: `The index folder ${indexDir} is inside the folder being indexed. Choose an index folder outside it.` })
  else if (inside(src, idx)) steps.push({ name: 'index', ok: false, detail: `${source} is inside the index folder ${indexDir}. Choose an index folder outside it.` })
  else if (kv.index === 'empty') steps.push({ name: 'index', ok: true, detail: `The index is written in ${indexDir}.` })
  else if (kv.index === 'studio') {
    const other = kv.index_source && kv.index_source !== src ? kv.index_source : ''
    steps.push(other
      ? { name: 'index', ok: false, detail: `${indexDir} holds the index of ${other}. Choose another index folder.` }
      : { name: 'index', ok: true, detail: `${indexDir} holds an index the Studio built; the new one replaces it when complete.` })
  } else if (kv.index === 'create') {
    // A folder the user named is made only once they confirm; the default
    // is the Studio's own, under ~/.activate-studio.
    if (spec.indexRoot) create.push(indexDir)
    steps.push({ name: 'index', ok: true, detail: spec.indexRoot ? `${indexDir} does not exist yet; it is created when the build starts.` : `The index is written in ${indexDir}.` })
  }
  else steps.push({ name: 'index', ok: false, detail:
    kv.index === 'foreign' ? `${indexDir} already holds files the Studio did not write. Choose an empty or new folder for the index.`
    : kv.index === 'readonly' ? `${indexDir} is not writable by the login on ${dest}.`
    : kv.index === 'notdir' ? `${indexDir} is a file, not a folder.`
    : kv.index === 'nocreate' ? `${indexDir} does not exist, and it cannot be made: ${kv.index_at || 'its parent'} is not writable by the login.`
    : `The index folder could not be checked on ${dest}.` })

  const python = kv.python && kv.python.startsWith('/') ? kv.python : null
  const model = python && kv.model && kv.model.startsWith('/') ? kv.model : null
  steps.push(python
    ? { name: 'extract', ok: true, detail: `Text is extracted there with Python ${kv.python_version || '3'}, so search covers what the files say${model ? ', and embeddings are made with the model in ~/.activate-studio/models' : ''}.` }
    : { name: 'extract', ok: true, warn: true, detail: `No Python 3.10 or later with SQLite's full-text module on ${dest}, so the index has names and metadata only.` })
  return done({ os: c.os, gufiBin: g.bin, gufiVersion: g.version || undefined, sourceRoot: source, indexDir, create, python, model })
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

/** Whether the Studio finds the pw CLI on this machine (findPwCli). */
export function pwCliFound(): boolean {
  return findPwCli().path !== null
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

/** Why a viewer cannot use pw ssh, in plain words, or null when they can:
 *  the Studio does not find the pw CLI, or there is no platform sign-in to
 *  act with. Settings, Libraries shows it beside the choice. */
export function pwUnavailable(viewerId: string | null | undefined): string | null {
  const owner = ownerOf(viewerId)
  if (!owner) return 'Sign in to add your own libraries.'
  if (!pwCliFound()) {
    const set = process.env.PW_CLI?.includes('/') ? ` PW_CLI names ${process.env.PW_CLI}, which is not there.` : ''
    return `The Studio does not find the pw CLI on this computer.${set} Install it, or choose where it is under Settings, Model access, pw CLI.`
  }
  if (!keyOf(owner)) {
    return owner === 'single'
      ? 'The pw CLI on this computer is not signed in to a platform. Sign it in under Settings, Model access.'
      : 'Systems on the platform are reached with your own platform key: add your ACTIVATE API key under Settings, Model access.'
  }
  return null
}

/** Whether a viewer can connect an index on a cluster: the pw CLI is here
 *  and there is a key to act with. */
export function pwAllowed(viewerId: string | null | undefined): boolean {
  return pwUnavailable(viewerId) === null
}

/** Whether pw ssh is offered as a choice at all: wherever it can be used,
 *  and on a one-person Studio always, with the reason when it cannot. */
export function pwOffered(viewerId: string | null | undefined): boolean {
  return pwAllowed(viewerId) || sshAllowed(viewerId)
}

export interface PwSystem {
  /** As pw ssh names it. */
  name: string
  /** The platform's display name, when it has one. */
  label: string
  /** The platform's status: active when running, off when stopped. */
  status: string
  running: boolean
  type: string | null
}

/** The viewer's systems on the platform, with their own key, running ones
 *  first. Names pw ssh cannot take are left out. */
export async function listPwSystems(viewerId: string | null | undefined): Promise<PwSystem[]> {
  const { key } = pwKey(viewerId)
  const parsed = firstJson<unknown>(await pw(['clusters', 'ls', '-o', 'json'], key, 60_000))
  const rows = (Array.isArray(parsed) ? parsed : (parsed as { clusters?: unknown[] } | null)?.clusters ?? []) as
    { name?: unknown; displayName?: unknown; status?: unknown; type?: unknown }[]
  const out: PwSystem[] = []
  for (const r of rows) {
    const name = String(r?.name ?? '')
    if (!CLUSTER.test(name)) continue
    const status = String(r?.status ?? '').toLowerCase() || 'unknown'
    out.push({ name, label: String(r?.displayName || name), status, running: status === 'active', type: r?.type ? String(r.type) : null })
  }
  return out.sort((a, b) => Number(b.running) - Number(a.running) || a.label.localeCompare(b.label))
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
  const label = (String(a.label ?? '').trim() || name).slice(0, 60)
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
  const label = (String(a.label ?? '').trim() || name).slice(0, 60)
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

// ---- building on the remote system, over ssh or pw ssh ----

export interface BuildInput { label?: unknown; destination?: unknown; port?: unknown; cluster?: unknown; sourceRoot?: unknown; indexRoot?: unknown; gufiBin?: unknown; create?: unknown; meaning?: unknown }
type Transport = 'ssh' | 'pw'

/** The host a build request names, as the probe reaches it, and its folders. */
function buildRequest(viewerId: string | null | undefined, transport: Transport, a: BuildInput) {
  let owner: string, host: ProbeHost, conn: { destination: string | null; port: number | null; site: string }
  if (transport === 'ssh') {
    if (!sshAllowed(viewerId)) throw new KbError(403, SSH_SINGLE_USER)
    owner = ownerOf(viewerId)!
    const { destination, port } = sshDest(a)
    const spec = { destination, port, indexRoot: '', gufiBin: null, sourceRoot: null }
    host = { site: sshSite(destination, port), key: '', name: destination, reached: `${destination}${port ? ` on port ${port}` : ''}`, advice: m => sshAdvice(m, spec) }
    conn = { destination, port, site: destination }
  } else {
    const k = pwKey(viewerId)
    owner = k.owner
    const cluster = String(a.cluster ?? '').trim()
    if (!CLUSTER.test(cluster)) throw new KbError(400, 'system: the name of one of your running clusters')
    host = { site: cluster, key: k.key, name: cluster, reached: cluster, advice: m => pwAdvice(m, cluster) }
    conn = { destination: null, port: null, site: cluster }
  }
  const sourceRoot = remotePath(a.sourceRoot, 'source folder')
  if (!sourceRoot) throw new KbError(400, 'source folder: the folder to index on that system, absolute or under ~')
  const spec: BuildSpec = { sourceRoot, indexRoot: remotePath(a.indexRoot, 'index folder'), gufiBin: remotePath(a.gufiBin, 'GUFI bin directory') }
  return { owner, host, conn, spec }
}

/** The check before a build, as the Add a library form shows it. */
export async function testBuild(viewerId: string | null | undefined, transport: Transport, a: BuildInput): Promise<BuildProbe> {
  const r = buildRequest(viewerId, transport, a)
  return probeBuild(r.host, r.spec)
}

let buildPollMs = 3000
/** Tests follow builds faster. */
export function setBuildPollMs(ms: number): void { buildPollMs = ms }
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
const newBuildId = (id: string) => `${id}-${Date.now().toString(36)}`
const ABS_ANY = /^\/[^\0-\x1f\x7f]*$/

/** Writes the build to the remote system and starts it there, with the
 *  library's level of search by meaning (off: no embeddings are made). */
async function startRemote(site: string, key: string, b: RemoteBuild, create: boolean, meaning: MeaningLevel): Promise<void> {
  const level = meaningArgs(meaning)
  const enrich = b.python ? indexerScript('enrich.py') : ''
  const embed = b.python && b.model && level ? indexerScript('embed.py') : ''
  const r = await remoteRun(site, key, ['bash', '-c', BUILD_START, '_', b.id, BUILD_RUN, enrich, embed,
    b.gufiBin, b.sourceRoot, b.indexDir, '4', b.python ?? '', b.model ?? '', create ? '1' : '0', (level ?? []).join(' ')], 60_000)
  if (r.code !== 0 || !/started/.test(r.stdout)) throw new KbError(502, `The build could not be started on ${siteName(site)}: ${lastLine(r.stderr) || `exit ${r.code}`}`)
}

/** The session a library's commands go over, and the key they act with. */
function sessionOf(owner: string, d: UserLibraryDef): { site: string; key: string } {
  if (d.transport === 'ssh') {
    if (owner !== 'single' || authEnabled()) throw new KbError(403, SSH_SINGLE_USER)
    return { site: sshSite(String(d.destination ?? ''), d.port ?? null), key: '' }
  }
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'This library is reached with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  return { site: d.site, key }
}

/** Starts an index on a remote system over the session, after the check
 *  passes here again; folders that do not exist are made only when the
 *  request says to (the form asks first). Followed as an index job. */
export async function addBuild(viewerId: string | null | undefined, transport: Transport, a: BuildInput): Promise<UserLibraryDef & { job?: number }> {
  const r = buildRequest(viewerId, transport, a)
  const p = await probeBuild(r.host, r.spec)
  if (!p.ok) throw new KbError(400, `The check did not pass. ${p.steps.find(s => !s.ok)?.detail ?? ''}`.trim())
  if (p.create?.length && a.create !== true) throw new KbError(409, `These folders do not exist yet: ${p.create.join(', ')}. Confirm to create them.`)
  const defs = loadUserLibraries(r.owner)
  const same = defs.find(d => d.build && d.transport === transport && d.site === r.conn.site && (d.port ?? null) === r.conn.port && d.build.indexDir === p.indexDir)
  if (same) throw new KbError(409, `This index folder is already used by ${same.label}.`)
  const name = path.posix.basename(p.sourceRoot!) || 'folder'
  const label = (String(a.label ?? '').trim() || name).slice(0, 60)
  const base = `u-${`${r.conn.site.replace(/^.*@/, '')}-${name}`.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 30) || 'library'}`
  let id = base
  for (let i = 2; defs.some(d => d.id === id); i++) id = `${base}-${i}`
  const build: RemoteBuild = {
    id: newBuildId(id), indexDir: p.indexDir!, sourceRoot: p.sourceRoot!, gufiBin: p.gufiBin!, python: p.python ?? null, model: p.model ?? null,
    startedAt: new Date().toISOString(), progress: { phase: PHASE_LABEL.prepare, done: 0, total: 0, current: null },
  }
  if (a.meaning !== undefined) await saveIndexSettings(id, { meaning: a.meaning })
  await startRemote(r.host.site, r.host.key, build, !!p.create?.includes(build.sourceRoot), indexSettings(id).meaning)
  const def: UserLibraryDef = {
    id, label, transport, existing: false, site: r.conn.site, destination: r.conn.destination, port: r.conn.port, os: p.os || null, gufiVersion: p.gufiVersion ?? null,
    resource: transport === 'ssh' ? r.host.site : r.conn.site, sourceDir: build.sourceRoot, sourceRoot: null, indexRoot: null, gufiBin: null,
    state: 'building', run: null, error: null, builtAt: null, directories: null, files: null, scheduler: null, build,
  }
  saveUserLibraries(r.owner, [...defs, def])
  return { ...def, job: follow(r.owner, id).id }
}

/** Builds a library's index again, beside the one there, which is
 *  searched until the new one replaces it. */
async function rebuildRemote(viewerId: string | null | undefined, owner: string, d: UserLibraryDef): Promise<UserLibraryDef> {
  if (d.state === 'building' || d.state === 'updating') return d
  if (d.transport === 'ssh' && !sshAllowed(viewerId)) throw new KbError(403, SSH_SINGLE_USER)
  const { site, key } = sessionOf(owner, d)
  const build: RemoteBuild = { ...d.build!, id: newBuildId(d.id), startedAt: new Date().toISOString(), progress: { phase: PHASE_LABEL.prepare, done: 0, total: 0, current: null } }
  await startRemote(site, key, build, false, indexSettings(d.id).meaning)
  const next = updateDef(owner, d.id, { state: d.indexRoot ? 'updating' : 'building', error: null, build })!
  follow(owner, d.id)
  return next
}

/** What a finished build left: the library is ready on its new index, or
 *  says why not (keeping an index it already had). */
function finishBuild(owner: string, d: UserLibraryDef, st: ReturnType<typeof parseStatus>): boolean {
  const b = d.build!
  const m = st.manifest
  if (!(st.state === 'finished' && st.exit === 0 && m && typeof m.index === 'string' && ABS_ANY.test(m.index))) {
    const error = st.state === 'missing' ? `the build's state folder is gone from ${siteName(d.resource)}`
      : st.error || (st.exit ? `the build ended with status ${st.exit}` : 'the build wrote no index summary')
    updateDef(owner, d.id, { state: d.indexRoot ? 'ready' : 'failed', error, build: { ...b, progress: null } })
    return false
  }
  const source = typeof m.source === 'string' && ABS_ANY.test(m.source) ? m.source : b.sourceRoot
  const gufiBin = typeof m.gufiBin === 'string' && ABS_ANY.test(m.gufiBin) ? m.gufiBin : b.gufiBin
  updateDef(owner, d.id, {
    state: 'ready', error: null, indexRoot: m.index, sourceRoot: source, sourceDir: source, gufiBin,
    builtAt: typeof m.builtAt === 'string' ? m.builtAt : new Date().toISOString(),
    directories: Number(m.directories) || 0, files: Number(m.files) || 0, fullText: m.fullText === true, vectors: m.vectors === true,
    build: { ...b, progress: null },
  })
  return true
}

const followers = new Map<string, number>()

/**
 * Follows a library's build on its remote system as a background job
 * (jobs.ts, GET /api/jobs/<id>): reads its state every few seconds, keeps
 * the phase and folder count on the job and on the library, and when it
 * ends, makes the library ready or says why not. A build already followed
 * keeps its job. The build runs on the remote system, so a restart here
 * loses nothing and an update need not wait for it; the next listing
 * follows it again.
 */
function follow(owner: string, id: string): Job {
  const k = `${owner}|${id}`
  const prev = followers.get(k)
  const was = prev != null ? getJob(prev) : null
  if (was && was.state === 'running') return was
  const job = startJob('index', 0, async h => {
    const t0 = Date.now()
    let misses = 0, savedAt = 0, savedPhase = ''
    for (;;) {
      const d = loadUserLibraries(owner).find(x => x.id === id)
      if (!d?.build || (d.state !== 'building' && d.state !== 'updating')) return { ms: Date.now() - t0 }
      const b = d.build
      let st: ReturnType<typeof parseStatus>
      try {
        const { site, key } = sessionOf(owner, d)
        st = parseStatus((await remoteRun(site, key, ['sh', '-c', BUILD_STATUS, '_', b.id, b.indexDir], 60_000)).stdout)
        misses = 0
      } catch (e) {
        // The build goes on there either way; the next listing follows it again.
        if (++misses >= 5) {
          updateDef(owner, id, { error: `The build could not be followed: ${(e as Error).message}` })
          throw e
        }
        await sleep(buildPollMs)
        continue
      }
      if (st.state === 'running') {
        const progress: BuildProgress = { phase: PHASE_LABEL[st.phase] ?? 'Starting', done: st.dirs ?? 0, total: 0, current: st.dirs != null ? `${st.dirs.toLocaleString('en-US')} folders` : null }
        h.progress({ ...progress, current: progress.current ?? '' })
        if (st.phase !== savedPhase || Date.now() - savedAt > 10_000) {
          updateDef(owner, id, { error: null, build: { ...b, progress } })
          savedAt = Date.now(); savedPhase = st.phase
        }
        await sleep(buildPollMs)
        continue
      }
      if (!finishBuild(owner, d, st)) throw new Error(loadUserLibraries(owner).find(x => x.id === id)?.error ?? 'the build failed')
      return { ms: Date.now() - t0 }
    }
  })
  followers.set(k, job.id)
  return job
}

export function removeUserLibrary(viewerId: string | null | undefined, id: string): void {
  const owner = ownerOf(viewerId)
  if (!owner) throw new KbError(404, 'no such library')
  const defs = loadUserLibraries(owner)
  if (!defs.some(d => d.id === id)) throw new KbError(404, 'no such library')
  saveUserLibraries(owner, defs.filter(d => d.id !== id))
  void forgetIndexSettings(id)
}

/** The shape Settings shows: no keys, and the remote paths, which are the user's own. */
export function publicUserLibrary(d: UserLibraryDef & { job?: number }) {
  return { id: d.id, label: d.label, transport: d.transport ?? 'pw', existing: !d.build && (d.transport === 'ssh' || !!d.existing), meaning: d.build ? indexSettings(d.id).meaning : null, site: d.site, destination: d.destination ?? null, port: d.port ?? null, gufiVersion: d.gufiVersion ?? null, sourceDir: d.sourceDir, sourceRoot: d.sourceRoot, indexRoot: d.indexRoot, state: d.state,
    run: d.run ? { slug: d.run.slug, mode: d.run.mode, startedAt: d.run.startedAt } : null, error: d.error, builtAt: d.builtAt,
    directories: d.directories, files: d.files, scheduler: d.scheduler, fullText: !!d.fullText, vectors: !!d.vectors,
    // Built here over the session: where the index is written, and how far a build has got.
    built: !!d.build, indexDir: d.build?.indexDir ?? null, progress: d.build?.progress ?? null, ...(d.job != null ? { job: d.job } : {}) }
}

/** The check before a build, as the form shows it. */
const publicBuildProbe = (p: BuildProbe) => ({ ok: p.ok, steps: p.steps, gufiBin: p.gufiBin, gufiVersion: p.gufiVersion, sourceRoot: p.sourceRoot, indexDir: p.indexDir, create: p.create ?? [] })

/** Whether the index workflow is offered for building on a cluster: on a
 *  Studio the platform hosts. A Studio on someone's own computer builds on
 *  a remote system over ssh or pw ssh instead. */
export function workflowOffered(): boolean {
  return authEnabled() || !!process.env.PW_PLATFORM_HOST
}

export async function userLibraryRoutes(app: FastifyInstance): Promise<void> {
  const viewer = (req: { user?: { id?: string } }) => req.user?.id ?? null
  app.get('/api/user-libraries', async req => ({
    libraries: (await listUserLibraries(viewer(req))).map(d => publicUserLibrary(d)), available: !!ownerOf(viewer(req)), ssh: sshAllowed(viewer(req)), pw: pwAllowed(viewer(req)),
    pwOffered: pwOffered(viewer(req)), pwWhy: pwUnavailable(viewer(req)), workflow: workflowOffered(),
  }))
  // The systems pw ssh can reach, for the choice in the form; why it cannot
  // be used, or why the list could not be had, rather than an error.
  app.get('/api/user-libraries/pw/systems', async req => {
    const why = pwUnavailable(viewer(req))
    if (why) return { usable: false, why, systems: [], error: null }
    try {
      return { usable: true, why: null, systems: await listPwSystems(viewer(req)), error: null }
    } catch (e) {
      return { usable: true, why: null, systems: [], error: String((e as Error).message ?? e) }
    }
  })
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
  // Building an index on the remote system, over either session: the check, then the build.
  for (const t of ['ssh', 'pw'] as const) {
    app.post(`/api/user-libraries/${t}/build/test`, async req => publicBuildProbe(await testBuild(viewer(req), t, (req.body ?? {}) as BuildInput)))
    app.post(`/api/user-libraries/${t}/build`, async req => publicUserLibrary(await addBuild(viewer(req), t, (req.body ?? {}) as BuildInput)))
  }
  app.post('/api/user-libraries/:id/refresh', async req => publicUserLibrary(await refreshUserLibrary(viewer(req), (req.params as { id: string }).id)))
  app.delete('/api/user-libraries/:id', async req => { removeUserLibrary(viewer(req), (req.params as { id: string }).id); return { ok: true } })
}
