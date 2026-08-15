import { execFile } from 'node:child_process'
import { dumpYaml } from '@parallelworks/workflow-parser'

/**
 * Compose several existing workflows into one that runs them as
 * subworkflows, the way the platform's `uses:` step does.
 *
 * The platform can already run a published workflow as a step, so a chain
 * does not need its steps copied in: each source workflow becomes a job
 * whose single step uses it, and the composed workflow declares whatever
 * inputs those steps need. Copying would fork the sources and freeze them
 * at today's definition; referencing keeps each one owned by whoever
 * maintains it.
 *
 * Inputs are merged by name. Two workflows asking for a resource ask for
 * the same one, which is usually what a chain wants and is the only
 * behaviour that keeps the generated form short; a conflicting definition
 * is reported rather than silently resolved.
 */

export interface ComposeStep {
  /** Workflow to run, e.g. 'cloudinit' or 'marketplace/cloudinit/v1.0.1'. */
  uses: string
  /** Job id in the composed workflow; derived from `uses` when absent. */
  id?: string
  /** Human label for the step. */
  name?: string
  /** Job ids this one waits for; omit for a straight chain. */
  needs?: string[]
}

export interface ComposeResult {
  yaml: Record<string, unknown>
  yamlText: string
  inputs: string[]
  conflicts: string[]
  missing: string[]
}

function jobId(uses: string, taken: Set<string>): string {
  const base = (uses.split('/').filter(p => p && !/^v?\d/.test(p)).pop() ?? 'job')
    .toLowerCase().replace(/[^a-z0-9_]+/g, '_').replace(/^_+|_+$/g, '') || 'job'
  if (!taken.has(base)) return base
  for (let i = 2; ; i++) if (!taken.has(`${base}_${i}`)) return `${base}_${i}`
}

function pw(args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile('pw', args, { timeout: 30_000, maxBuffer: 16 * 1024 * 1024 },
      (err, so, se) => (err ? reject(new Error(se || err.message)) : resolve(so)))
  })
}

/** The workflow's own name, without a marketplace prefix or version. */
function sourceName(uses: string): string {
  const parts = uses.split('/').filter(Boolean)
  const withoutMarketplace = parts[0] === 'marketplace' ? parts.slice(1) : parts
  return withoutMarketplace.filter(p => !/^v?\d/.test(p))[0] ?? uses
}

export async function composeWorkflows(steps: ComposeStep[], opts: { chain?: boolean } = {}): Promise<ComposeResult> {
  if (!steps.length) throw new Error('at least one workflow is required')

  const taken = new Set<string>()
  const resolved = steps.map(s => {
    const id = (s.id ?? jobId(s.uses, taken)).replace(/[^A-Za-z0-9_]/g, '_')
    taken.add(id)
    return { ...s, id }
  })

  // Merge the inputs each source declares, so the composed workflow asks
  // for them once and passes them down.
  const inputs: Record<string, Record<string, unknown>> = {}
  const conflicts: string[] = []
  const missing: string[] = []
  const perStepInputs = new Map<string, string[]>()

  for (const step of resolved) {
    const name = sourceName(step.uses)
    let doc: Record<string, any> | null = null
    try {
      doc = JSON.parse(await pw(['workflows', 'get', name, '-o', 'json']))
    } catch {
      missing.push(name)
    }
    const declared = (doc?.yaml?.on?.execute?.inputs ?? {}) as Record<string, any>
    const names: string[] = []
    for (const [key, def] of Object.entries(declared)) {
      if (def && typeof def === 'object' && def.type === 'group') {
        // Groups nest their fields; the composed form takes the fields.
        for (const [k, d] of Object.entries((def.items ?? {}) as Record<string, unknown>)) {
          names.push(k)
          record(k, d as Record<string, unknown>, name)
        }
        continue
      }
      names.push(key)
      record(key, def as Record<string, unknown>, name)
    }
    perStepInputs.set(step.id, names)
  }

  function record(key: string, def: Record<string, unknown>, from: string): void {
    const existing = inputs[key]
    if (!existing) { inputs[key] = { ...def }; return }
    if (JSON.stringify(existing) !== JSON.stringify(def)) {
      conflicts.push(`${key}: ${from} declares it differently; keeping the first definition`)
    }
  }

  const jobs: Record<string, unknown> = {}
  resolved.forEach((step, i) => {
    const withInputs: Record<string, string> = {}
    for (const key of perStepInputs.get(step.id) ?? []) withInputs[key] = `\${{ inputs.${key} }}`
    const needs = step.needs ?? (opts.chain !== false && i > 0 ? [resolved[i - 1].id] : undefined)
    jobs[step.id] = {
      ...(needs?.length ? { needs } : {}),
      steps: [{
        name: step.name ?? `Run ${sourceName(step.uses)}`,
        uses: step.uses,
        ...(Object.keys(withInputs).length ? { with: withInputs } : {}),
      }],
    }
  })

  const yaml: Record<string, unknown> = {
    permissions: ['*'],
    on: { execute: { inputs } },
    jobs,
  }
  return { yaml, yamlText: dumpYaml(yaml), inputs: Object.keys(inputs), conflicts, missing }
}
