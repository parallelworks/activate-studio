import fs from 'node:fs'
import path from 'node:path'
import { isMainThread, parentPort, Worker, workerData } from 'node:worker_threads'

/**
 * Each folder's latest change under a root: the folder's own time and the
 * newest of the files directly in it, keyed by its path below the root ('' for
 * the root itself), as the background sync records them. Names starting with
 * a dot are passed over, and so are the folders `exclude` names.
 *
 * The walk runs on a worker thread with synchronous calls. The sync asked for
 * every folder and every file one await at a time on the server's own thread,
 * which took 22 to 47 s for a tree of 449,000 folders and 1.5 million files;
 * the same walk with synchronous calls takes about 6 s, and on a worker thread
 * it holds nothing else up.
 */
export function scanLatest(root: string, exclude: Iterable<string>): Promise<Map<string, number>> {
  return scanLatestDetail(root, exclude).then(r => r.latest)
}

/** The same, with the folders that could not be listed for want of
 *  permission (EPERM, which macOS answers where its privacy settings have
 *  not let the app in, or EACCES), by their path below the root. Their
 *  content is left out of the scan, as it is of the index. */
export function scanLatestDetail(root: string, exclude: Iterable<string>): Promise<{ latest: Map<string, number>; denied: string[] }> {
  const done = (r: Walked) => ({ latest: new Map(r.pairs), denied: r.denied })
  return new Promise(resolve => {
    let worker: Worker
    try {
      // The process's own flags, less those a worker refuses (--input-type,
      // --eval) or should not repeat (--test); a loader such as tsx stays.
      const execArgv = process.execArgv.filter(a => !/^--(input-type|eval|print|test)\b/.test(a))
      worker = new Worker(new URL(import.meta.url), { workerData: { dirScan: true, root, exclude: [...exclude] }, execArgv })
    } catch {
      // No worker here (a test runner, say): the same walk, on this thread.
      resolve(done(walk(root, new Set(exclude))))
      return
    }
    worker.once('message', (r: Walked) => resolve(done(r)))
    worker.once('error', () => resolve(done(walk(root, new Set(exclude)))))
  })
}

interface Walked { pairs: [string, number][]; denied: string[] }

function walk(root: string, exclude: Set<string>): Walked {
  const out: [string, number][] = []
  const denied: string[] = []
  const visit = (abs: string, rel: string) => {
    let entries: fs.Dirent[]
    try { entries = fs.readdirSync(abs, { withFileTypes: true }) } catch (e) {
      const code = (e as NodeJS.ErrnoException).code
      if (code === 'EPERM' || code === 'EACCES') denied.push(rel)
      return
    }
    let latest = 0
    try { latest = fs.statSync(abs, { throwIfNoEntry: false })?.mtimeMs ?? 0 } catch { /* gone */ }
    const dirs: fs.Dirent[] = []
    for (const e of entries) {
      if (e.name.startsWith('.')) continue
      if (e.isDirectory()) { if (!exclude.has(e.name)) dirs.push(e); continue }
      try {
        const s = fs.statSync(path.join(abs, e.name), { throwIfNoEntry: false })
        if (s && s.mtimeMs > latest) latest = s.mtimeMs
      } catch { /* unreadable: as gone */ }
    }
    out.push([rel, Math.floor(latest)])
    for (const d of dirs) visit(path.join(abs, d.name), rel ? `${rel}/${d.name}` : d.name)
  }
  visit(root, '')
  return { pairs: out, denied }
}

if (!isMainThread && (workerData as { dirScan?: boolean } | null)?.dirScan) {
  const { root, exclude } = workerData as { root: string; exclude: string[] }
  parentPort!.postMessage(walk(root, new Set(exclude)))
}
