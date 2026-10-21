import crypto from 'node:crypto'
import { STATUS_CODES } from 'node:http'
import { platformOrigin } from '../sessions.js'

/**
 * Platform operations the chat calls over the platform API with the
 * viewer's own key. The names, descriptions, parameters, and result fields
 * are the platform web chat's own, so the two chats behave the same. A tool
 * over a GET reads; a tool over a POST changes something, and the chat
 * holds each call to it until the person who asked approves it (see
 * approvals.ts). Nothing here runs a changing call on its own say-so:
 * runPlatformTool is reached for those only from an approved hold.
 */

export interface PlatformTool {
  name: string
  /** The platform API operation id. */
  operation: string
  method: 'GET' | 'POST'
  /** The operation's path; {name} segments are filled from the arguments. */
  path: string
  description: string
  parameters: { type: 'object'; properties: Record<string, Record<string, unknown>>; required?: string[] }
  /** The item fields the model gets. Everything else is dropped, which keeps
   *  results small and keeps fields such as tokens away from the model. */
  fields?: string[]
  /** Path parameters filled from the viewer, never from the model. */
  fromViewer?: string[]
  /** Path parameters the model may leave out, filled from the viewer. */
  defaults?: string[]
  /** Tools the user may run in place of a held call, with the same arguments. */
  instead?: string[]
}

const USER = { type: 'string', description: 'Username of the target user.' }
const clusterName = (verb: string) => ({ type: 'string', description: `The name of the cluster to ${verb}` })

export const PLATFORM_TOOLS: PlatformTool[] = [
  {
    name: 'list_storage',
    operation: 'get-storages',
    method: 'GET',
    path: '/api/storage',
    description: 'List the storage the user can use, such as buckets and file systems: type, cloud, region, size and status.',
    parameters: {
      type: 'object',
      properties: {
        category: { type: 'string', enum: ['buckets', 'disks', 'lustre', 'nfs'], description: 'Only return storages in this category.' },
        type: { type: 'array', items: { type: 'string' }, description: 'Only return storages of these CSP-qualified types, e.g. aws-bucket. Repeatable.' },
        permission: { type: 'string', enum: ['read', 'mount', 'edit'], description: 'Only return storages the requesting user holds at least this access on. Defaults to read.' },
        provisioned: { type: 'boolean', description: 'Only return provisioned storages.' },
        scope: { type: 'string', description: 'Whose storages to list: mine (default) or user:<username>. Naming another user requires administrator access.' },
      },
    },
    fields: ['name', 'displayName', 'category', 'type', 'user', 'csp', 'region', 'sizeGb', 'status', 'statusDescription', 'attached', 'uri', 'group'],
  },
  {
    name: 'start_cluster',
    operation: 'provision-cluster',
    method: 'POST',
    path: '/api/organizations/{organization}/users/{user}/clusters/{clusterName}/provision',
    description: 'Start a cloud cluster that is off, provisioning its controller and resources. It starts billing. Use resume_cluster for one that is stopped. Leave user out for the user\'s own cluster.',
    parameters: { type: 'object', properties: { user: USER, clusterName: clusterName('provision') }, required: ['clusterName'] },
    fromViewer: ['organization'],
    defaults: ['user'],
  },
  {
    name: 'resume_cluster',
    operation: 'resume-cluster',
    method: 'POST',
    path: '/api/organizations/{organization}/users/{user}/clusters/{clusterName}/resume',
    description: 'Resume a stopped cloud cluster, restarting its controller. It starts billing again. Leave user out for the user\'s own cluster.',
    parameters: { type: 'object', properties: { user: USER, clusterName: clusterName('resume') }, required: ['clusterName'] },
    fromViewer: ['organization'],
    defaults: ['user'],
  },
  {
    name: 'stop_cluster',
    operation: 'stop-cluster',
    method: 'POST',
    path: '/api/organizations/{organization}/users/{user}/clusters/{clusterName}/stop',
    description: 'Stop a running cloud cluster\'s controller to stop its compute billing. It keeps its disks and settings and can be resumed. Leave user out for the user\'s own cluster.',
    parameters: { type: 'object', properties: { user: USER, clusterName: clusterName('stop') }, required: ['clusterName'] },
    fromViewer: ['organization'],
    defaults: ['user'],
    instead: ['destroy_cluster'],
  },
  {
    name: 'destroy_cluster',
    operation: 'destroy-cluster',
    method: 'POST',
    path: '/api/organizations/{organization}/users/{user}/clusters/{clusterName}/destroy',
    description: 'Destroy a cloud cluster\'s provisioned resources, including its disks, so nothing bills. Its definition stays, so it can be started again later from scratch. Use stop_cluster to keep its disks. Leave user out for the user\'s own cluster.',
    parameters: { type: 'object', properties: { user: USER, clusterName: clusterName('destroy') }, required: ['clusterName'] },
    fromViewer: ['organization'],
    defaults: ['user'],
    instead: ['stop_cluster'],
  },
  {
    name: 'cancel_workflow_run',
    operation: 'cancel-workflow-run',
    method: 'POST',
    path: '/api/workflow-runs/{slug}/cancel',
    description: 'Cancel a running workflow run, by the slug workflow_runs gives it.',
    parameters: { type: 'object', properties: { slug: { type: 'string', description: 'The slug identifier of the workflow run to cancel.' } }, required: ['slug'] },
  },
]

/** What a changing tool's approval card says it does. */
const EFFECT: Record<string, { title: (a: Record<string, unknown>) => string; reason: string; insteadLabel?: string; insteadNote?: string }> = {
  start_cluster: { title: a => `Start cluster ${a.clusterName}?`, reason: 'Provisions its controller and resources. It starts billing.' },
  resume_cluster: { title: a => `Resume cluster ${a.clusterName}?`, reason: 'Restarts its controller. It starts billing again.' },
  stop_cluster: {
    title: a => `Stop cluster ${a.clusterName}?`, reason: 'Stops its controller to stop its compute billing. Its disks and settings are kept, and it can be resumed.',
    insteadLabel: 'Stop it instead', insteadNote: 'Keeps its disks and settings, so it can be resumed.',
  },
  destroy_cluster: {
    title: a => `Destroy cluster ${a.clusterName}?`, reason: 'Destroys its provisioned resources, including its disks, so nothing bills. Its definition stays, so it can be started again from scratch.',
    insteadLabel: 'Destroy it instead', insteadNote: 'Deletes its disks too, so nothing bills.',
  },
  cancel_workflow_run: { title: a => `Cancel run ${a.slug}?`, reason: 'Cancels the workflow run.' },
}

/** Caps what one call sends back to the model, as the platform chat does. */
export const MAX_RESULT_BYTES = 16 * 1024
const CALL_TIMEOUT_MS = 20_000
/** Appended to the description the model reads for a tool that asks first. */
export const ASKS_FIRST_NOTE = ' The user approves each call before it runs.'

const byName = new Map(PLATFORM_TOOLS.map(t => [t.name, t]))

export function platformTool(name: string): PlatformTool | undefined { return byName.get(name) }
export function isPlatformTool(name: string): boolean { return byName.has(name) }
/** Whether calls to the tool change something, and so wait for the user's approval. */
export function asksFirst(name: string): boolean { return byName.get(name)?.method === 'POST' }
export const ASKS_FIRST_TOOLS: string[] = PLATFORM_TOOLS.filter(t => t.method === 'POST').map(t => t.name)

/** The tools the user may run in place of a held call to name. */
export function insteadOf(name: string): string[] {
  return (byName.get(name)?.instead ?? []).filter(n => byName.has(n))
}

/** The definitions the model reads. */
export function platformToolSpecs(): { type: 'function'; function: { name: string; description: string; parameters: Record<string, unknown> } }[] {
  return PLATFORM_TOOLS.map(t => ({
    type: 'function' as const,
    function: { name: t.name, description: t.description + (t.method === 'POST' ? ASKS_FIRST_NOTE : ''), parameters: t.parameters },
  }))
}

/**
 * Identifies what a call to the tool does, so an approval given under one
 * definition of a tool does not run a changed one: its name, operation,
 * method, path, parameters, and which parameters come from the viewer.
 */
export function toolHash(name: string): string {
  const t = byName.get(name)
  if (!t) return ''
  const parts = [t.name, t.operation, t.method, t.path, JSON.stringify(t.parameters), (t.fromViewer ?? []).join(','), (t.defaults ?? []).join(',')]
  return crypto.createHash('sha256').update(parts.join('\0')).digest('hex')
}

export type CallArgs = Record<string, string | boolean | string[]>

/**
 * A call's arguments in canonical form, or what is wrong with them. Only
 * declared parameters are kept, a null or empty value counts as left out,
 * a parameter the model may leave out is filled from the viewer, and the
 * keys are sorted, so the same call always has the same text and a
 * different call never does.
 */
export function prepareArgs(name: string, raw: unknown, viewerUsername: string | null): { args: CallArgs; canonical: string } | { problem: string } {
  const t = byName.get(name)
  if (!t) return { problem: `Unknown tool ${name}` }
  let input: unknown = raw
  if (typeof raw === 'string') {
    if (!raw.trim()) input = {}
    else {
      try { input = JSON.parse(raw) } catch { return { problem: 'The arguments were not valid JSON.' } }
    }
  }
  if (!input || typeof input !== 'object' || Array.isArray(input)) return { problem: 'The arguments were not valid JSON.' }
  const given = input as Record<string, unknown>
  const pathParams = [...t.path.matchAll(/\{(\w+)\}/g)].map(m => m[1])
  const args: CallArgs = {}
  for (const [key, schema] of Object.entries(t.parameters.properties)) {
    const v = given[key]
    if (v === null || v === undefined || v === '') continue
    if (schema.type === 'array') {
      const list = (Array.isArray(v) ? v : [v]).map(x => typeof x === 'string' || typeof x === 'number' ? String(x) : null)
      if (list.some(x => x === null)) return { problem: `The ${key} argument must be a list of strings.` }
      if (list.length) args[key] = list as string[]
      continue
    }
    if (schema.type === 'boolean') {
      if (typeof v === 'boolean') args[key] = v
      else if (v === 'true' || v === 'false') args[key] = v === 'true'
      else return { problem: `The ${key} argument must be true or false.` }
      continue
    }
    if (typeof v !== 'string' && typeof v !== 'number' && typeof v !== 'boolean') return { problem: `The ${key} argument must be a string, number or true/false.` }
    const s = String(v).trim()
    if (!s) continue
    if (Array.isArray(schema.enum) && !schema.enum.includes(s)) return { problem: `The ${key} argument must be one of ${(schema.enum as string[]).join(', ')}.` }
    args[key] = s
  }
  for (const p of t.defaults ?? []) {
    if (args[p] !== undefined) continue
    if (!viewerUsername) return { problem: `The ${p} argument is required.` }
    args[p] = viewerUsername
  }
  for (const p of pathParams) {
    if ((t.fromViewer ?? []).includes(p)) continue
    if (typeof args[p] !== 'string' || !args[p]) return { problem: `The ${p} argument is required.` }
  }
  const canonical = JSON.stringify(Object.fromEntries(Object.keys(args).sort().map(k => [k, args[k]])))
  return { args, canonical }
}

/** The call as the approval card shows it: the tool and every argument it will run with. */
export function callSignature(name: string, args: CallArgs): string {
  const t = byName.get(name)
  const order = t ? Object.keys(t.parameters.properties) : Object.keys(args)
  const shown = order.filter(k => args[k] !== undefined).map(k => `${k}: ${JSON.stringify(args[k])}`)
  return `${name}(${shown.join(', ')})`
}

export interface ApprovalAlternative { tool: string; label: string; description?: string }

/** The approval part the chat draws for a held call, in the chat package's ApprovalPart shape. */
export function approvalPart(name: string, args: CallArgs, toolCallId: string, expiresAt: string): Record<string, unknown> {
  const e = EFFECT[name]
  const instead: ApprovalAlternative[] = insteadOf(name).map(other => ({
    tool: other, label: EFFECT[other]?.insteadLabel ?? `Run ${other} instead`, ...(EFFECT[other]?.insteadNote ? { description: EFFECT[other].insteadNote } : {}),
  }))
  return {
    kind: 'approval',
    id: toolCallId,
    approvalKind: 'confirm',
    toolName: name,
    subject: 'call',
    header: e ? e.title(args) : `Run ${name}?`,
    command: callSignature(name, args),
    ...(e ? { reason: e.reason } : {}),
    canAllowDirectory: false,
    expiresAt,
    // Not part of the package's ApprovalPart: the tools the user may run in
    // its place, which the Studio draws beside the card.
    ...(instead.length ? { instead } : {}),
  }
}

const sessionCache = new Map<string, { at: number; v: { username: string; organization: string } | null }>()
const SESSION_TTL_MS = 5 * 60_000
const keyId = (key: string) => crypto.createHash('sha256').update(key).digest('hex').slice(0, 24)

/** The platform account a key belongs to: its username and organization name. */
export async function viewerAccount(key: string): Promise<{ username: string; organization: string } | null> {
  const id = keyId(key)
  const hit = sessionCache.get(id)
  if (hit && Date.now() - hit.at < SESSION_TTL_MS) return hit.v
  const origin = platformOrigin()
  if (!origin) return null
  let v: { username: string; organization: string } | null = null
  try {
    const res = await fetch(`${origin}/api/auth/session`, { headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' }, signal: AbortSignal.timeout(CALL_TIMEOUT_MS) })
    if (res.ok) {
      const body = await res.json() as { username?: unknown; organization?: unknown }
      if (typeof body?.username === 'string' && body.username) {
        v = { username: body.username, organization: typeof body.organization === 'string' ? body.organization : '' }
      }
    }
  } catch { /* unreachable: nothing to fill from */ }
  if (v) sessionCache.set(id, { at: Date.now(), v })
  return v
}

/** Tests reset the account cache between cases. */
export function resetPlatformToolsForTests(): void { sessionCache.clear() }

function isScalar(v: unknown): boolean {
  return v === null || ['string', 'number', 'boolean'].includes(typeof v)
}

/** A result reduced to the tool's fields: each item keeps only those, and a
 *  wrapping object keeps its scalars. */
export function projectResult(tool: PlatformTool, value: unknown): unknown {
  if (value && typeof value === 'object' && !Array.isArray(value)) delete (value as Record<string, unknown>).$schema
  const fields = tool.fields ?? []
  const items = (v: unknown): unknown => {
    if (!fields.length) return v
    if (Array.isArray(v)) return v.map(items)
    if (v && typeof v === 'object') {
      const o = v as Record<string, unknown>
      return Object.fromEntries(fields.filter(f => o[f] !== undefined && o[f] !== null).map(f => [f, o[f]]))
    }
    return v
  }
  if (Array.isArray(value)) return items(value)
  if (value && typeof value === 'object' && fields.length) {
    const o = value as Record<string, unknown>
    // A listing wrapped in an object: the list's items are projected and the
    // wrapper keeps its scalar fields, such as a count.
    const listKey = Object.keys(o).find(k => Array.isArray(o[k]))
    if (listKey) return Object.fromEntries(Object.entries(o).filter(([k, v]) => k === listKey || isScalar(v)).map(([k, v]) => [k, k === listKey ? items(v) : v]))
    return items(value)
  }
  return value
}

export function truncateResult(s: string): string {
  if (Buffer.byteLength(s) <= MAX_RESULT_BYTES) return s
  return Buffer.from(s).subarray(0, MAX_RESULT_BYTES).toString('utf8').replace(/�+$/, '') + '\n[truncated: the result was longer than the tool limit]'
}

/**
 * Runs one platform operation with the key and returns the text for the
 * model and whether it failed. A failure's text is the platform's own
 * message from its problem document, which is safe to show the user.
 */
export async function runPlatformTool(name: string, args: CallArgs, key: string): Promise<{ result: string; failed: boolean }> {
  const t = byName.get(name)
  if (!t) return { result: `Unknown tool ${name}`, failed: true }
  const origin = platformOrigin()
  if (!origin) return { result: 'No platform address is configured.', failed: true }
  const fill: Record<string, string> = {}
  if ((t.fromViewer ?? []).includes('organization')) {
    const account = await viewerAccount(key)
    if (!account?.organization) return { result: 'The organization for this call could not be found.', failed: true }
    fill.organization = account.organization
  }
  let path = t.path
  const query = new URLSearchParams()
  for (const [k, v] of Object.entries(args)) {
    if (path.includes(`{${k}}`)) { path = path.replaceAll(`{${k}}`, encodeURIComponent(String(v))); continue }
    if (Array.isArray(v)) for (const x of v) query.append(k, x)
    else query.set(k, String(v))
  }
  for (const [k, v] of Object.entries(fill)) path = path.replaceAll(`{${k}}`, encodeURIComponent(v))
  if (/\{\w+\}/.test(path)) return { result: `The ${/\{(\w+)\}/.exec(path)![1]} argument is required.`, failed: true }
  const qs = query.toString()
  let res: Response
  try {
    res = await fetch(`${origin}${path}${qs ? `?${qs}` : ''}`, {
      method: t.method,
      headers: { Authorization: `Bearer ${key}`, Accept: 'application/json' },
      signal: AbortSignal.timeout(CALL_TIMEOUT_MS),
    })
  } catch (e) {
    return { result: `The platform could not be reached: ${String((e as Error).message ?? e).slice(0, 160)}`, failed: true }
  }
  const text = await res.text().catch(() => '')
  if (res.status >= 400) {
    let problem: { message?: unknown; detail?: unknown } = {}
    try { problem = JSON.parse(text) } catch { /* not a problem document */ }
    if (typeof problem?.message === 'string' && problem.message) return { result: problem.message, failed: true }
    if (typeof problem?.detail === 'string' && problem.detail) return { result: problem.detail, failed: true }
    return { result: STATUS_CODES[res.status] ?? `status ${res.status}`, failed: true }
  }
  if (!text.trim()) return { result: 'Done.', failed: false }
  let value: unknown
  try { value = JSON.parse(text) } catch { return { result: 'The lookup returned a result that could not be read.', failed: true } }
  return { result: truncateResult(JSON.stringify(projectResult(t, value))), failed: false }
}
