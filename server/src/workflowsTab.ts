/**
 * The Workflows tab: a deployment's curated set of ACTIVATE workflows, shown
 * as tiles and run from their own forms without going through the chat.
 *
 * Everything here acts as the viewer. Workflow records on ACTIVATE belong to
 * one user and cannot be shared, so each person sees and runs their own
 * copies, under their own allocation and permissions. The curated set is a
 * list of record names; a viewer who lacks one can add it to their account
 * from the deployment owner's copy, which is the only cross-user path the
 * platform offers.
 *
 * The tab exists only where a platform credential does: a Studio running
 * without ACTIVATE has nothing to list.
 */
import type { FastifyInstance } from 'fastify'
import { execFile } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { parse as parseYaml, stringify as dumpYaml } from 'yaml'
import { convertToDynamicForm, prepareSubmittableValues, workflowHasUserInputs } from '@parallelworks/workflow-parser'
import { GATEWAY_BASE, PW_CLI } from './config.js'
import { gatewayKey } from './chat/gateway.js'
import { resolveUserCred } from './credentials.js'
import { effectiveSettings } from './settings.js'
import { KbError } from './kb.js'
import { recordRun, listRuns, markRunEnded, isTerminalRunState } from './runs.js'
import { withWorkspace } from './workspace.js'

/** The credential platform calls run under: the viewer's own platform key,
 *  else the deployment's. A provider key (one with a baseUrl) is chat-only. */
export function platformKeyFor(userId: string | undefined): { key: string | null; own: boolean } {
  const cred = userId ? resolveUserCred(userId) : null
  if (cred && !cred.baseUrl && cred.key) return { key: cred.key, own: true }
  const dep = gatewayKey()
  return { key: dep || null, own: false }
}

/** Whether this deployment can reach a platform at all, for the tab's visibility. */
export function platformAvailable(userId: string | undefined): boolean {
  return !!platformKeyFor(userId).key
}

type Cli = (args: string[], key: string | null, timeoutMs?: number) => Promise<string>
let cli: Cli = (args, key, timeoutMs = 60_000) => new Promise((resolve, reject) => {
  const env = key ? { ...process.env, PW_API_KEY: key } : process.env
  execFile(PW_CLI, args, { timeout: timeoutMs, maxBuffer: 32 * 1024 * 1024, env }, (err, stdout, stderr) =>
    err ? reject(new Error((stderr || err.message).trim())) : resolve(stdout))
})
/** Tests replace the CLI. */
export function setWorkflowsCli(fn: Cli): void { cli = fn }

/**
 * A failed CLI call as an error the page can show. Without this a plain
 * Error reached the sanitizing handler and the viewer saw only "internal
 * error", even when the cause was an expired credential they could fix.
 */
export function platformError(e: unknown): KbError {
  const text = String((e as Error)?.message ?? e).replace(/^\S+Z \[(ERROR|WARN)\] /gm, '').trim()
  if (/authentication has expired|please authenticate|401|unauthorized/i.test(text)) {
    return new KbError(401, 'The platform credential has expired. Add your own platform API key under Settings, Model access, or renew the deployment credential.')
  }
  return new KbError(502, `The platform did not answer: ${text.split('\n')[0].slice(0, 300)}`)
}
const call: Cli = (args, key, t) => cli(args, key, t).catch(e => { throw platformError(e) })

/** The CLI appends notices after its JSON; take the first balanced value. */
export function firstJson<T = unknown>(text: string): T {
  const start = text.search(/[[{]/)
  if (start < 0) throw new Error('no JSON in CLI output')
  const open = text[start], close = open === '[' ? ']' : '}'
  let depth = 0, inStr = false, esc = false
  for (let i = start; i < text.length; i++) {
    const c = text[i]
    if (inStr) { if (esc) esc = false; else if (c === '\\') esc = true; else if (c === '"') inStr = false; continue }
    if (c === '"') inStr = true
    else if (c === open) depth++
    else if (c === close && --depth === 0) return JSON.parse(text.slice(start, i + 1)) as T
  }
  throw new Error('unterminated JSON in CLI output')
}

interface WorkflowRow {
  name: string
  displayName?: string
  description?: string
  imageUrl?: string
  tags?: string[]
  type?: string
  configurations?: { id?: string; name?: string; inputs?: Record<string, unknown> }[]
}

const listCache = new Map<string, { at: number; rows: WorkflowRow[] }>()
async function listFor(key: string | null): Promise<WorkflowRow[]> {
  const k = (key ?? '').slice(-12)
  const hit = listCache.get(k)
  if (hit && Date.now() - hit.at < 60_000) return hit.rows
  const raw = firstJson<unknown>(await call(['workflows', 'ls', '-o', 'json'], key, 90_000))
  const rows = (Array.isArray(raw) ? raw : (raw as { workflows?: unknown[] }).workflows ?? []) as WorkflowRow[]
  listCache.set(k, { at: Date.now(), rows })
  return rows
}

/** The curated names, from Settings or STUDIO_WORKFLOWS. */
export function curatedNames(): string[] {
  return (effectiveSettings().workflowCollection ?? []).filter(Boolean)
}

const SAFE_NAME = /^[A-Za-z0-9_.-]{1,120}$/
function checkName(name: string): string {
  if (!SAFE_NAME.test(name)) throw new KbError(400, 'invalid workflow name')
  return name
}

/**
 * The cluster and bucket pickers hold an object so conditional fields can
 * read `inputs.resource.schedulerType`; the platform wants the pw:// string
 * and expands it itself at run time.
 */
export function resolveStudioRefs(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(resolveStudioRefs)
  if (v && typeof v === 'object') {
    const ref = (v as { _studioRef?: unknown })._studioRef
    if (typeof ref === 'string') return ref
    return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, resolveStudioRefs(x)]))
  }
  return v
}

const runChecked = new Map<string, number>()
const iconCache = new Map<string, { at: number; type: string; body: Buffer }>()

export async function workflowsTabRoutes(app: FastifyInstance): Promise<void> {
  const viewer = (req: { user?: { id?: string; username?: string } }) => {
    const { key, own } = platformKeyFor(req.user?.id)
    if (!key) throw new KbError(409, 'No platform credential: add your ACTIVATE API key under Settings, Model access.')
    return { key, own, username: req.user?.username ?? null }
  }

  // What an administrator can pick from: every workflow on the account the
  // Studio is deployed under, which on a site deployment is the site's list.
  app.get('/api/workflows/catalog', async req => {
    const key = gatewayKey() ?? viewer(req).key
    const rows = await listFor(key)
    const curated = new Set(curatedNames())
    return {
      workflows: rows.map(w => ({
        name: w.name,
        displayName: w.displayName || w.name,
        description: w.description ?? '',
        tags: (w.tags ?? []).filter(Boolean),
        curated: curated.has(w.name),
      })).sort((a, b) => a.displayName.localeCompare(b.displayName)),
      missing: [...curated].filter(n => !rows.some(w => w.name === n)),
    }
  })

  // The tiles: the curated set, as this viewer has it.
  app.get('/api/workflows/collection', async req => {
    const names = curatedNames()
    if (!names.length) return { configured: false, workflows: [] }
    const v = viewer(req)
    const mine = await listFor(v.key)
    const byName = new Map(mine.map(w => [w.name, w]))
    // A viewer on their own key may lack a curated record; the owner's copy
    // supplies its title and icon until they add it.
    let owner: Map<string, WorkflowRow> | null = null
    if (v.own && names.some(n => !byName.has(n)) && gatewayKey()) {
      owner = new Map((await listFor(gatewayKey()).catch(() => [])).map(w => [w.name, w]))
    }
    const workflows = names.map(name => {
      const w = byName.get(name) ?? owner?.get(name)
      return {
        name,
        displayName: w?.displayName || name,
        description: w?.description ?? '',
        tags: (w?.tags ?? []).filter(Boolean),
        icon: w?.imageUrl ? `/api/workflows/${encodeURIComponent(name)}/icon` : null,
        configurations: (byName.get(name)?.configurations ?? []).map(c => c.name ?? c.id ?? '').filter(Boolean),
        installed: byName.has(name),
        available: !!w,
      }
    })
    return { configured: true, workflows }
  })

  // A tile's icon. Platform blobs need the credential, so they are served
  // through here; outside URLs are fetched once and cached.
  app.get('/api/workflows/:name/icon', async (req, reply) => {
    const name = checkName((req.params as { name: string }).name)
    const hit = iconCache.get(name)
    if (hit && Date.now() - hit.at < 3_600_000) return reply.type(hit.type).header('Cache-Control', 'private, max-age=3600').send(hit.body)
    const v = viewer(req)
    const rows = [...await listFor(v.key), ...(v.own && gatewayKey() ? await listFor(gatewayKey()).catch(() => []) : [])]
    const url = rows.find(w => w.name === name)?.imageUrl
    if (!url) return reply.status(404).send({ error: 'no icon' })
    const host = new URL(GATEWAY_BASE).origin
    const abs = url.startsWith('/') ? host + url : url
    if (!/^https:\/\//.test(abs)) return reply.status(404).send({ error: 'no icon' })
    const res = await fetch(abs, { headers: abs.startsWith(host) ? { Authorization: `Bearer ${v.key}` } : {}, signal: AbortSignal.timeout(15_000) })
    if (!res.ok) return reply.status(404).send({ error: 'icon unavailable' })
    const type = res.headers.get('content-type') || 'image/png'
    if (!/^image\//.test(type)) return reply.status(404).send({ error: 'not an image' })
    const body = Buffer.from(await res.arrayBuffer())
    iconCache.set(name, { at: Date.now(), type, body })
    return reply.type(type).header('Cache-Control', 'private, max-age=3600').send(body)
  })

  // The form: the workflow's inputs in the platform's dynamic-form shape,
  // and its saved configurations as presets.
  app.get('/api/workflows/:name/form', async req => {
    const name = checkName((req.params as { name: string }).name)
    const v = viewer(req)
    const doc = firstJson<any>(await call(['workflows', 'get', name, '-o', 'json'], v.key, 60_000))
    const yamlDoc = typeof doc?.yaml === 'string' ? parseYaml(doc.yaml) : doc?.yaml
    const inputs = yamlDoc?.on?.execute?.inputs
    let form: unknown = null
    try { form = inputs && workflowHasUserInputs(yamlDoc) ? convertToDynamicForm(inputs) : {} } catch { form = null }
    return {
      name,
      displayName: doc?.displayName || name,
      description: doc?.description ?? '',
      form,
      configurations: (doc?.configurations ?? []).map((c: any) => ({ name: c.name ?? c.id ?? '', inputs: c.inputs ?? {} })),
    }
  })

  // Clusters for the cluster picker, as the pw:// reference the platform
  // expands into the object workflows read (ip, schedulerType, ...).
  app.get('/api/platform/clusters', async req => {
    const v = viewer(req)
    const rows = firstJson<any[]>(await call(['cluster', 'ls', '-o', 'json'], v.key, 90_000))
    return {
      clusters: rows.map(c => ({
        value: c.user ? `pw://${c.user}/${c.name}` : String(c.name),
        name: String(c.name),
        label: c.displayName || c.name,
        status: String(c.status ?? ''),
        scheduler: c.schedulerType ?? null,
        user: c.user ?? null,
        type: c.type ?? null,
      })),
    }
  })

  // Partitions (and the site's own fields and defaults) for one cluster.
  app.get('/api/platform/partitions', async req => {
    const v = viewer(req)
    const cluster = String((req.query as { cluster?: string }).cluster ?? '').replace(/^pw:\/\/[^/]+\//, '')
    const envs = firstJson<any[]>(await call(['environments', 'ls', '-o', 'json'], v.key, 90_000))
    const mine = envs.filter(e => !cluster || String(e.clusterName) === cluster)
    return {
      partitions: mine.map(e => ({
        value: String(e.partitionName ?? e.name),
        label: String(e.displayName ?? e.partitionName ?? e.name),
        state: String(e.partitionState ?? e.status ?? ''),
        availNodes: e.availNodes ?? null,
        totalNodes: e.totalNodes ?? null,
      })),
    }
  })

  app.get('/api/platform/buckets', async req => {
    const v = viewer(req)
    const rows = firstJson<any>(await call(['buckets', 'ls', '-o', 'json'], v.key, 60_000))
    const list: any[] = Array.isArray(rows) ? rows : rows?.buckets ?? []
    return {
      buckets: list.map(b => ({
        value: b.uri ?? (b.user ? `pw://${b.user}/${b.name}` : String(b.name)),
        label: b.displayName || b.name,
      })),
    }
  })

  // Validate or run. Values arrive as the form holds them; the platform's
  // own submission logic drops hidden and ignored fields before they go.
  app.post('/api/workflows/:name/run', async req => {
    const name = checkName((req.params as { name: string }).name)
    const body = req.body as { inputs?: Record<string, unknown>; dryRun?: boolean }
    const v = viewer(req)
    if (!curatedNames().includes(name)) throw new KbError(403, `${name} is not in this Studio's workflow collection`)
    const doc = firstJson<any>(await call(['workflows', 'get', name, '-o', 'json'], v.key, 60_000))
    const yamlDoc = typeof doc?.yaml === 'string' ? parseYaml(doc.yaml) : doc?.yaml
    let values: Record<string, unknown> = body.inputs ?? {}
    try { values = prepareSubmittableValues(yamlDoc, values, v.username ?? '') } catch { /* submit as given */ }
    values = resolveStudioRefs(values) as Record<string, unknown>
    const args = ['workflows', 'run', name, '-i', JSON.stringify(values), '-o', 'json']
    if (body.dryRun) args.push('--dry-run')
    let out: string
    try {
      // Raw CLI errors here: withWorkspace recognizes a stopped workspace by its text.
      out = (await withWorkspace({ cli: (a, t) => cli(a, v.key, t) }, () => cli(args, v.key, 180_000))).value
    } catch (e) {
      const msg = String((e as Error).message ?? e).replace(/^\S+Z \[(ERROR|WARN)\] /gm, '')
      return { ok: false, dryRun: !!body.dryRun, message: msg.slice(0, 2000) }
    }
    if (body.dryRun) return { ok: true, dryRun: true, message: out.trim().slice(0, 2000) || 'Validation passed.' }
    let slug: string | null = null
    try { const r = firstJson<any>(out); slug = String((r?.run ?? r)?.slug ?? (r?.run ?? r)?.id ?? '') || null } catch { /* plain output */ }
    if (slug) recordRun({ slug, workflow: name, owner: v.username })
    return { ok: true, dryRun: false, slug, message: slug ? `Run ${slug} submitted.` : out.trim().slice(0, 2000) }
  })

  // First use: copy the deployment owner's record into the viewer's account.
  app.post('/api/workflows/:name/install', async req => {
    const name = checkName((req.params as { name: string }).name)
    const v = viewer(req)
    if (!curatedNames().includes(name)) throw new KbError(403, `${name} is not in this Studio's workflow collection`)
    const ownerKey = gatewayKey()
    if (!v.own || !ownerKey) throw new KbError(409, 'Nothing to add: this Studio runs workflows with its own credential.')
    const doc = firstJson<any>(await call(['workflows', 'get', name, '-o', 'json'], ownerKey, 60_000))
    const text = typeof doc?.yaml === 'string' ? doc.yaml : dumpYaml(doc?.yaml ?? {})
    const tmp = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'wf-')), `${name}.yaml`)
    fs.writeFileSync(tmp, text)
    try {
      const args = ['workflows', 'create', '--yaml', tmp, name]
      if (doc?.displayName) args.splice(2, 0, '--display-name', String(doc.displayName))
      await call(args, v.key, 60_000)
    } finally { fs.rmSync(path.dirname(tmp), { recursive: true, force: true }) }
    listCache.clear()
    return { ok: true }
  })

  // Recent runs of the curated workflows, from the Studio's run registry.
  // The background watcher polls with the host's credential, which cannot
  // see a run in a viewer's own account; unfinished runs are checked here
  // with the viewer's key, at most every 20 seconds each.
  app.get('/api/workflows/runs', async req => {
    const names = new Set(curatedNames())
    const me = req.user?.username ?? null
    const mine = () => listRuns(100).filter(r => names.has(r.workflow) && (!me || !r.owner || r.owner === me)).slice(0, 30)
    const { key } = platformKeyFor(req.user?.id)
    const open = mine().filter(r => !r.endedAt && !isTerminalRunState(r.state) && Date.now() - (runChecked.get(r.slug) ?? 0) > 20_000)
    if (key && open.length) {
      await Promise.all(open.slice(0, 5).map(async r => {
        runChecked.set(r.slug, Date.now())
        try {
          const doc = firstJson<{ status?: string }>(await call(['workflows', 'runs', 'view', r.slug, '-o', 'json'], key, 30_000))
          if (doc.status && isTerminalRunState(doc.status)) markRunEnded(r.slug, String(doc.status).toLowerCase())
        } catch { /* left to the watcher */ }
      }))
    }
    return { runs: mine() }
  })
}

/** Tests reset the caches between cases. */
export function resetWorkflowsTabForTests(): void { listCache.clear(); iconCache.clear(); runChecked.clear() }
