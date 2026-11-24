// A step that failed and that the turn then did again on the same target
// (a later call of the same tool on the same file, folder, run or name that
// went through) is part of the work, not an open failure: the thread folds
// it with the other steps instead of drawing it red on its own row, and its
// result still says it failed. The server leaves it out of the suggested
// retries (server-go chat, suggest.go, recovered), by the same rule.

export const RECOVERED_NOTE = 'Failed, then done again by a later step. '

interface Call { kind?: string; id?: string; name?: string; args?: string; status?: string; result?: string }

/** What a call works on, from its arguments, which may be cut off. */
export function stepTarget(args: string | undefined): string {
  const keys = ['path', 'from', 'run', 'name']
  try {
    const v = JSON.parse(args || '{}') as Record<string, unknown>
    for (const k of keys) { const s = typeof v[k] === 'string' ? (v[k] as string).trim() : ''; if (s) return s }
    return ''
  } catch {
    for (const k of keys) { const m = new RegExp(`"${k}"\\s*:\\s*"([^"]+)"`).exec(args ?? ''); if (m?.[1]?.trim()) return m[1].trim() }
    return ''
  }
}

const settled = (c: Call) => c.status !== 'error' && c.status !== 'running' && c.status !== 'waiting'

/** The ids of the failed calls among `parts` a later call recovered. */
export function recoveredSteps(parts: readonly Call[]): Set<string> {
  const calls = parts.filter(p => p.kind === 'tool_call')
  const out = new Set<string>()
  calls.forEach((c, i) => {
    if (c.status !== 'error' || !c.id) return
    const target = stepTarget(c.args)
    if (calls.slice(i + 1).some(l => l.name === c.name && settled(l) && stepTarget(l.args) === target)) out.add(c.id)
  })
  return out
}

/** A saved message's parts, with each recovered failure settled. */
export function foldRecovered<T extends Call>(parts: T[]): T[] {
  const ids = recoveredSteps(parts)
  if (!ids.size) return parts
  return parts.map(p => (p.id && ids.has(p.id) ? { ...p, status: 'ok', result: RECOVERED_NOTE + (p.result ?? '') } : p))
}

/**
 * The same while a reply streams: `start` and `end` follow each call, and
 * `end` gives the earlier failures a call that went through recovers, to be
 * ended again as settled.
 */
export function recoveryTracker() {
  const calls: Call[] = []
  return {
    start(id: string, name: string, args: string) { calls.push({ kind: 'tool_call', id, name, args, status: 'running' }) },
    end(id: string, result: string, isError: boolean): { id: string; result: string }[] {
      const c = calls.find(x => x.id === id)
      if (!c) return []
      c.status = isError ? 'error' : 'ok'
      c.result = result
      if (isError) return []
      const target = stepTarget(c.args)
      const out: { id: string; result: string }[] = []
      for (const f of calls) {
        if (f === c || f.status !== 'error' || f.name !== c.name || stepTarget(f.args) !== target) continue
        f.status = 'ok'
        out.push({ id: f.id!, result: RECOVERED_NOTE + (f.result ?? '') })
      }
      return out
    },
  }
}
