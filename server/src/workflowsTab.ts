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
import { entryTitle, githubThumbnail, githubYamlUrls, parseWorkflowEntry, type WorkflowEntry } from './workflowEntries.js'
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


interface MarketRow { slug: string; name?: string; description?: string; imageUrl?: string; type?: string }
const marketCache = new Map<string, { at: number; rows: MarketRow[] }>()
async function marketplaceFor(key: string | null): Promise<MarketRow[]> {
  const k = (key ?? '').slice(-12)
  const hit = marketCache.get(k)
  if (hit && Date.now() - hit.at < 300_000) return hit.rows
  const raw = firstJson<unknown>(await call(['marketplace', 'ls', '-o', 'json'], key, 90_000))
  const rows = ((Array.isArray(raw) ? raw : (raw as { items?: unknown[] }).items ?? []) as MarketRow[]).filter(r => !r.type || r.type === 'workflow')
  marketCache.set(k, { at: Date.now(), rows })
  return rows
}

function entryOf(req: { query: unknown }): WorkflowEntry {
  const e = parseWorkflowEntry(String((req.query as { w?: string }).w ?? ''))
  if (!e) throw new KbError(400, 'invalid workflow: use a name, marketplace/<slug>, or github.com/<owner>/<repo>[/path][@ref]')
  return e
}

interface Definition { yaml: any; displayName: string; description: string; configurations: { name: string; inputs: Record<string, unknown> }[] }

/** A workflow's YAML and details, wherever it is defined. */
async function definitionOf(e: WorkflowEntry, key: string | null): Promise<Definition> {
  if (e.kind === 'account') {
    const doc = firstJson<any>(await call(['workflows', 'get', e.name, '-o', 'json'], key, 60_000))
    return {
      yaml: typeof doc?.yaml === 'string' ? parseYaml(doc.yaml) : doc?.yaml,
      displayName: doc?.displayName || e.name,
      description: doc?.description ?? '',
      configurations: (doc?.configurations ?? []).map((c: any) => ({ name: c.name ?? c.id ?? '', inputs: c.inputs ?? {} })),
    }
  }
  if (e.kind === 'marketplace') {
    const text = await call(['marketplace', 'get', e.slug, '--yaml'], key, 60_000)
    const m = (await marketplaceFor(key).catch(() => [])).find(x => x.slug === e.slug)
    return { yaml: parseYaml(text), displayName: m?.name || e.slug, description: m?.description ?? '', configurations: [] }
  }
  let lastStatus = 0
  for (const url of githubYamlUrls(e)) {
    const res = await fetch(url, { signal: AbortSignal.timeout(20_000) }).catch(() => null)
    if (res?.ok) return { yaml: parseYaml(await res.text()), displayName: entryTitle(e), description: e.entry, configurations: [] }
    lastStatus = res?.status ?? 0
  }
  throw new KbError(lastStatus === 404 ? 404 : 502, lastStatus === 404
    ? `No workflow.yaml at ${e.entry}. Private repositories still run, but their form cannot be shown here.`
    : `Could not read ${e.entry} from GitHub.`)
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
  // When the deployment's credential is missing, expired, or rejected, the
  // viewer's own key lists their account instead, and the answer says so.
  app.get('/api/workflows/catalog', async req => {
    const dep = gatewayKey()
    const mine = platformKeyFor(req.user?.id)
    let rows: WorkflowRow[] | null = null
    let source: 'deployment' | 'viewer' = 'deployment'
    let depError: unknown = null
    if (dep) rows = await listFor(dep).catch(e => { depError = e; return null })
    if (!rows && mine.own && mine.key) { rows = await listFor(mine.key); source = 'viewer' }
    if (!rows) throw depError ?? new KbError(409, 'No platform credential: add your ACTIVATE API key under Settings, Model access.')
    const curated = new Set(curatedNames())
    // Marketplace workflows, listed with whichever key worked; a failure
    // here leaves the account list standing.
    const market = await marketplaceFor(source === 'viewer' ? mine.key : dep).catch(() => [])
    return {
      source,
      marketplace: market.map(m => ({
        name: `marketplace/${m.slug}`,
        displayName: m.name || m.slug,
        description: m.description ?? '',
        curated: curated.has(`marketplace/${m.slug}`),
      })).sort((a, b) => a.displayName.localeCompare(b.displayName)),
      workflows: rows.map(w => ({
        name: w.name,
        displayName: w.displayName || w.name,
        description: w.description ?? '',
        tags: (w.tags ?? []).filter(Boolean),
        curated: curated.has(w.name),
      })).sort((a, b) => a.displayName.localeCompare(b.displayName)),
      missing: [...curated].filter(n => parseWorkflowEntry(n)?.kind === 'account' && !rows.some(w => w.name === n)),
    }
  })

  // The tiles: the curated set, as this viewer has it. Account workflows
  // come from the viewer's list (or the owner's, until they add a copy);
  // marketplace and GitHub entries run as they are and need no copy.
  app.get('/api/workflows/collection', async req => {
    const names = curatedNames()
    if (!names.length) return { configured: false, workflows: [] }
    const v = viewer(req)
    const entries = names.map(n => parseWorkflowEntry(n)).filter((e): e is WorkflowEntry => !!e)
    const accountNames = entries.filter(e => e.kind === 'account').map(e => e.entry)
    const mine = accountNames.length ? await listFor(v.key) : []
    const byName = new Map(mine.map(w => [w.name, w]))
    let owner: Map<string, WorkflowRow> | null = null
    if (v.own && accountNames.some(n => !byName.has(n)) && gatewayKey()) {
      owner = new Map((await listFor(gatewayKey()).catch(() => [])).map(w => [w.name, w]))
    }
    const market = entries.some(e => e.kind === 'marketplace') ? await marketplaceFor(v.key).catch(() => null) : null
    const iconOf = (entry: string) => `/api/workflows/item/icon?w=${encodeURIComponent(entry)}`
    const workflows = entries.map(e => {
      if (e.kind === 'account') {
        const w = byName.get(e.name) ?? owner?.get(e.name)
        return {
          name: e.entry, kind: e.kind,
          displayName: w?.displayName || e.name,
          description: w?.description ?? '',
          tags: (w?.tags ?? []).filter(Boolean),
          icon: w?.imageUrl ? iconOf(e.entry) : null,
          configurations: (byName.get(e.name)?.configurations ?? []).map(c => c.name ?? c.id ?? '').filter(Boolean),
          installed: byName.has(e.name),
          available: !!w,
        }
      }
      if (e.kind === 'marketplace') {
        const m = market?.find(x => x.slug === e.slug)
        return {
          name: e.entry, kind: e.kind,
          displayName: m?.name || e.slug, description: m?.description ?? '', tags: [],
          icon: m?.imageUrl ? iconOf(e.entry) : null, configurations: [],
          installed: true, available: market ? !!m : true,
        }
      }
      return {
        name: e.entry, kind: e.kind,
        displayName: entryTitle(e), description: e.entry, tags: [],
        icon: iconOf(e.entry), configurations: [], installed: true, available: true,
      }
    })
    return { configured: true, workflows }
  })

  // A tile's icon. Platform blobs need the credential, so they are served
  // through here; outside URLs are fetched once and cached.
  app.get('/api/workflows/item/icon', async (req, reply) => {
    const e = entryOf(req)
    const hit = iconCache.get(e.entry)
    if (hit && Date.now() - hit.at < 3_600_000) return reply.type(hit.type).header('Cache-Control', 'private, max-age=3600').send(hit.body)
    const v = viewer(req)
    let url: string | undefined
    if (e.kind === 'account') {
      const rows = [...await listFor(v.key), ...(v.own && gatewayKey() ? await listFor(gatewayKey()).catch(() => []) : [])]
      url = rows.find(w => w.name === e.name)?.imageUrl
    } else if (e.kind === 'marketplace') {
      url = (await marketplaceFor(v.key).catch(() => [])).find(x => x.slug === e.slug)?.imageUrl
    } else url = githubThumbnail(e)
    if (!url) return reply.status(404).send({ error: 'no icon' })
    const host = new URL(GATEWAY_BASE).origin
    const abs = url.startsWith('/') ? host + url : url
    if (!/^https:\/\//.test(abs)) return reply.status(404).send({ error: 'no icon' })
    const res = await fetch(abs, { headers: abs.startsWith(host) ? { Authorization: `Bearer ${v.key}` } : {}, signal: AbortSignal.timeout(15_000) }).catch(() => null)
    if (!res?.ok) return reply.status(404).send({ error: 'icon unavailable' })
    const type = res.headers.get('content-type') || 'image/png'
    if (!/^image\//.test(type)) return reply.status(404).send({ error: 'not an image' })
    const body = Buffer.from(await res.arrayBuffer())
    iconCache.set(e.entry, { at: Date.now(), type, body })
    return reply.type(type).header('Cache-Control', 'private, max-age=3600').send(body)
  })

  // The form: the workflow's inputs in the platform's dynamic-form shape,
  // and its saved configurations as presets. Also how Settings checks a
  // GitHub entry before adding it.
  app.get('/api/workflows/item/form', async req => {
    const e = entryOf(req)
    const v = viewer(req)
    const d = await definitionOf(e, v.key)
    const inputs = d.yaml?.on?.execute?.inputs
    let form: unknown = null
    try { form = inputs && workflowHasUserInputs(d.yaml) ? convertToDynamicForm(inputs) : {} } catch { form = null }
    return { name: e.entry, kind: e.kind, displayName: d.displayName, description: d.description, form, configurations: d.configurations }
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
  app.post('/api/workflows/item/run', async req => {
    const e = entryOf(req)
    const body = req.body as { inputs?: Record<string, unknown>; dryRun?: boolean }
    const v = viewer(req)
    if (!curatedNames().includes(e.entry)) throw new KbError(403, `${e.entry} is not in this Studio's workflow collection`)
    const d = await definitionOf(e, v.key)
    let values: Record<string, unknown> = body.inputs ?? {}
    try { values = prepareSubmittableValues(d.yaml, values, v.username ?? '') } catch { /* submit as given */ }
    values = resolveStudioRefs(values) as Record<string, unknown>
    const args = ['workflows', 'run', e.entry, '-i', JSON.stringify(values), '-o', 'json']
    if (body.dryRun) args.push('--dry-run')
    let out: string
    try {
      // Raw CLI errors here: withWorkspace recognizes a stopped workspace by its text.
      out = (await withWorkspace({ cli: (a, t) => cli(a, v.key, t) }, () => cli(args, v.key, 180_000))).value
    } catch (err) {
      const msg = String((err as Error).message ?? err).replace(/^\S+Z \[(ERROR|WARN)\] /gm, '')
      return { ok: false, dryRun: !!body.dryRun, message: msg.slice(0, 2000) }
    }
    if (body.dryRun) return { ok: true, dryRun: true, message: out.trim().slice(0, 2000) || 'Validation passed.' }
    let slug: string | null = null
    try { const r = firstJson<any>(out); slug = String((r?.run ?? r)?.slug ?? (r?.run ?? r)?.id ?? '') || null } catch { /* plain output */ }
    if (slug) recordRun({ slug, workflow: e.entry, owner: v.username })
    return { ok: true, dryRun: false, slug, message: slug ? `Run ${slug} submitted.` : out.trim().slice(0, 2000) }
  })

  // First use of an account workflow: copy the deployment owner's record
  // into the viewer's account. Marketplace and GitHub entries need none.
  app.post('/api/workflows/item/install', async req => {
    const e = entryOf(req)
    const v = viewer(req)
    if (!curatedNames().includes(e.entry)) throw new KbError(403, `${e.entry} is not in this Studio's workflow collection`)
    if (e.kind !== 'account') throw new KbError(409, 'Marketplace and GitHub workflows run without a copy in your account.')
    const ownerKey = gatewayKey()
    if (!v.own || !ownerKey) throw new KbError(409, 'Nothing to add: this Studio runs workflows with its own credential.')
    const doc = firstJson<any>(await call(['workflows', 'get', e.name, '-o', 'json'], ownerKey, 60_000))
    const text = typeof doc?.yaml === 'string' ? doc.yaml : dumpYaml(doc?.yaml ?? {})
    const tmp = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'wf-')), `${e.name}.yaml`)
    fs.writeFileSync(tmp, text)
    try {
      const args = ['workflows', 'create', '--yaml', tmp, e.name]
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
