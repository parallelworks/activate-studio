import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GATEWAY_BASE, INDEX_BASE, PW_CLI } from './config.js'
import { authEnabled } from './auth.js'
import { KbError } from './kb.js'
import * as pwcode from './pwcode.js'
import { remoteDaemon } from './pwcodeBridge.js'
import { gatewayKey } from './chat/gateway.js'
import { resolveUserCred } from './credentials.js'

/**
 * pw code agents at sites: this host's daemon, or the daemon on one of the
 * user's clusters, reached over the bridge (pwcodeBridge.ts). An agent is a
 * pw code session there, given an objective and usually a /goal, so it
 * keeps working turn after turn until an evaluator finds the goal met or
 * the turn bound is reached. The assistant's agent tools and the Agents
 * page both work through this module.
 *
 * The local site is the daemon of the account the Studio runs as. A viewer
 * acting there would act as that account, so it is offered only when
 * sign-in is off (one person's Studio) or AGENT_LOCAL_SITE=1 says the
 * deployment owner wants it. Clusters are reached with the viewer's own
 * platform key, the one their chat uses, so each connection is that user's
 * and acts as them. The deployment's key stands in only when sign-in is off,
 * where the Studio has one user and the key is theirs; on a shared Studio a
 * viewer without a key of their own gets no remote agent. (Delegated sign-in
 * through the platform's OIDC applications will replace stored keys.)
 */

/** The key a viewer's agents act with: their own, or on a one-person
 *  Studio the deployment's. */
export function agentKey(userKey: string | null | undefined): string | null {
  return userKey || (authEnabled() ? null : gatewayKey() || null)
}

export const LOCAL = 'local'
const SITE = /^(?:pw:\/\/)?[A-Za-z0-9_.-]{1,120}(?:\/[A-Za-z0-9_.-]{1,120}){0,2}$/
const SESSION = /^[A-Za-z0-9_.:-]{1,120}$/
/** A workspace as typed: absolute, or under ~, $HOME, or $WORKDIR, in
 *  plain path characters, since it reaches a remote shell. */
const WORKSPACE = /^(?:~|\$HOME|\$WORKDIR)?(?:\/[A-Za-z0-9_.@+-]+)*\/?$/
export const PERMISSION_MODES = ['read-only', 'accept-edits', 'plan'] as const
export const DEFAULT_GOAL_TURNS = 20

export function localSiteAllowed(): boolean {
  return process.env.AGENT_LOCAL_SITE === '1' || (process.env.AGENT_LOCAL_SITE !== '0' && !authEnabled())
}

export function checkSite(site: string): string {
  const s = String(site ?? '').trim()
  if (s === LOCAL) {
    if (!localSiteAllowed()) throw new KbError(403, 'The local site is not offered on this Studio; name one of your clusters.')
    return s
  }
  if (!SITE.test(s)) throw new KbError(400, `not a site name: ${s.slice(0, 80)}`)
  return s
}

export function checkSession(id: string): string {
  if (!SESSION.test(String(id ?? ''))) throw new KbError(400, 'not a session id')
  return String(id)
}

/** The daemon client for a site, acting with `key` on a cluster. */
export function clientFor(site: string, key: string | null): pwcode.DaemonClient {
  const s = checkSite(site)
  if (s === LOCAL) return pwcode.localDaemon
  if (!key) throw new KbError(409, 'Agents on your clusters run with your own platform key: add your ACTIVATE API key under Settings, Model access.')
  return remoteDaemon(s, key)
}

const platformHost = (): string => { try { return new URL(GATEWAY_BASE).hostname } catch { return '' } }

function pwWithKey(args: string[], key: string, timeoutMs = 90_000): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(PW_CLI, args, { timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024, env: { ...process.env, PW_API_KEY: key } },
      (err, out, stderr) => (err ? reject(new KbError(502, String(stderr || err.message).trim().split('\n').pop()?.slice(0, 300) ?? 'pw failed')) : resolve(String(out))))
  })
}

export interface Site { name: string; kind: 'local' | 'cluster'; type?: string }

/** The local site when offered, then the viewer's active clusters. */
export async function listSites(key: string | null): Promise<Site[]> {
  const out: Site[] = localSiteAllowed() ? [{ name: LOCAL, kind: 'local' }] : []
  if (!key) return out
  const host = platformHost()
  const text = await pwWithKey(['clusters', 'ls', '--status', 'active', '-o', 'json', ...(host ? ['--platform-host', host] : [])], key)
  const start = text.search(/[[{]/)
  const parsed = start < 0 ? [] : JSON.parse(text.slice(start, text.lastIndexOf(text[start] === '[' ? ']' : '}') + 1))
  const rows = (Array.isArray(parsed) ? parsed : parsed?.clusters ?? []) as { name?: string; type?: string }[]
  for (const r of rows) if (r?.name && SITE.test(r.name)) out.push({ name: r.name, kind: 'cluster', type: r.type })
  return out
}

/** The site's daemon status, starting the daemon there when none runs. */
export async function probeSite(site: string, key: string | null): Promise<pwcode.DaemonStatus & { usable: boolean; why: string | null }> {
  const c = clientFor(site, key)
  if (c === pwcode.localDaemon) {
    const s = await pwcode.ensureDaemon().catch(() => null)
    return { ...(s ?? {} as pwcode.DaemonStatus), usable: !!s, why: pwcode.unusableReason(s) }
  }
  const s = await c.call<pwcode.DaemonStatus>('GET', '/v1/status')
  const why = pwcode.unusableReason(s)
  return { ...s, usable: !why, why }
}

/** A new agent workspace's default path, unique per start. */
function defaultWorkspace(objective: string): string {
  const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..*$/, '').replace('T', '-')
  const slug = objective.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 32) || 'agent'
  return `~/studio-agents/${stamp}-${slug}`
}

/** The workspace made and given as an absolute path on the site. */
async function makeWorkspace(site: string, key: string | null, ws: string): Promise<string> {
  if (!WORKSPACE.test(ws)) throw new KbError(400, 'workspace: an absolute path, or one under ~, $HOME, or $WORKDIR, in plain path characters')
  if (site === LOCAL) {
    const abs = ws.startsWith('/') ? ws : path.join(INDEX_BASE, 'agents', ws.replace(/^(~|\$HOME|\$WORKDIR)\/?/, ''))
    fs.mkdirSync(abs, { recursive: true })
    return abs
  }
  const host = platformHost()
  const out = await pwWithKey(['ssh', ...(host ? ['--platform-host', host] : []), site, `mkdir -p -- ${ws} && cd -- ${ws} && pwd`], key!)
  const abs = out.trim().split('\n').pop() ?? ''
  if (!abs.startsWith('/')) throw new KbError(502, `could not make the workspace on ${site}`)
  return abs
}

/** A goal as sent to pw code: the user's condition with the turn bound
 *  written into it, since a goal has no limit of its own. */
export function boundedGoal(goal: string, turns: number): string {
  const n = Math.max(1, Math.min(Math.round(turns) || DEFAULT_GOAL_TURNS, 200))
  return `${goal.trim().slice(0, 3600)}\n\nIf this is not met within ${n} turns, stop and say what remains.`
}

export interface StartAgent {
  site: string; key: string | null; objective: string
  goal?: string | null; maxTurns?: number; workspace?: string | null
  model?: string | null; permissionMode?: string | null
}

/** Starts a pw code session at the site on the objective, with the goal
 *  when one is given; the goal keeps it working after the first turn. */
export async function startAgent(o: StartAgent) {
  const objective = String(o.objective ?? '').trim()
  if (!objective) throw new KbError(400, 'an objective is required')
  const mode = o.permissionMode || 'accept-edits'
  if (!(PERMISSION_MODES as readonly string[]).includes(mode)) throw new KbError(400, `permission mode: one of ${PERMISSION_MODES.join(', ')}`)
  const c = clientFor(o.site, o.key)
  const status = await probeSite(o.site, o.key)
  if (!status.usable) throw new KbError(502, `${o.site}: ${status.why}`)
  const workspace = await makeWorkspace(c.site, o.key, (o.workspace || defaultWorkspace(objective)).trim())
  const session = await pwcode.createSession({ workspace, permissionMode: mode, remoteControl: true, ...(o.model ? { model: o.model } : {}) }, c)
  await pwcode.sendMessage(session.id, objective, c)
  let goal: string | null = null
  if (o.goal && o.goal.trim()) {
    goal = boundedGoal(o.goal, o.maxTurns ?? DEFAULT_GOAL_TURNS)
    await pwcode.setGoal(session.id, goal, c)
  }
  return { site: c.site, session: session.id, workspace, host: status.hostname, mode, goal }
}

/** One history item as a line of text. */
export function historyLine(h: pwcode.HistoryItem): string | null {
  const text = (h.text ?? '').trim()
  switch (h.kind) {
    case 'user': return `> ${text.slice(0, 600)}`
    case 'assistant': return text ? text.slice(0, 1500) : null
    case 'tool_call': return `[${h.toolName ?? 'tool'}] ${(h.toolArgs ?? '').slice(0, 300)}`
    case 'tool_result': return text ? `  result: ${text.slice(0, 400)}` : null
    case 'notice': return text ? `! ${text.slice(0, 300)}` : null
    case 'goal_progress': {
      const g = goalProgressOf(h)
      return g ? `[goal] turn ${g.turns ?? '?'}: ${g.outcome || 'not met yet'}${g.reason ? `, ${g.reason}` : ''}${g.err ? ` (${g.err})` : ''}` : null
    }
    default: return null
  }
}

export function goalProgressOf(h: pwcode.HistoryItem): pwcode.GoalProgress | null {
  if (h.kind !== 'goal_progress' || !h.text) return null
  try { return JSON.parse(h.text) as pwcode.GoalProgress } catch { return null }
}

/** Where a session stands: its state, its goal, the last goal check, what
 *  it waits on, and the end of its transcript. */
export async function agentStatus(site: string, key: string | null, id: string, lines = 12) {
  const c = clientFor(site, key)
  const d = await pwcode.getSession(checkSession(id), c)
  let goal: pwcode.GoalStatus | null = null
  try { goal = (await pwcode.sessionState(id, c))?.goal ?? null } catch { /* an older daemon */ }
  const hist = d.history ?? []
  const lastCheck = [...hist].reverse().map(goalProgressOf).find(Boolean) ?? null
  const recent = hist.slice(-Math.max(1, Math.min(lines, 60))).map(historyLine).filter((l): l is string => !!l)
  return {
    site: c.site, session: d.id, status: d.status, workspace: d.workspace, model: d.model,
    goal, lastCheck, approvals: (d.approvals ?? []).map(a => ({ id: a.id, kind: a.kind, text: [a.header || a.kind, a.command || a.question || a.reason].filter(Boolean).join(': ') })),
    recent,
  }
}

export async function agentSessions(site: string, key: string | null) {
  const rows = await pwcode.listSessions(clientFor(site, key))
  return (rows ?? []).map(r => ({ id: r.id, name: r.name ?? null, status: r.status ?? null, workspace: r.workspace ?? null, model: r.model ?? null, updatedAt: r.updatedAt ?? null }))
}

const GOAL_ACTIONS = new Set(['pause', 'resume', 'clear', 'show'])

/** Sets, reads, pauses, resumes, or clears a session's goal. */
export async function agentGoal(site: string, key: string | null, id: string, action: string, condition?: string | null, turns?: number) {
  const c = clientFor(site, key)
  let args: string
  if (action === 'set') {
    if (!condition?.trim()) throw new KbError(400, 'a condition is required to set a goal')
    args = boundedGoal(condition, turns ?? DEFAULT_GOAL_TURNS)
  } else if (GOAL_ACTIONS.has(action)) args = action === 'show' ? '' : action
  else throw new KbError(400, 'action: set, show, pause, resume, or clear')
  return pwcode.setGoal(checkSession(id), args, c)
}

/** Direction reaches a working turn without ending it; a message queues a new turn. */
export async function agentSteer(site: string, key: string | null, id: string, text: string, mode: 'direction' | 'message' = 'direction') {
  const c = clientFor(site, key)
  const clean = String(text ?? '').trim().slice(0, 4000)
  if (!clean) throw new KbError(400, 'text is required')
  if (mode === 'message') return { queued: (await pwcode.sendMessage(checkSession(id), clean, c)).queued }
  const r = await pwcode.steerSession(checkSession(id), clean, c)
  // No working turn to take direction: send it as the next message.
  if (!r?.delivered) return { queued: (await pwcode.sendMessage(id, clean, c)).queued, asMessage: true }
  return { delivered: true }
}

export async function agentApprove(site: string, key: string | null, id: string, approval: string, allowed: boolean, denyMessage?: string) {
  const c = clientFor(site, key)
  if (!SESSION.test(String(approval ?? ''))) throw new KbError(400, 'not an approval id')
  await pwcode.answerApproval(checkSession(id), approval, { allowed, ...(allowed ? {} : { denyMessage: denyMessage || 'Denied from the Studio.' }) }, c)
  return { ok: true }
}

export async function agentStop(site: string, key: string | null, id: string) {
  await pwcode.interruptSession(checkSession(id), clientFor(site, key))
  return { ok: true }
}

export async function agentSiteRoutes(app: FastifyInstance): Promise<void> {
  const keyOf = (req: { user?: { id?: string } }) => {
    const cred = req.user?.id ? resolveUserCred(req.user.id) : null
    return agentKey(cred && !cred.baseUrl ? cred.key : null)
  }
  const p = (req: { params: unknown }) => req.params as { site: string; id: string; approval: string }
  const b = (req: { body: unknown }) => (req.body ?? {}) as Record<string, unknown>

  app.get('/api/agent-sites', async req => ({ sites: await listSites(keyOf(req)), localOffered: localSiteAllowed() }))
  app.get('/api/agent-sites/:site/status', async req => probeSite(p(req).site, keyOf(req)))
  app.get('/api/agent-sites/:site/sessions', async req => ({ sessions: await agentSessions(p(req).site, keyOf(req)) }))
  app.post('/api/agent-sites/:site/sessions', async req => {
    const x = b(req)
    return startAgent({ site: p(req).site, key: keyOf(req), objective: String(x.objective ?? ''), goal: x.goal ? String(x.goal) : null,
      maxTurns: Number(x.maxTurns) || undefined, workspace: x.workspace ? String(x.workspace) : null,
      model: x.model ? String(x.model) : null, permissionMode: x.permissionMode ? String(x.permissionMode) : null })
  })
  app.get('/api/agent-sites/:site/sessions/:id', async req => agentStatus(p(req).site, keyOf(req), p(req).id, Number((req.query as { lines?: string }).lines) || 30))
  app.post('/api/agent-sites/:site/sessions/:id/goal', async req => {
    const x = b(req)
    return agentGoal(p(req).site, keyOf(req), p(req).id, String(x.action ?? ''), x.condition ? String(x.condition) : null, Number(x.maxTurns) || undefined)
  })
  app.post('/api/agent-sites/:site/sessions/:id/steer', async req => {
    const x = b(req)
    return agentSteer(p(req).site, keyOf(req), p(req).id, String(x.text ?? ''), x.mode === 'message' ? 'message' : 'direction')
  })
  app.post('/api/agent-sites/:site/sessions/:id/approvals/:approval', async req => {
    const x = b(req)
    return agentApprove(p(req).site, keyOf(req), p(req).id, p(req).approval, x.allowed === true, x.denyMessage ? String(x.denyMessage) : undefined)
  })
  app.post('/api/agent-sites/:site/sessions/:id/interrupt', async req => agentStop(p(req).site, keyOf(req), p(req).id))
}
