import { execFile } from 'node:child_process'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { authEnabled } from './auth.js'
import { INDEX_BASE, INDEX_DIR, KB_ROOT } from './config.js'
import { extractorReport, forgetExtractors } from './extractors.js'
import {
  cancelAllIndexJobs, cancelIndexJob, getIndexJob, incrementalIndexDir, indexJobsRunning, indexStatus, startBuild, startDelete,
  startIndexJob, startOtherJob, sweep,
} from './indexing.js'
import { INDEX_LOG, indexLogInfo, tailIndexLog } from './indexLog.js'
import { INDEX_SETTINGS_DEFAULTS, cpuCount, indexSettings, saveIndexSettings, threadsFor } from './indexSettings.js'
import { KbError } from './kb.js'
import { getLibrary, requireWritable } from './libraries.js'
import { canRestart, launcherConfigFile, readLauncherSettings, requestRestart, saveLauncherSetting } from './lifecycle.js'
import { effectiveSettings } from './settings.js'

/**
 * The index's own routes: its status (one answer for every view that shows
 * indexing), the jobs and their controls (build, stop, delete), the log,
 * what can be read on this host, and the settings for how the index is
 * built and where it is kept.
 */

function requireSingleUser(what: string): void {
  if (authEnabled()) throw new KbError(403, `${what} is for a single-user install; on this Studio the deployment manages the index`)
}

function readerOptions() {
  const s = indexSettings()
  const eff = effectiveSettings()
  return { documents: s.documents, ocr: s.ocr, legacyOffice: s.legacyOffice, pdfReader: s.pdfReader, vision: eff.aiEnabled && s.imageDescriptions && !!eff.visionModel }
}

/** A path as typed: ~ is the home folder, and a relative path is taken from it. */
function expand(p: string): string {
  const home = os.homedir()
  if (p === '~') return home
  if (p.startsWith('~/')) return path.join(home, p.slice(2))
  return path.resolve(home, p)
}

const within = (parent: string, child: string) => child === parent || child.startsWith(parent + path.sep)
const real = (p: string) => { try { return fs.realpathSync(p) } catch { return path.resolve(p) } }

export async function indexRoutes(app: FastifyInstance): Promise<void> {
  const primary = (req: { user?: { id?: string } }) => getLibrary(null, effectiveSettings().kbLabel, req.user?.id ?? null)

  app.get('/api/index/status', async () => ({ ...indexStatus(), log: { ...indexLogInfo(), reveal: process.platform === 'darwin' && !authEnabled() } }))

  // What this host can extract text from, so a format that indexes by
  // filename alone is visible rather than a silent gap.
  app.get('/api/index/extractors', async req => {
    if ((req.query as { fresh?: string }).fresh === '1') forgetExtractors()
    return extractorReport(readerOptions())
  })

  app.post('/api/index/dir', async req => {
    requireWritable(primary(req))
    const { path: rel = '' } = (req.body ?? {}) as { path?: string }
    if (!rel) return { error: 'path required' }
    const { ms } = await incrementalIndexDir(rel)
    return { indexed: rel, ms }
  })

  // Start an indexing pass and watch it, so a bulk upload can show the
  // phase it is in rather than holding a request open.
  app.post('/api/index/job', async req => {
    requireWritable(primary(req))
    const { path: rel = '' } = (req.body ?? {}) as { path?: string }
    return startIndexJob(String(rel).replace(/^\/+|\/+$/g, ''))
  })

  app.get('/api/index/job/:id', async (req, reply) => {
    const job = getIndexJob(Number((req.params as { id: string }).id))
    if (!job) return reply.status(404).send({ error: 'no such job' })
    return job
  })

  app.post('/api/index/job/:id/cancel', async (req, reply) => {
    requireWritable(primary(req))
    const job = cancelIndexJob(Number((req.params as { id: string }).id))
    if (!job) return reply.status(404).send({ error: 'no such job' })
    return job
  })

  // Stop indexing: the running job and everything waiting behind it.
  app.post('/api/index/cancel', async req => {
    requireWritable(primary(req))
    return { stopped: cancelAllIndexJobs() }
  })

  // Build (or bring up to date) the main folder's whole index, in the
  // background; follow it through /api/index/status or /api/index/job/:id.
  app.post('/api/index/build', async req => {
    requireWritable(primary(req))
    return startBuild()
  })

  // The same, under the name scripts and the macOS app check have used.
  app.post('/api/reindex', async req => {
    requireWritable(primary(req))
    const job = startBuild()
    return { started: true, job }
  })

  // Deletes the index and its caches, never the folder's files. The page
  // asks first; the request must say it was confirmed.
  app.delete('/api/index', async req => {
    requireSingleUser('Deleting the index')
    requireWritable(primary(req))
    if ((req.query as { confirm?: string }).confirm !== '1') throw new KbError(400, 'add ?confirm=1: this deletes the index (the files stay)')
    return startDelete()
  })

  app.post('/api/index/sweep', async req => {
    requireWritable(primary(req))
    const changed = await sweep(m => app.log.info(m))
    return { changed }
  })

  // The index log as plain text: the last lines (?lines=N), or all of it
  // (?full=1), which a browser shows as a page of its own.
  app.get('/api/index/log', async (req, reply) => {
    const q = req.query as { lines?: string; full?: string }
    reply.header('Content-Type', 'text/plain; charset=utf-8').header('Cache-Control', 'no-store')
    if (q.full === '1') {
      try { return reply.send(await fsp.readFile(INDEX_LOG, 'utf8')) } catch { return reply.send('') }
    }
    const n = Math.min(Math.max(Number(q.lines) || 400, 1), 5000)
    return reply.send(await tailIndexLog(n))
  })

  // On a Mac, show the log in the Finder (the Studio runs as the person on
  // their own computer).
  app.post('/api/index/log/reveal', async () => {
    requireSingleUser('Opening the log')
    if (process.platform !== 'darwin') throw new KbError(409, `open ${INDEX_LOG} on the machine the Studio runs on`)
    if (!fs.existsSync(INDEX_LOG)) throw new KbError(404, 'nothing has been logged yet')
    await new Promise<void>((resolve, reject) => execFile('open', ['-R', INDEX_LOG], { timeout: 10_000 }, e => (e ? reject(e) : resolve())))
    return { opened: INDEX_LOG }
  })

  app.get('/api/index/settings', async () => {
    const launcher = !!launcherConfigFile()
    return {
      settings: indexSettings(),
      defaults: INDEX_SETTINGS_DEFAULTS,
      // A single-user install builds and deletes its index here; a Studio
      // that signs users in has its index managed by the deployment.
      single: !authEnabled(),
      cpus: cpuCount(),
      threads: threadsFor(),
      readers: extractorReport(readerOptions()),
      ai: { enabled: effectiveSettings().aiEnabled, visionModel: effectiveSettings().visionModel },
      location: {
        current: INDEX_DIR,
        default: INDEX_BASE,
        folder: KB_ROOT,
        canChange: !authEnabled() && launcher,
        saved: launcher ? (readLauncherSettings().STUDIO_INDEX_DIR ?? '') : '',
      },
    }
  })

  app.put('/api/index/settings', async req => {
    const body = (req.body ?? {}) as Record<string, unknown>
    const settings = await saveIndexSettings('kb', body)
    forgetExtractors()
    return { settings, threads: threadsFor(), readers: extractorReport(readerOptions()) }
  })

  // Keep the index somewhere other than the Studio's data folder (an
  // external disk, say). The index and its caches move there, and the
  // Studio restarts on it; '' goes back to the default.
  app.post('/api/index/location', async req => {
    requireSingleUser('Moving the index')
    if (!launcherConfigFile()) {
      throw new KbError(409, 'Set STUDIO_INDEX_DIR in the Studio\'s environment and restart it.')
    }
    const raw = String(((req.body ?? {}) as { path?: string }).path ?? '').trim()
    const target = raw ? expand(raw) : INDEX_BASE
    if (real(target) === real(INDEX_DIR)) return { moved: false, restarting: false, note: 'The index is already kept there.' }
    if (fs.existsSync(target) && !fs.statSync(target).isDirectory()) throw new KbError(400, `${target} is not a folder`)
    try { fs.mkdirSync(target, { recursive: true }); fs.accessSync(target, fs.constants.W_OK) } catch { throw new KbError(400, `${target} is not writable`) }
    if (within(real(KB_ROOT), real(target))) throw new KbError(400, `${target} is inside the folder being indexed; choose a place outside it`)
    if (within(real(target), real(KB_ROOT))) throw new KbError(400, `${target} contains the folder being indexed; choose a place outside it`)
    if (indexJobsRunning()) throw new KbError(409, 'Indexing is running; stop it or wait for it to finish, then move the index.')
    const from = INDEX_DIR
    const job = startOtherJob('move', `Moving the index to ${target}`, async log => {
      for (const name of ['gufi', 'extract', 'embed-cache.db', 'folders', 'indexed-root']) {
        const src = path.join(from, name)
        if (!fs.existsSync(src)) continue
        const dst = path.join(target, name)
        await fsp.rm(dst, { recursive: true, force: true })
        try { await fsp.rename(src, dst) } catch (e) {
          if ((e as NodeJS.ErrnoException).code !== 'EXDEV') throw e
          await fsp.cp(src, dst, { recursive: true })
          await fsp.rm(src, { recursive: true, force: true })
        }
        log(`moved ${src} to ${dst}`)
      }
      saveLauncherSetting('STUDIO_INDEX_DIR', raw && real(target) !== real(INDEX_BASE) ? target : '')
    })
    // The move is quick on one disk and takes a while to another; the
    // restart waits for it.
    void (async () => {
      const until = Date.now() + 6 * 3600_000
      while (Date.now() < until) {
        const j = getIndexJob(job.id)
        if (!j || (j.state !== 'queued' && j.state !== 'running')) break
        await new Promise(r => setTimeout(r, 500))
      }
      if (getIndexJob(job.id)?.state === 'done') requestRestart()
    })()
    return canRestart()
      ? { moved: true, restarting: true, job }
      : { moved: true, restarting: false, job, note: 'Restart the Studio to use the index there.' }
  })
}
