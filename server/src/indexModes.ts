import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'

/**
 * Index folders of a source whose folders their owner may not write.
 *
 * gufi_dir2index gives each folder of the index the mode of the folder it
 * indexes, so the index keeps the source's permissions. It does so as it
 * finishes a folder, before the folders inside it have made theirs. Run by
 * a user other than root, a source folder of mode 0555 (a read-only share,
 * a snapshot, a tree unpacked read only) gets an index folder this process
 * may not write in: gufi_dir2index cannot make the index folders inside it,
 * prints "mkdir … failure" for each, leaves them and everything below them
 * out, and exits 0. The text pass, which writes to each folder's db.db, and
 * moving the tree into place then fail on the folder they may not write.
 *
 * So before it runs, the index folders inside each such folder are made
 * (gufi_dir2index takes a folder that exists), and after it, each such
 * index folder gets read, write and search permission for its owner, this
 * process. What GUFI records about the source, the modes included, is in
 * its databases and is unchanged; the group and other bits of the index
 * folders stay those of the source. Root needs none of this.
 */

/** A source folder whose owner may not write it, by its path below the
 *  root ('' for the root), and the folders in it gufi_dir2index enters. */
export interface Locked { rel: string; dirs: string[] }

/** Whether this process is one an index folder's mode can keep out: any
 *  user but root. */
export function lockingUser(): boolean {
  return process.getuid?.() !== 0
}

/** Whether gufi_dir2index, run here, makes an index folder of this mode
 *  that this process may not write in. */
export function locks(mode: number): boolean {
  return lockingUser() && (mode & 0o700) !== 0o700
}

/**
 * The entry for a folder the walk visits: null unless its mode locks it.
 * `entries` are what it holds; `skip` the folder names gufi_dir2index is
 * told to pass over. Only the folders this process may list are named,
 * since gufi_dir2index makes no index folder for one it cannot open.
 */
export function lockedEntry(abs: string, rel: string, mode: number, entries: fs.Dirent[], skip: ReadonlySet<string>): Locked | null {
  if (!locks(mode)) return null
  const dirs: string[] = []
  for (const e of entries) {
    if (!e.isDirectory() || skip.has(e.name)) continue
    try { fs.accessSync(path.join(abs, e.name), fs.constants.R_OK | fs.constants.X_OK); dirs.push(e.name) } catch { /* left out, and said so */ }
  }
  return { rel, dirs }
}

/** The entries at or below `rel`, with their paths below it. */
export function lockedUnder(locked: Locked[], rel: string): Locked[] {
  if (!rel) return locked
  return locked.flatMap(l => (l.rel === rel ? [{ ...l, rel: '' }] : l.rel.startsWith(`${rel}/`) ? [{ ...l, rel: l.rel.slice(rel.length + 1) }] : []))
}

/** Makes the index folders inside each locked folder, under `built` (the
 *  tree gufi_dir2index is about to write), before it runs. */
export async function prepareIndex(built: string, locked: Locked[]): Promise<void> {
  for (const l of locked) for (const d of l.dirs) await fsp.mkdir(path.join(built, l.rel, d), { recursive: true })
}

/** Gives a folder read, write and search permission for its owner, keeping
 *  the rest of its mode; nothing when it has them or is not there. */
export function openDir(p: string): void {
  let st: fs.Stats
  try { st = fs.statSync(p) } catch { return }
  if (st.isDirectory() && (st.mode & 0o700) !== 0o700) {
    try { fs.chmodSync(p, (st.mode & 0o7777) | 0o700) } catch { /* not ours: left as it is */ }
  }
}

/** After gufi_dir2index: the index folders of the locked folders, under
 *  `built`, opened for this process. Parents first, since a folder whose
 *  owner may not search it keeps out a change to what is inside. */
export function openIndex(built: string, locked: Locked[]): void {
  const order = [...locked].sort((a, b) => depthOf(a.rel) - depthOf(b.rel))
  for (const l of order) openDir(path.join(built, l.rel))
}
const depthOf = (rel: string) => (rel ? rel.split('/').length : 0)

/** Every folder under `root` opened, parents first: for a tree made before
 *  the index folders were opened, which `rm` cannot empty. */
function openTree(root: string): void {
  openDir(root)
  let entries: fs.Dirent[]
  try { entries = fs.readdirSync(root, { withFileTypes: true }) } catch { return }
  for (const e of entries) if (e.isDirectory()) openTree(path.join(root, e.name))
}

/**
 * Removes a tree as `rm -rf` does, opening its folders first when one
 * keeps the removal out: an index made before its folders were opened,
 * or by a GUFI run elsewhere. The folder holding `p` is opened too.
 */
export async function rmTree(p: string): Promise<void> {
  try { await fsp.rm(p, { recursive: true, force: true }); return } catch (e) { if (!refused(e)) throw e }
  openDir(path.dirname(p))
  openTree(p)
  await fsp.rm(p, { recursive: true, force: true })
}

/** The same, at once. */
export function rmTreeSync(p: string): void {
  try { fs.rmSync(p, { recursive: true, force: true }); return } catch (e) { if (!refused(e)) throw e }
  openDir(path.dirname(p))
  openTree(p)
  fs.rmSync(p, { recursive: true, force: true })
}

const refused = (e: unknown) => ['EACCES', 'EPERM', 'ENOTEMPTY'].includes((e as NodeJS.ErrnoException).code ?? '')

/** Folders gufi_dir2index could not make an index folder for, as it says
 *  ("mkdir <index folder>/db.db failure: 13 Permission denied"), by their
 *  index folders. */
export function unmadeIndexDirs(stderr: string): string[] {
  const out: string[] = []
  for (const m of String(stderr ?? '').matchAll(/^mkdir (\/.*)\/db\.db failure: \d+/gm)) out.push(m[1])
  return out
}

/** An index folder under `built` named again as the source folder it is of. */
export function sourceOf(indexDir: string, built: string, src: string): string | null {
  if (indexDir === built) return src
  return indexDir.startsWith(`${built}/`) ? path.join(src, indexDir.slice(built.length + 1)) : null
}
