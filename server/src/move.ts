import fsp from 'node:fs/promises'
import path from 'node:path'
import { EXCLUDE_DIRS } from './config.js'
import { KbError, extractPath, moveCacheEntry, resolveKb } from './kb.js'
import { incrementalIndexDir, indexRootDb } from './indexing.js'
import { modelCachePath } from './model.js'
import { pdfPreviewPaths } from './preview.js'

/**
 * Moving material inside the corpus.
 *
 * A move is a rename plus bookkeeping: the derived caches are keyed by path
 * and travel with the file, and both ends are re-indexed so search and the
 * assistant see the new location. Labels need nothing here; xattr labels
 * belong to the inode a rename preserves, and the overlay is inode-keyed
 * with its stored path refreshed by the re-index.
 */

export interface MoveOutcome {
  moved: { from: string; to: string }[]
  skipped: { path: string; reason: string }[]
  roots: Set<string>
  rootDb: boolean
  indexMs: number
}

export async function movePaths(
  paths: string[],
  destRaw: string,
  onProgress?: (done: number, current: string) => void,
): Promise<MoveOutcome> {
  const dest = destRaw.replace(/^\/+|\/+$/g, '')
  if (!paths.length) throw new KbError(400, 'paths required')
  for (const part of dest.split('/').filter(Boolean)) {
    if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected destination')
  }
  const dstStat = await fsp.stat(resolveKb(dest)).catch(() => null)
  if (!dstStat?.isDirectory()) throw new KbError(400, `not a directory: ${dest || 'root'}`)

  const out: MoveOutcome = { moved: [], skipped: [], roots: new Set(), rootDb: false, indexMs: 0 }
  const touch = (rel: string) => { if (rel.includes('/')) out.roots.add(rel.split('/')[0]); else out.rootDb = true }

  let done = 0
  for (const rel of paths) {
    const cleaned = rel.replace(/^\/+|\/+$/g, '')
    onProgress?.(done, cleaned)
    try {
      for (const part of cleaned.split('/')) {
        if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected path')
      }
      const abs = resolveKb(cleaned)
      const name = path.basename(cleaned)
      const targetRel = dest ? `${dest}/${name}` : name
      if (targetRel === cleaned) throw new KbError(400, 'already there')
      const st = await fsp.stat(abs)
      // Moving a directory into itself would take the tree with it.
      if (st.isDirectory() && (dest === cleaned || dest.startsWith(`${cleaned}/`))) {
        throw new KbError(400, 'cannot move a directory into itself')
      }
      const targetAbs = resolveKb(targetRel)
      if (await fsp.stat(targetAbs).then(() => true, () => false)) throw new KbError(409, 'name already exists there')
      await fsp.rename(abs, targetAbs)
      if (st.isFile()) {
        await moveCacheEntry(extractPath(cleaned), extractPath(targetRel))
        const from = pdfPreviewPaths(cleaned)
        const to = pdfPreviewPaths(targetRel)
        await moveCacheEntry(from.file, to.file)
        await moveCacheEntry(from.pages, to.pages)
        await moveCacheEntry(modelCachePath(cleaned), modelCachePath(targetRel))
      }
      out.moved.push({ from: cleaned, to: targetRel })
      touch(cleaned)
      touch(targetRel)
    } catch (err) {
      out.skipped.push({ path: cleaned, reason: err instanceof KbError ? err.message : String((err as Error).message ?? err) })
    }
    onProgress?.(++done, cleaned)
  }
  return out
}

/**
 * Rename in place: the same bookkeeping as a move, since the corpus sees no
 * difference between a file arriving under a new name and one arriving in a
 * new directory.
 */
export async function renamePath(rel: string, nameRaw: string): Promise<MoveOutcome> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  const name = path.basename(nameRaw.trim())
  if (!cleaned) throw new KbError(400, 'path required')
  if (!name || name.startsWith('.') || name !== nameRaw.trim()) throw new KbError(400, `unusable name: ${nameRaw}`)
  if (/[/\\]/.test(name)) throw new KbError(400, 'a name cannot contain a path separator')
  for (const part of cleaned.split('/')) {
    if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected path')
  }

  const parent = cleaned.includes('/') ? cleaned.slice(0, cleaned.lastIndexOf('/')) : ''
  const targetRel = parent ? `${parent}/${name}` : name
  const out: MoveOutcome = { moved: [], skipped: [], roots: new Set(), rootDb: false, indexMs: 0 }
  if (targetRel === cleaned) return out

  const abs = resolveKb(cleaned)
  const targetAbs = resolveKb(targetRel)
  const st = await fsp.stat(abs)
  if (await fsp.stat(targetAbs).then(() => true, () => false)) throw new KbError(409, 'name already exists here')
  await fsp.rename(abs, targetAbs)
  if (st.isFile()) {
    await moveCacheEntry(extractPath(cleaned), extractPath(targetRel))
    const from = pdfPreviewPaths(cleaned)
    const to = pdfPreviewPaths(targetRel)
    await moveCacheEntry(from.file, to.file)
    await moveCacheEntry(from.pages, to.pages)
    await moveCacheEntry(modelCachePath(cleaned), modelCachePath(targetRel))
  }
  out.moved.push({ from: cleaned, to: targetRel })
  if (parent) out.roots.add(parent.split('/')[0])
  else out.rootDb = true
  return out
}

/**
 * Copy into another directory. Derived caches are not copied: they are
 * rebuilt for the new path on the next pass, and a stale copy of an
 * extraction is worse than none.
 */
export async function copyPaths(
  paths: string[],
  destRaw: string,
  onProgress?: (done: number, current: string) => void,
): Promise<MoveOutcome> {
  const dest = destRaw.replace(/^\/+|\/+$/g, '')
  if (!paths.length) throw new KbError(400, 'paths required')
  for (const part of dest.split('/').filter(Boolean)) {
    if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected destination')
  }
  const dstStat = await fsp.stat(resolveKb(dest)).catch(() => null)
  if (!dstStat?.isDirectory()) throw new KbError(400, `not a directory: ${dest || 'root'}`)

  const out: MoveOutcome = { moved: [], skipped: [], roots: new Set(), rootDb: false, indexMs: 0 }
  const touch = (rel: string) => { if (rel.includes('/')) out.roots.add(rel.split('/')[0]); else out.rootDb = true }

  let done = 0
  for (const rel of paths) {
    const cleaned = rel.replace(/^\/+|\/+$/g, '')
    onProgress?.(done, cleaned)
    try {
      for (const part of cleaned.split('/')) {
        if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, 'protected path')
      }
      const abs = resolveKb(cleaned)
      const st = await fsp.stat(abs)
      if (st.isDirectory() && (dest === cleaned || dest.startsWith(`${cleaned}/`))) {
        throw new KbError(400, 'cannot copy a directory into itself')
      }
      // Copying beside the original needs a name that is free, the way a
      // file manager appends "copy" rather than refusing.
      const name = path.basename(cleaned)
      const ext = st.isFile() ? path.extname(name) : ''
      const stem = ext ? name.slice(0, -ext.length) : name
      let targetRel = dest ? `${dest}/${name}` : name
      for (let n = 2; await fsp.stat(resolveKb(targetRel)).then(() => true, () => false); n++) {
        const candidate = n === 2 ? `${stem} copy${ext}` : `${stem} copy ${n}${ext}`
        targetRel = dest ? `${dest}/${candidate}` : candidate
        if (n > 50) throw new KbError(409, 'too many copies of that name')
      }
      await fsp.cp(abs, resolveKb(targetRel), { recursive: true })
      out.moved.push({ from: cleaned, to: targetRel })
      touch(targetRel)
    } catch (err) {
      out.skipped.push({ path: cleaned, reason: err instanceof KbError ? err.message : String((err as Error).message ?? err) })
    }
    onProgress?.(++done, cleaned)
  }
  return out
}

/** Re-index each touched subtree once, however many files moved through it. */
export async function reindexRoots(roots: Set<string>, rootDb: boolean): Promise<number> {
  let ms = 0
  for (const root of roots) ms += (await incrementalIndexDir(root)).ms
  if (rootDb) ms += (await indexRootDb()).ms
  return ms
}
