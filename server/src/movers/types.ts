/**
 * The data mover layer: how files get from a library to somewhere else.
 *
 * The assistant has one transfer tool (chat/tools.ts, `transfer_files`) and
 * never calls a mover. The Studio owns everything around one: what may be
 * copied and where (plan.ts, destination.ts), the person's approval
 * (chat/approvals.ts), mapping a library's paths to a place the files can
 * be read (source.ts), the job (jobs.ts), and checking the result. A mover
 * only moves bytes, so a new one is a small adapter.
 *
 * The contract is that of a job service, which a local process fits as
 * well as a remote one: `start` hands back a transfer id at once, `status`
 * is polled with it, and `cancel` asks for a stop. A mover's own word is
 * never the result: when `status` says done, the Studio walks the
 * destination and compares it with what it listed at the source, since a
 * mover can report success on a bad result.
 */

/** Where a library's files are read: its path mapping, and how to get there. */
export type SourceRoute =
  | { kind: 'local'; root: string; name: string; via: 'library' }
  /** Over ssh or pw ssh. `key` is the viewer's platform key for a cluster,
   *  '' over plain ssh, and is never stored or shown. */
  | { kind: 'ssh'; site: string; key: string; root: string; name: string; via: 'library' | 'read-through' }

/** One file to copy, as the Studio found it at the source. */
export interface ManifestFile {
  /** Relative to the route's root. */
  rel: string
  /** Relative to the destination. */
  out: string
  size: number
  /** Seconds. */
  mtime: number
  /** Permission bits. */
  mode: number
}

export interface MoverRequest {
  /** The Studio's job id, for a mover that labels its work. */
  job: string
  source: SourceRoute
  /** What the person chose, relative to the route's root. A job service
   *  that takes folders is given these; a mover that copies file by file
   *  uses `files`. */
  items: string[]
  files: ManifestFile[]
  /** An empty folder of the Studio's own. Each file belongs at `out` below
   *  it, and nothing else may be left there. */
  dest: string
}

export interface MoverStatus {
  /** `lost`: the mover no longer knows the transfer, as a local process
   *  after a restart. Nothing can be said about what it did. */
  state: 'running' | 'done' | 'failed' | 'cancelled' | 'lost'
  /** Progress, where the mover can tell. */
  bytes?: number
  files?: number
  /** Why it failed, in words a person can read. */
  message?: string
}

export interface Mover {
  id: string
  /** How a plan names it. */
  label: string
  /** How often the Studio asks `status`. */
  pollMs: number
  /** Whether this mover can read these files; why not, when it cannot. */
  canReach(source: SourceRoute): { ok: true } | { ok: false; why: string }
  /** Starts the transfer and returns its id without waiting for it. A
   *  failure that shows only later belongs in `status`. */
  start(req: MoverRequest): Promise<string>
  status(id: string): Promise<MoverStatus>
  /** Asks the transfer to stop; false when the mover cannot stop one. */
  cancel(id: string): Promise<boolean>
}

const registry = new Map<string, Mover>()

export function registerMover(m: Mover): void { registry.set(m.id, m) }
export function unregisterMover(id: string): void { registry.delete(id) }
export function moverById(id: string): Mover | undefined { return registry.get(id) }
export function moverIds(): string[] { return [...registry.keys()] }

/** The first mover, in the order they were registered, that can read the
 *  route; why none can, when none can. */
export function moverFor(source: SourceRoute): { mover: Mover } | { why: string } {
  const why: string[] = []
  for (const mover of registry.values()) {
    const r = mover.canReach(source)
    if (r.ok) return { mover }
    why.push(`${mover.label}: ${r.why}`)
  }
  return { why: why.join('; ') || 'no mover is registered' }
}
