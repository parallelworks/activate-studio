#!/usr/bin/env node
/**
 * End-to-end check of the HPC chain the assistant uses: site knowledge,
 * a real (not dry-run) workflow submission as a scheduler job, following
 * it, and reading the result. Runs against a live platform through the
 * same executeTool entry point the chat calls, so a pass here means the
 * model has a working chain under it, not just unit-tested pieces.
 *
 *   PW_CONTEXT=activate node server/scripts/e2e-hpc.mjs [resource] [workflow]
 *
 * Defaults: resource a30gpuserver, workflow activatebatch (a Slurm batch
 * job that echoes a marker). Exit 0 only when the run completes and the
 * marker appears in its output.
 */
import path from 'node:path'
import { fileURLToPath } from 'node:url'
const here = path.dirname(fileURLToPath(import.meta.url))
process.env.KB_ROOT ||= '/tmp'
process.env.INDEX_BASE ||= path.join(process.env.KB_ROOT, '.e2e-index')
const { executeTool } = await import(path.join(here, '..', 'dist', 'chat', 'tools.js'))

const resource = process.argv[2] || 'a30gpuserver'
const workflow = process.argv[3] || 'activatebatch'
const marker = `studio-e2e-${Date.now().toString(36)}`
const t0 = Date.now()
const stamp = () => `${((Date.now() - t0) / 1000).toFixed(0).padStart(4)}s`
const call = async (name, args) => {
  const r = await executeTool(name, JSON.stringify(args), {})
  const text = typeof r === 'string' ? r : (r.content ?? r.text ?? JSON.stringify(r))
  try { return JSON.parse(text) } catch { return { result: String(text), summary: '' } }
}

console.log(stamp(), `site knowledge: ${resource}`)
console.log((await call('hpc_environments', { resource })).summary)
console.log(stamp(), `submit ${workflow} on ${resource} through the scheduler`)
const launch = await call('run_workflow', {
  name: workflow, resource, dry_run: false,
  inputs: JSON.stringify({ commands: `hostname\necho ${marker}\nscontrol show job $SLURM_JOB_ID | head -1\n`, scheduler: true, slurm: { time: '00:05:00', nodes: 1, cpus_per_task: 1 } }),
})
const slug = (String(launch.result).match(new RegExp(`${workflow}-\\d{5}`)) || [])[0]
if (!slug) { console.error('no run slug in', String(launch.result).slice(0, 400)); process.exit(2) }
console.log(stamp(), 'run', slug)
let final = ''
for (let i = 0; i < 20; i++) {
  const w = await call('watch_run', { run: slug, wait: 45 })
  if (w.summary !== final) { console.log(stamp(), w.summary); final = w.summary }
  if (/:\s*(completed|error|failed|canceled)$/i.test(w.summary)) break
}
const detail = await call('workflow_run_detail', { run: slug })
const ok = /:\s*completed$/i.test(final) && String(detail.result).includes(marker)
console.log(stamp(), ok ? `PASS: ${slug} completed and the marker came back from the cluster` : `FAIL: ${final || 'no terminal state'}; marker ${String(detail.result).includes(marker) ? 'present' : 'absent'}`)
process.exit(ok ? 0 : 1)
