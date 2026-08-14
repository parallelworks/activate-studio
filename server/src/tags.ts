import { execFile } from 'node:child_process'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { EXCLUDE_DIRS, GUFI_BIN, GUFI_INDEX, KB_ROOT, gufiAvailable } from './config.js'
import { KbError, resolveKb } from './kb.js'
import { reindexForDir, reindexForFile } from './indexing.js'

export const TAG_XATTR = 'user.studio.tags'
const DELIM = '\x1e'

function runBin(cmd: string, args: string[], timeoutMs = 30_000): Promise<{ stdout: string; code: number }> {
  return new Promise(resolve => {
    execFile(cmd, args, { timeout: timeoutMs, maxBuffer: 16 * 1024 * 1024 },
      (err, so) => resolve({ stdout: so ?? '', code: err ? 1 : 0 }))
  })
}

export function normalizeTag(t: string): string {
  return t.trim().toLowerCase().replace(/[^a-z0-9 _-]/g, '').replace(/\s+/g, '-').slice(0, 40)
}

export async function readTags(abs: string): Promise<string[]> {
  const { stdout, code } = await runBin('getfattr', ['--only-values', '--absolute-names', '-n', TAG_XATTR, abs])
  if (code !== 0) return []
  return stdout.split(',').map(t => t.trim()).filter(Boolean)
}

async function writeTags(abs: string, tags: string[]): Promise<void> {
  if (tags.length === 0) {
    await runBin('setfattr', ['-x', TAG_XATTR, abs])
    return
  }
  const { code } = await runBin('setfattr', ['-n', TAG_XATTR, '-v', tags.join(','), abs])
  if (code !== 0) throw new KbError(500, `could not set tags on ${abs}`)
}

/** Own tags for every entry of a directory, one getfattr process per listing. */
export async function readTagsBatch(absPaths: string[]): Promise<Map<string, string[]>> {
  const out = new Map<string, string[]>()
  if (absPaths.length === 0) return out
  const { stdout } = await runBin('getfattr', ['--absolute-names', '-n', TAG_XATTR, ...absPaths])
  let current = ''
  for (const line of stdout.split('\n')) {
    if (line.startsWith('# file: ')) current = '/' + line.slice(8).replace(/^\/+/, '')
    else if (line.startsWith(TAG_XATTR + '=')) {
      const v = line.slice(TAG_XATTR.length + 1).replace(/^"|"$/g, '')
      out.set(current, v.split(',').map(t => t.trim()).filter(Boolean))
    }
  }
  return out
}

/* ---- corpus-wide tag maps from the GUFI index, cached briefly ---- */

interface TagMaps { dirTags: Map<string, string[]>; fileTags: Map<string, string[]>; at: number }
let cache: TagMaps | null = null
export function invalidateTagMaps(): void { cache = null }

function gufiRows(sql: string): Promise<string[][]> {
  return new Promise((resolve, reject) => {
    execFile(path.join(GUFI_BIN, 'gufi_query'), ['-n', '8', '-d', DELIM, '-E', sql, GUFI_INDEX],
      { timeout: 60_000, maxBuffer: 32 * 1024 * 1024 },
      (err, so) => (err && !so ? reject(new Error(err.message)) : resolve(
        (so ?? '').split('\n').filter(Boolean).map(l => l.split(DELIM)))))
  })
}

const relOf = (p: string): string => {
  const idx = path.resolve(GUFI_INDEX)
  const abs = path.resolve(p)
  return abs.startsWith(idx + path.sep) ? abs.slice(idx.length + 1) : abs === idx ? '' : p
}

export async function tagMaps(): Promise<TagMaps> {
  if (cache && Date.now() - cache.at < 60_000) return cache
  const dirTags = new Map<string, string[]>()
  const fileTags = new Map<string, string[]>()
  if (gufiAvailable()) {
    // GUFI stores xattr names and values as BLOBs; TEXT comparisons are
    // silently false without the casts.
    const dirs = await gufiRows(
      `SELECT rpath(sname, sroll), CAST(x.value AS TEXT) FROM vrsummary JOIN xattrs_pwd x ON vrsummary.inode = x.inode WHERE CAST(x.name AS TEXT) = '${TAG_XATTR}';`,
    ).catch(() => [])
    for (const [p, v] of dirs) dirTags.set(relOf(p), v.split(',').map(t => t.trim()).filter(Boolean))
    const files = await gufiRows(
      `SELECT rpath(sname, sroll)||'/'||v.name, CAST(x.value AS TEXT) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = '${TAG_XATTR}';`,
    ).catch(() => [])
    for (const [p, v] of files) fileTags.set(relOf(p), v.split(',').map(t => t.trim()).filter(Boolean))
  }
  cache = { dirTags, fileTags, at: Date.now() }
  return cache
}

/** Own tags plus every ancestor directory's tags. */
export async function effectiveTags(rel: string): Promise<{ own: string[]; inherited: string[] }> {
  const maps = await tagMaps()
  const own = maps.fileTags.get(rel) ?? maps.dirTags.get(rel) ?? []
  const inherited = new Set<string>()
  const parts = rel.split('/').slice(0, -1)
  for (let i = 0; i <= parts.length; i++) {
    for (const t of maps.dirTags.get(parts.slice(0, i).join('/')) ?? []) inherited.add(t)
  }
  return { own, inherited: [...inherited].filter(t => !own.includes(t)) }
}

/** Annotate search hits with effective tags; optionally require one of `filter`. */
export async function annotateHits<T extends { path: string; tags?: string[] }>(
  hits: T[], filter?: string[],
): Promise<T[]> {
  const maps = await tagMaps()
  const annotated = hits.map(h => {
    const tags = new Set<string>(maps.fileTags.get(h.path) ?? [])
    const parts = h.path.split('/')
    for (let i = 0; i < parts.length; i++) {
      for (const t of maps.dirTags.get(parts.slice(0, i).join('/')) ?? []) tags.add(t)
    }
    return { ...h, tags: [...tags] }
  })
  if (!filter?.length) return annotated
  const want = filter.map(normalizeTag).filter(Boolean)
  return annotated.filter(h => want.some(t => h.tags!.includes(t)))
}

export async function tagRoutes(app: FastifyInstance): Promise<void> {
  // Vocabulary with usage counts, from the index.
  app.get('/api/tags', async () => {
    const maps = await tagMaps()
    const counts = new Map<string, number>()
    for (const tags of [...maps.dirTags.values(), ...maps.fileTags.values()]) {
      for (const t of tags) counts.set(t, (counts.get(t) ?? 0) + 1)
    }
    return { tags: [...counts.entries()].map(([tag, count]) => ({ tag, count })).sort((a, b) => b.count - a.count) }
  })

  app.get('/api/kb/tags', async req => {
    const rel = String((req.query as { path?: string }).path ?? '').replace(/^\/+|\/+$/g, '')
    return effectiveTags(rel)
  })

  // Apply or remove tags on files and directories (multi-select friendly).
  app.post('/api/kb/tags', async req => {
    const body = req.body as { paths?: string[]; add?: string[]; remove?: string[] }
    const paths = (body.paths ?? []).slice(0, 200)
    const add = (body.add ?? []).map(normalizeTag).filter(Boolean)
    const remove = (body.remove ?? []).map(normalizeTag).filter(Boolean)
    if (!paths.length || (!add.length && !remove.length)) throw new KbError(400, 'paths and add or remove required')

    const updated: Record<string, string[]> = {}
    const touched = new Set<string>()
    for (const rel of paths) {
      const cleaned = rel.replace(/^\/+|\/+$/g, '')
      for (const part of cleaned.split('/')) {
        if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, `not taggable: ${cleaned}`)
      }
      const abs = resolveKb(cleaned)
      const current = await readTags(abs)
      const next = [...new Set([...current.filter(t => !remove.includes(t)), ...add])]
      await writeTags(abs, next)
      updated[cleaned] = next
      touched.add(cleaned)
    }
    // Re-index the touched subtrees so the xattrs land in the index.
    const roots = new Set([...touched].map(rel => rel.split('/')[0]))
    for (const root of roots) {
      const abs = resolveKb(root)
      const isDir = await import('node:fs/promises').then(m => m.stat(abs)).then(s => s.isDirectory()).catch(() => false)
      await (isDir ? reindexForDir(root) : reindexForFile(root))
    }
    invalidateTagMaps()
    return { updated }
  })
}
