import { execFile } from 'node:child_process'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { EXCLUDE_DIRS, GUFI_BIN, GUFI_INDEX, KB_ROOT, gufiAvailable, PYTHON_BIN } from './config.js'
import { KbError, resolveKb } from './kb.js'
import { reindexForDir, reindexForFile } from './indexing.js'
import { identify, inodeOf, overlayAdopt, overlayByInode, overlayEmpty, overlayEntries, overlayFlush, overlayGet, overlayNotePath, overlayPrune, overlayRekey, overlaySet, sameObject } from './tagsOverlay.js'

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

/** True when the filesystem accepted the xattr; false sends the caller to
 *  the overlay (NFS/EFS refuse user xattrs with ENOTSUP). */
async function writeTags(abs: string, tags: string[]): Promise<boolean> {
  if (tags.length === 0) {
    await runBin('setfattr', ['-x', TAG_XATTR, abs])
    return true
  }
  const { code } = await runBin('setfattr', ['-n', TAG_XATTR, '-v', tags.join(','), abs])
  return code === 0
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
  if (!overlayEmpty()) {
    for (const abs of absPaths) {
      const rel = path.relative(KB_ROOT, abs)
      const ov = overlayGet(rel)
      if (ov) out.set(abs, ov)
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
  // Overlay entries win over (possibly stale) index rows, and make labels
  // work even before a reindex has run or when GUFI is absent.
  if (!overlayEmpty()) {
    for (const e of Object.values(overlayEntries())) {
      ;(e.dir ? dirTags : fileTags).set(e.path, e.tags)
    }
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

/** Apply or remove tags; shared by the REST route and the chat tool. */
export async function applyTagsCore(rawPaths: string[], addRaw: string[], removeRaw: string[]): Promise<Record<string, string[]>> {
  const paths = rawPaths.slice(0, 1000)
  const add = addRaw.map(normalizeTag).filter(Boolean)
  const remove = removeRaw.map(normalizeTag).filter(Boolean)
  if (!paths.length || (!add.length && !remove.length)) throw new KbError(400, 'paths and add or remove required')

  const updated: Record<string, string[]> = {}
  const touched = new Set<string>()
  for (const rel of paths) {
    const cleaned = rel.replace(/^\/+|\/+$/g, '')
    for (const part of cleaned.split('/')) {
      if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, `not taggable: ${cleaned}`)
    }
    const abs = resolveKb(cleaned)
    const current = overlayGet(cleaned) ?? await readTags(abs)
    const next = [...new Set([...current.filter(t => !remove.includes(t)), ...add])]
    const onFs = await writeTags(abs, next)
    overlaySet(cleaned, onFs ? [] : next)
    updated[cleaned] = next
    touched.add(cleaned)
  }
  // Content did not change, so a subtree reindex (with re-embedding) is
  // wasted minutes; upsert the xattr rows in the existing index dbs instead.
  // Any path where that fails falls back to a real reindex.
  const failed = await upsertIndexTags(paths.map(rel => rel.replace(/^\/+|\/+$/g, '')), updated)
  for (const rel of failed) {
    // Best effort: the label is already durable (xattr or overlay); a
    // reindex failure (no GUFI yet) must not fail the apply.
    const abs = resolveKb(rel)
    const isDir = await import('node:fs/promises').then(m => m.stat(abs)).then(s => s.isDirectory()).catch(() => false)
    await (isDir ? reindexForDir(rel) : reindexForFile(rel)).catch(() => {})
  }
  invalidateTagMaps()
  return updated
}

/**
 * Re-attach overlay labels to a rebuilt index by joining on inode, which is
 * how GUFI expects information that a tree walk cannot recover to be
 * restored. Asking the index where each inode lives now (rather than
 * trusting a stored path) means renames and moves survive a reindex, and a
 * full pass prunes labels whose file is gone from both the index and disk.
 */
export async function reapplyTagOverlay(prefix = ''): Promise<number> {
  if (overlayEmpty() || !gufiAvailable()) return 0
  const byInode = overlayByInode()
  const entries = overlayEntries()

  const rows: { rel: string; inode: string; dir: boolean }[] = []
  const dirs = await gufiRows(
    'SELECT rpath(sname, sroll), CAST(inode AS TEXT) FROM vrsummary;',
  ).catch(() => [])
  for (const [p, ino] of dirs) rows.push({ rel: relOf(p), inode: ino, dir: true })
  const files = await gufiRows(
    "SELECT rpath(sname, sroll)||'/'||name, CAST(inode AS TEXT) FROM vrpentries;",
  ).catch(() => [])
  for (const [p, ino] of files) rows.push({ rel: relOf(p), inode: ino, dir: false })

  const scope = prefix.replace(/^\/+|\/+$/g, '')
  const updated: Record<string, string[]> = {}
  const seen = new Set<string>()
  let pathsMoved = false
  for (const row of rows) {
    const keys = byInode.get(row.inode)
    if (!keys) continue
    // The index reports inodes only; stat confirms both the fsid (a corpus
    // spanning devices must not cross-apply labels) and the creation time
    // (a recycled inode is a different file).
    const id = identify(path.join(KB_ROOT, row.rel))
    if (!id) continue
    // Exact key first; failing that, the same inode under another device
    // id with the same creation time is the same file on a rebuilt host.
    let key = keys.find(k => k === id.key)
    if (!key) {
      const alt = keys.find(k => inodeOf(k) === inodeOf(id.key) && entries[k] && sameObject(entries[k], id))
      if (alt) { entries[id.key] = entries[alt]; overlayRekey(alt, id.key); key = id.key; pathsMoved = true }
    }
    const entry = key ? entries[key] : undefined
    if (!key || !entry || !sameObject(entry, id)) continue
    seen.add(key)
    if (overlayNotePath(key, row.rel, row.dir)) pathsMoved = true
    if (scope && row.rel !== scope && !row.rel.startsWith(scope + '/')) continue
    updated[row.rel] = entries[key].tags
  }
  if (pathsMoved) overlayFlush()

  const rels = Object.keys(updated)
  const failed = rels.length ? await upsertIndexTags(rels, updated).catch(() => rels) : []
  if (!scope) overlayPrune(seen)
  invalidateTagMaps()
  return rels.length - failed.length
}



/** Write user.studio.tags rows straight into the per-directory index dbs.
 *  Returns the paths that could not be updated in place. */
async function upsertIndexTags(rels: string[], tags: Record<string, string[]>): Promise<string[]> {
  const fsSync = await import('node:fs')
  const pathMod = path
  const jobs: { db: string; inode: string; value: string; rel: string }[] = []
  const failed: string[] = []
  for (const rel of rels) {
    try {
      const abs = resolveKb(rel)
      const st = fsSync.statSync(abs, { bigint: true })
      const isDir = fsSync.statSync(abs).isDirectory()
      const db = isDir
        ? pathMod.join(GUFI_INDEX, rel, 'db.db')
        : pathMod.join(GUFI_INDEX, pathMod.dirname(rel) === '.' ? '' : pathMod.dirname(rel), 'db.db')
      if (!fsSync.existsSync(db)) { failed.push(rel); continue }
      jobs.push({ db, inode: st.ino.toString(), value: (tags[rel] ?? []).join(','), rel })
    } catch {
      failed.push(rel)
    }
  }
  if (jobs.length) {
    const script = `
import json, sqlite3, sys
jobs = json.load(sys.stdin)
bad = []
for j in jobs:
    try:
        db = sqlite3.connect(j['db'])
        db.execute("DELETE FROM xattrs_pwd WHERE CAST(inode AS TEXT) = ? AND CAST(name AS TEXT) = 'user.studio.tags'", (j['inode'],))
        if j['value']:
            db.execute("INSERT INTO xattrs_pwd (inode, name, value) VALUES (?, 'user.studio.tags', ?)", (j['inode'], j['value']))
        db.commit()
        db.close()
    except Exception:
        bad.append(j['rel'])
print(json.dumps(bad))
`
    const out = await new Promise<string>((resolve, reject) => {
      const child = execFile(PYTHON_BIN, ['-c', script], { timeout: 60_000 },
        (err, so) => (err ? reject(err) : resolve(so)))
      child.stdin?.write(JSON.stringify(jobs))
      child.stdin?.end()
    }).catch(() => JSON.stringify(jobs.map(j => j.rel)))
    try { failed.push(...JSON.parse(out)) } catch { failed.push(...jobs.map(j => j.rel)) }
  }
  return failed
}

/**
 * Rebuild overlay entries from the label rows the index still carries.
 * The index is written from the overlay, so normally this finds nothing
 * new; after the overlay has been lost it is the only copy left.
 */
export async function recoverOverlayFromIndex(): Promise<number> {
  if (!gufiAvailable()) return 0
  const dirs = await gufiRows(
    `SELECT rpath(sname, sroll), CAST(x.value AS TEXT) FROM vrsummary JOIN xattrs_pwd x ON vrsummary.inode = x.inode WHERE CAST(x.name AS TEXT) = '${TAG_XATTR}';`,
  ).catch(() => [])
  const files = await gufiRows(
    `SELECT rpath(sname, sroll)||'/'||v.name, CAST(x.value AS TEXT) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = '${TAG_XATTR}';`,
  ).catch(() => [])
  let adopted = 0
  for (const [p, value] of [...dirs, ...files]) {
    const tags = String(value ?? '').split(/[,\s]+/).map(normalizeTag).filter(Boolean)
    if (tags.length && overlayAdopt(relOf(p), tags)) adopted++
  }
  if (adopted) invalidateTagMaps()
  return adopted
}

export async function tagRoutes(app: FastifyInstance): Promise<void> {
  // An empty overlay beside an index that still carries labels is a lost
  // overlay, not a corpus without labels; recover once the index is up.
  const t = setTimeout(() => {
    if (!overlayEmpty()) return
    recoverOverlayFromIndex()
      .then(n => { if (n) app.log.warn({ adopted: n }, 'label overlay was empty; entries recovered from the index') })
      .catch(() => {})
  }, 20_000)
  t.unref?.()

  app.post('/api/kb/tags/recover', async () => ({ adopted: await recoverOverlayFromIndex() }))

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
    return { updated: await applyTagsCore(body.paths ?? [], body.add ?? [], body.remove ?? []) }
  })
}
