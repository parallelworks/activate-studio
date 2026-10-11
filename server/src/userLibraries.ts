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
import { remoteRun } from './remoteExec.js'
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
 * A user library's index root, as the rest of the Studio sees it, is an
 * opaque reference ("remote:<owner>:<id>"); gufi.ts hands any query on one
 * to remoteQuery here. Paths and keys never reach the browser.
 */

export interface UserLibraryDef {
  id: string
  label: string
  site: string
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

/** What a remote root resolves to, or null for a local path. */
export function remoteTarget(root: string | undefined | null): { site: string; key: string; gufiBin: string; indexRoot: string; sourceRoot: string | null } | null {
  if (!root || !root.startsWith(REMOTE_PREFIX)) return null
  const [hash, id] = root.slice(REMOTE_PREFIX.length).split(':')
  let owner = ''
  try { owner = fs.readFileSync(path.join(INDEX_BASE, 'users', hash, 'owner'), 'utf8') } catch { throw new KbError(404, 'no such library') }
  const d = loadUserLibraries(owner).find(x => x.id === id)
  if (!d || !d.indexRoot || !d.gufiBin) throw new KbError(409, 'this library is not built yet')
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'This library is read with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  return { site: d.site, key, gufiBin: d.gufiBin, indexRoot: d.indexRoot, sourceRoot: d.sourceRoot }
}

/** Runs gufi_query on the library's cluster, as gufi.ts would locally. */
export async function remoteQuery(root: string, args: string[], subdir?: string): Promise<string> {
  const t = remoteTarget(root)!
  const target = subdir ? path.posix.join(t.indexRoot, cleanRel(subdir)) : t.indexRoot
  const t0 = Date.now()
  const r = await remoteRun(t.site, t.key, [`${t.gufiBin}/gufi_query`, ...args, target])
  noteGufi([`${t.gufiBin}/gufi_query`, ...args, target], { site: t.site, ms: Date.now() - t0 })
  // Like the local runner: a nonzero exit with rows still means rows.
  if (r.code !== 0 && !r.stdout) throw new KbError(502, `gufi_query on ${t.site} failed: ${r.stderr.trim().split('\n').pop() ?? r.code}`)
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
  const script = `"$1"/gufi_query -d "$3" -n 1 --min-level 0 --max-level 0 -E 'SELECT name, type, size, mtime FROM vrpentries;' "$2"; printf '\\n__DIRS__\\n'; "$1"/gufi_query -d "$3" -n 1 --min-level 1 --max-level 1 -S 'SELECT name, mtime FROM vrsummary;' "$2"`
  const r = await remoteRun(t.site, t.key, ['bash', '-c', script, '_', t.gufiBin, target, '\x1e'])
  const [filesPart, dirsPart = ''] = r.stdout.split('\n__DIRS__\n')
  const rows = (s: string) => s.split('\n').filter(Boolean).map(l => l.split('\x1e'))
  const at = (name: string) => (sub ? `${sub}/${name}` : name)
  const dirs: KbEntry[] = rows(dirsPart).filter(x => x[0]).map(x => ({ name: x[0], path: at(x[0]), type: 'dir', size: 0, mtime: Number(x[1]) || 0, kind: 'dir' }))
  const files: KbEntry[] = rows(filesPart).filter(x => x[0] && x[1] !== 'd').map(x => ({ name: x[0], path: at(x[0]), type: 'file', size: Number(x[2]) || 0, mtime: Number(x[3]) || 0, kind: classify(x[0], false) }))
  const byName = (a: KbEntry, b: KbEntry) => a.name.localeCompare(b.name)
  return [...dirs.sort(byName), ...files.sort(byName)]
}

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
  const r = await remoteRun(t.site, t.key, ['bash', '-c', 'stat -c "%s %Y" -- "$1" || exit 3; [ "$2" = 1 ] && head -c "$3" -- "$1"; exit 0', '_', abs, text ? '1' : '0', String(PREVIEW_BYTES + 1)])
  if (r.code !== 0) throw new KbError(404, `${t.site}: ${r.stderr.trim().split('\n').pop() ?? 'cannot read the file'}`)
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
  const r = await remoteRun(t.site, t.key, ['bash', '-c', 's=$(stat -c %s -- "$1") || exit 3; [ "$s" -le "$2" ] || exit 4; cat -- "$1"', '_', abs, String(DOWNLOAD_MAX)], 300_000)
  if (r.code === 4) throw new KbError(413, `This file is over ${DOWNLOAD_MAX / 1024 / 1024} MB; copy it from ${t.site} directly.`)
  if (r.code !== 0) throw new KbError(404, `${t.site}: ${r.stderr.trim().split('\n').pop() ?? 'cannot read the file'}`)
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
  if (d.state === 'building' || d.state === 'updating') return d
  const key = keyOf(owner)
  if (!key) throw new KbError(409, 'add your ACTIVATE API key under Settings, Model access')
  const mode = d.indexRoot ? 'update' : 'build'
  const slug = await launch(d, key, mode)
  return updateDef(owner, id, { state: d.indexRoot ? 'updating' : 'building', error: null, run: { slug, mode, startedAt: new Date().toISOString() } })!
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
  return { id: d.id, label: d.label, site: d.site, sourceDir: d.sourceDir, sourceRoot: d.sourceRoot, indexRoot: d.indexRoot, state: d.state,
    run: d.run ? { slug: d.run.slug, mode: d.run.mode, startedAt: d.run.startedAt } : null, error: d.error, builtAt: d.builtAt,
    directories: d.directories, files: d.files, scheduler: d.scheduler }
}

export async function userLibraryRoutes(app: FastifyInstance): Promise<void> {
  const viewer = (req: { user?: { id?: string } }) => req.user?.id ?? null
  app.get('/api/user-libraries', async req => ({ libraries: (await listUserLibraries(viewer(req))).map(publicUserLibrary), available: !!ownerOf(viewer(req)) }))
  app.post('/api/user-libraries', async req => publicUserLibrary(await addFromCluster(viewer(req), (req.body ?? {}) as AddFromCluster)))
  app.post('/api/user-libraries/:id/refresh', async req => publicUserLibrary(await refreshUserLibrary(viewer(req), (req.params as { id: string }).id)))
  app.delete('/api/user-libraries/:id', async req => { removeUserLibrary(viewer(req), (req.params as { id: string }).id); return { ok: true } })
}
