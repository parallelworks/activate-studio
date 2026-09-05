/**
 * The platform runs a user's workflows inside a per-user workspace that
 * scales down when idle. A submission made while it is down fails with
 * "User workspace not found or is not running" (and, for a little while
 * after a start request, "Could not execute workflow"), and the platform
 * did not start it on demand for a CLI launch. Every launch path in the
 * Studio therefore goes through this: on that failure, ask for a start,
 * then retry the launch until it is accepted or the budget runs out. The
 * launch itself is the ground truth for readiness; status text is not
 * consulted.
 */
export const WORKSPACE_DOWN = /user workspace not found or is not running|could not execute workflow/i

export function isWorkspaceDown(message: string): boolean {
  return WORKSPACE_DOWN.test(message)
}

export interface WorkspaceRunner {
  /** Run the pw CLI with these arguments; rejects with the CLI's error text. */
  cli: (args: string[], timeoutMs?: number) => Promise<string>
  sleep?: (ms: number) => Promise<void>
  /** Total time to keep retrying after a start request. */
  budgetMs?: number
  retryEveryMs?: number
  log?: (msg: string) => void
}

/**
 * Run `launch`, and if it fails because the workspace is down, start the
 * workspace and keep retrying the launch until it succeeds or the budget
 * is spent. Returns what the launch returns; rethrows anything that is
 * not the workspace condition. `started` reports whether a start was
 * needed, so a caller can say so.
 */
export async function withWorkspace<T>(r: WorkspaceRunner, launch: () => Promise<T>): Promise<{ value: T; started: boolean; waitedMs: number }> {
  const sleep = r.sleep ?? (ms => new Promise(res => setTimeout(res, ms)))
  const budget = r.budgetMs ?? 240_000
  const every = r.retryEveryMs ?? 15_000
  try {
    return { value: await launch(), started: false, waitedMs: 0 }
  } catch (e) {
    if (!isWorkspaceDown(String((e as Error).message ?? e))) throw e
  }
  r.log?.('user workspace is not running; requesting a start and retrying the launch')
  try { await r.cli(['workspace', 'start'], 120_000) } catch (e) { r.log?.(`workspace start: ${String((e as Error).message ?? e).slice(0, 200)}`) }
  const t0 = Date.now()
  let lastErr: unknown = null
  while (Date.now() - t0 < budget) {
    await sleep(every)
    try {
      return { value: await launch(), started: true, waitedMs: Date.now() - t0 }
    } catch (e) {
      lastErr = e
      if (!isWorkspaceDown(String((e as Error).message ?? e))) throw e
    }
  }
  throw new Error(`The platform user workspace is still not running after a start request and ${Math.round(budget / 1000)} seconds of retries; the last error was: ${String((lastErr as Error)?.message ?? lastErr).slice(0, 200)}. Start it from the platform (pw workspace start) and try again.`)
}
