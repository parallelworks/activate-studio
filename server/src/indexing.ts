import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { EXCLUDE_DIRS, GUFI_BIN, GUFI_INDEX, INDEX_BASE, KB_ROOT, PROJECT_ROOT } from './config.js'
import { invalidateDbList } from './gufi.js'
import { invalidateContext } from './chat/context.js'

const SKIP_FILE = path.join(INDEX_BASE, 'skip-dirs.txt')
const SWEEP_STATE = path.join(INDEX_BASE, 'sweep-state.json')
const EMBED_MODEL = path.join(INDEX_BASE, 'models', 'minilm384.gguf')

export interface IndexStatus {
  running: boolean
  lastSweepAt: string | null
  lastSweepMs: number | null
  lastChanges: string[]
  lastError: string | null
  sweepIntervalSec: number
}

const status: IndexStatus = {
  running: false,
  lastSweepAt: null,
  lastSweepMs: null,
  lastChanges: [],
  lastError: null,
  sweepIntervalSec: Number(process.env.SWEEP_INTERVAL_SEC ?? 300),
}

export function indexStatus(): IndexStatus { return { ...status } }

function run(cmd: string, args: string[], timeoutMs = 10 * 60_000): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(cmd, args, { timeout: timeoutMs, maxBuffer: 32 * 1024 * 1024 }, (err, so, se) =>
      err ? reject(new Error(`${path.basename(cmd)}: ${se || err.message}`)) : resolve(so))
  })
}

function ensureSkipFile(): string {
  if (!fs.existsSync(SKIP_FILE)) {
    fs.mkdirSync(INDEX_BASE, { recursive: true })
    fs.writeFileSync(SKIP_FILE, [...EXCLUDE_DIRS, '.claude', '.github'].join('\n') + '\n')
  }
  return SKIP_FILE
}

let indexLock: Promise<unknown> = Promise.resolve()
/** Serialize all index mutations; concurrent uploads and sweeps queue up. */
function withLock<T>(fn: () => Promise<T>): Promise<T> {
  const next = indexLock.then(fn, fn)
  indexLock = next.catch(() => {})
  return next
}

/**
 * Re-index a single KB subtree in seconds: gufi_dir2index into a staging
 * area, swap the matching subtree of the live index, then run enrichment
 * and embeddings restricted to that subtree.
 */
export function incrementalIndexDir(rel: string): Promise<{ ms: number }> {
  return withLock(async () => {
    const t0 = Date.now()
    const cleaned = rel.replace(/^\/+|\/+$/g, '')
    if (!cleaned) throw new Error('use a full rebuild for the root')
    const src = path.join(KB_ROOT, cleaned)
    if (!path.resolve(src).startsWith(KB_ROOT + path.sep)) throw new Error('path escapes the knowledge base')
    if (!fs.existsSync(src) || !fs.statSync(src).isDirectory()) throw new Error(`not a directory: ${cleaned}`)

    const staging = path.join(INDEX_BASE, `inc-${Date.now()}-${Math.floor(Math.random() * 1e6)}`)
    await fsp.mkdir(staging, { recursive: true })
    try {
      await run(path.join(GUFI_BIN, 'gufi_dir2index'),
        ['-x', '-n', '8', '--skip-file', ensureSkipFile(), src, staging])
      const built = path.join(staging, path.basename(src))
      const target = path.join(GUFI_INDEX, cleaned)
      await fsp.mkdir(path.dirname(target), { recursive: true })
      await fsp.rm(target, { recursive: true, force: true })
      await fsp.rename(built, target)

      await run('python3', [path.join(PROJECT_ROOT, 'indexer', 'enrich.py'),
        '--kb-root', KB_ROOT, '--index', GUFI_INDEX,
        '--extract-cache', path.join(INDEX_BASE, 'extract'), '--subdir', cleaned])
      if (fs.existsSync(EMBED_MODEL)) {
        await run('python3', [path.join(PROJECT_ROOT, 'indexer', 'embed.py'),
          '--index', GUFI_INDEX, '--model', EMBED_MODEL, '--subdir', cleaned])
      }
      invalidateDbList()
      invalidateContext()
      return { ms: Date.now() - t0 }
    } finally {
      await fsp.rm(staging, { recursive: true, force: true })
    }
  })
}

/**
 * Rebuild only the root directory's db (files directly under KB_ROOT).
 * gufi_dir2index --max-level 0 emits exactly that one db; enrichment and
 * embeddings run non-recursively on it.
 */
export function indexRootDb(): Promise<{ ms: number }> {
  return withLock(async () => {
    const t0 = Date.now()
    const staging = path.join(INDEX_BASE, `inc-root-${Date.now()}`)
    await fsp.mkdir(staging, { recursive: true })
    try {
      await run(path.join(GUFI_BIN, 'gufi_dir2index'),
        ['-x', '-n', '4', '--max-level', '0', '--skip-file', ensureSkipFile(), KB_ROOT, staging])
      const built = path.join(staging, path.basename(KB_ROOT), 'db.db')
      await fsp.mkdir(GUFI_INDEX, { recursive: true })
      await fsp.rename(built, path.join(GUFI_INDEX, 'db.db'))
      await run('python3', [path.join(PROJECT_ROOT, 'indexer', 'enrich.py'),
        '--kb-root', KB_ROOT, '--index', GUFI_INDEX,
        '--extract-cache', path.join(INDEX_BASE, 'extract'), '--no-recurse'])
      if (fs.existsSync(EMBED_MODEL)) {
        await run('python3', [path.join(PROJECT_ROOT, 'indexer', 'embed.py'),
          '--index', GUFI_INDEX, '--model', EMBED_MODEL, '--no-recurse'])
      }
      invalidateDbList()
      invalidateContext()
      return { ms: Date.now() - t0 }
    } finally {
      await fsp.rm(staging, { recursive: true, force: true })
    }
  })
}

/** Re-index whatever covers a KB-relative FILE path: the file's top-level
 *  subtree, or the root db for files directly under the root. */
export function reindexForFile(rel: string): Promise<{ ms: number }> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  return cleaned.includes('/') ? incrementalIndexDir(cleaned.split('/')[0]) : indexRootDb()
}

/** Re-index whatever covers a KB-relative DIRECTORY: its top-level subtree,
 *  or the root db when the directory is the root itself. */
export function reindexForDir(rel: string): Promise<{ ms: number }> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  return cleaned ? incrementalIndexDir(cleaned.split('/')[0]) : indexRootDb()
}

/** Remove cache entries whose source file no longer exists. Cache layouts
 *  mirror the KB tree with a suffix appended (extract: <rel>.txt keeps the
 *  original extension; pdf-preview: <rel minus extension>.pdf). */
async function pruneCache(cacheRoot: string, kind: '.txt' | '.pdf'): Promise<void> {
  const walk = async (dir: string, rel: string) => {
    let entries
    try { entries = await fsp.readdir(dir, { withFileTypes: true }) } catch { return }
    for (const e of entries) {
      const abs = path.join(dir, e.name)
      const childRel = rel ? `${rel}/${e.name}` : e.name
      if (e.isDirectory()) { await walk(abs, childRel); continue }
      let exists = false
      if (kind === '.txt' && e.name.endsWith('.txt')) {
        exists = fs.existsSync(path.join(KB_ROOT, childRel.slice(0, -4)))
      } else if (kind === '.pdf' && e.name.endsWith('.pdf')) {
        const base = path.join(KB_ROOT, path.dirname(childRel), path.basename(e.name, '.pdf'))
        exists = ['.docx', '.pptx', '.xlsx', '.doc', '.ppt', '.xls', '.odt', '.odp', '.ods']
          .some(ext => fs.existsSync(base + ext))
      } else {
        exists = true
      }
      if (!exists) await fsp.rm(abs, { force: true }).catch(() => {})
    }
  }
  await walk(cacheRoot, '')
}

interface DirScan { latest: number }

/** Latest mtime a directory presents: itself plus its direct files. */
async function scanKbDirs(): Promise<Map<string, DirScan>> {
  const out = new Map<string, DirScan>()
  const walk = async (abs: string, rel: string) => {
    let entries
    try { entries = await fsp.readdir(abs, { withFileTypes: true }) } catch { return }
    let latest = 0
    try { latest = (await fsp.stat(abs)).mtimeMs } catch { /* ignore */ }
    for (const e of entries) {
      if (e.name.startsWith('.')) continue
      const child = path.join(abs, e.name)
      if (e.isDirectory()) {
        if (!EXCLUDE_DIRS.has(e.name)) await walk(child, rel ? `${rel}/${e.name}` : e.name)
      } else {
        try { latest = Math.max(latest, (await fsp.stat(child)).mtimeMs) } catch { /* ignore */ }
      }
    }
    out.set(rel, { latest: Math.floor(latest) })
  }
  await walk(KB_ROOT, '')
  return out
}

function loadSweepState(): Record<string, number> {
  try { return JSON.parse(fs.readFileSync(SWEEP_STATE, 'utf8')) } catch { return {} }
}

/**
 * Find directories whose content is newer than the last sweep and re-index
 * just those subtrees, so files dropped into the KB by any means (scp,
 * generators, git) become searchable within one sweep interval.
 */
export async function sweep(log: (msg: string) => void): Promise<string[]> {
  if (status.running) return []
  status.running = true
  const t0 = Date.now()
  try {
    const state = loadSweepState()
    const scan = await scanKbDirs()

    let rootChanged = false
    let changed: string[] = []
    for (const [rel, s] of scan) {
      if ((state[rel === '' ? '.' : rel] ?? 0) < s.latest) {
        if (rel === '') rootChanged = true
        else changed.push(rel)
      }
    }
    // Removed directories: drop their index subtrees.
    const removed = Object.keys(state).filter(rel => rel && rel !== '.' && !scan.has(rel))
    for (const rel of removed) {
      await fsp.rm(path.join(GUFI_INDEX, rel), { recursive: true, force: true }).catch(() => {})
    }
    // Stale extraction caches for files deleted outside the UI: prune them so
    // a later same-named file cannot reuse another file's text.
    await pruneCache(path.join(INDEX_BASE, 'extract'), '.txt')
    await pruneCache(path.join(INDEX_BASE, 'pdf-preview'), '.pdf')
    // A parent re-index covers its whole subtree; keep only subtree roots.
    changed.sort()
    changed = changed.filter(rel => !changed.some(other => other !== rel && rel.startsWith(other + '/')))

    if (rootChanged) {
      log('sweep: re-indexing root files')
      await indexRootDb()
    }
    for (const rel of changed) {
      log(`sweep: re-indexing ${rel}`)
      await incrementalIndexDir(rel)
    }
    if (rootChanged || changed.length || removed.length) {
      const next: Record<string, number> = {}
      for (const [rel, s] of scan) next[rel === '' ? '.' : rel] = s.latest
      await fsp.writeFile(SWEEP_STATE, JSON.stringify(next))
    }
    status.lastChanges = [...(rootChanged ? ['(root files)'] : []), ...changed, ...removed.map(r => `${r} (removed)`)]
    status.lastError = null
    return changed
  } catch (err: any) {
    status.lastError = String(err?.message ?? err)
    return []
  } finally {
    status.running = false
    status.lastSweepAt = new Date().toISOString()
    status.lastSweepMs = Date.now() - t0
  }
}

/**
 * With no recorded state every directory looks changed, and the first sweep
 * would rebuild the whole index one directory at a time. Assume the index is
 * current when no state exists (full rebuilds handle real drift) and record
 * the present scan without indexing.
 */
async function primeSweepState(): Promise<void> {
  if (fs.existsSync(SWEEP_STATE)) return
  const scan = await scanKbDirs()
  const next: Record<string, number> = {}
  for (const [rel, s] of scan) next[rel === '' ? '.' : rel] = s.latest
  await fsp.writeFile(SWEEP_STATE, JSON.stringify(next))
}

export function startSweepTimer(log: (msg: string) => void): void {
  if (status.sweepIntervalSec <= 0) return
  void primeSweepState().then(() => {
    setInterval(() => { void sweep(log) }, status.sweepIntervalSec * 1000).unref()
  })
}
