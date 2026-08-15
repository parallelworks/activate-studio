/**
 * Background jobs with progress, for work the interface should be able to
 * watch rather than wait on inside a request: a bulk move of a few hundred
 * files, the indexing pass after an upload.
 *
 * Deliberately in memory. A job is only interesting while the page that
 * started it is still open, and a restart drops the page too.
 */

export interface Job {
  id: number
  kind: 'move' | 'index'
  /** What the job is doing right now, for the label above the bar. */
  phase: string
  state: 'running' | 'done' | 'error'
  done: number
  total: number
  /** The item in flight, so a slow step names itself. */
  current: string
  /** Whatever the job wants to hand back when it finishes. */
  result: unknown
  error: string | null
  startedAt: string
  finishedAt: string | null
  ms: number | null
}

let seq = 0
const jobs = new Map<number, Job>()

export interface JobHandle {
  job: Job
  progress: (patch: Partial<Pick<Job, 'phase' | 'done' | 'total' | 'current'>>) => void
}

export function startJob(
  kind: Job['kind'],
  total: number,
  run: (h: JobHandle) => Promise<unknown>,
): Job {
  const job: Job = {
    id: ++seq, kind, phase: 'starting', state: 'running',
    done: 0, total, current: '', result: null, error: null,
    startedAt: new Date().toISOString(), finishedAt: null, ms: null,
  }
  jobs.set(job.id, job)
  // Finished jobs are kept only long enough to be read once or twice.
  for (const [id, j] of jobs) if (jobs.size > 30 && j.state !== 'running') jobs.delete(id)

  const t0 = Date.now()
  const handle: JobHandle = {
    job,
    progress: patch => Object.assign(job, patch),
  }
  void (async () => {
    try {
      job.result = await run(handle)
      job.state = 'done'
      job.phase = 'done'
    } catch (e) {
      job.error = String((e as Error).message ?? e).slice(0, 400)
      job.state = 'error'
      job.phase = 'error'
    } finally {
      job.ms = Date.now() - t0
      job.finishedAt = new Date().toISOString()
    }
  })()
  return job
}

export function getJob(id: number): Job | null { return jobs.get(id) ?? null }
