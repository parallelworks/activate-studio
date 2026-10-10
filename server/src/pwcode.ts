import { execFile } from 'node:child_process'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { PW_CLI } from './config.js'

/**
 * A client for the local pw code daemon, the process that hosts pw code
 * sessions for one Unix user. It listens on a Unix socket only that user
 * can open, so a request needs no credential: whoever reaches the socket
 * is the owner. The Studio drives agents through it instead of one-shot
 * `pw code -p` runs, which gives it each session's live history, its
 * approval requests, and a way to answer or interrupt them, and keeps the
 * task's board token inside the request body rather than in a process's
 * arguments, where `ps` would show it to every user on the host.
 *
 * The API is versioned by an integer the daemon reports; the CLI itself
 * checks it by strict equality, and so does this client, because a
 * session request the daemon misreads would run with the wrong settings.
 */

export const PROTOCOL_VERSION = 2

export interface DaemonStatus {
  protocolVersion: number; version: string; pid: number; hostname: string
  sessions: number; remoteStart: boolean; remoteMaxPermissionMode: string
}
export interface HistoryItem { kind: string; text?: string; toolName?: string; toolArgs?: string; fromMain?: boolean }
export interface ApprovalRequest {
  id: string; kind: string; header?: string; question?: string; command?: string; reason?: string; mode?: string
}
export interface SessionDetail {
  id: string; status: 'idle' | 'running' | 'awaiting_approval' | 'live_elsewhere' | string
  messages: number; history: HistoryItem[] | null; approvals: ApprovalRequest[] | null
  workspace: string; model: string; remoteControl?: boolean
}
export interface McpServerStatus { name: string; connected: boolean; connecting: boolean; disabled?: boolean; err?: string; tools?: unknown[] | null }
export interface CreateSessionRequest {
  workspace: string; model?: string; allocation?: string; permissionMode?: string
  agent?: string; agentsJson?: string; allowedTools?: string[]; remoteControl?: boolean
}
export interface ApprovalAnswer { allowed: boolean; denyMessage?: string }

/** The daemon's socket: $XDG_STATE_HOME/pw/code-<hostname>.sock, the
 *  path the CLI uses. PW_CODE_SOCKET overrides it (tests, and a daemon
 *  run under another state directory). */
export function socketPath(): string {
  if (process.env.PW_CODE_SOCKET) return process.env.PW_CODE_SOCKET
  const state = process.env.XDG_STATE_HOME || path.join(os.homedir(), '.local', 'state')
  return path.join(state, 'pw', `code-${os.hostname()}.sock`)
}

export class DaemonError extends Error {
  constructor(message: string, readonly status = 0) { super(message) }
}

function call<T>(method: string, route: string, body?: unknown, timeoutMs = 30_000): Promise<T> {
  return new Promise((resolve, reject) => {
    const payload = body === undefined ? undefined : JSON.stringify(body)
    const req = http.request({
      socketPath: socketPath(), path: route, method, timeout: timeoutMs,
      headers: { Host: 'daemon', Accept: 'application/json', ...(payload ? { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(payload) } : {}) },
    }, res => {
      const chunks: Buffer[] = []
      res.on('data', c => chunks.push(c))
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8')
        const status = res.statusCode ?? 0
        let parsed: unknown = null
        try { parsed = text ? JSON.parse(text) : null } catch { /* plain text error */ }
        if (status >= 400) {
          const d = parsed as { detail?: string; title?: string } | null
          reject(new DaemonError(`pw code daemon: ${d?.detail || d?.title || text.slice(0, 200) || status}`, status))
          return
        }
        resolve(parsed as T)
      })
    })
    req.on('timeout', () => req.destroy(new DaemonError('pw code daemon did not answer in time')))
    req.on('error', e => reject(e instanceof DaemonError ? e : new DaemonError(`pw code daemon unreachable: ${(e as NodeJS.ErrnoException).code ?? e.message}`)))
    if (payload) req.write(payload)
    req.end()
  })
}

/** The daemon's status, or null when nothing answers on the socket. */
export async function daemonStatus(): Promise<DaemonStatus | null> {
  try { return await call<DaemonStatus>('GET', '/v1/status', undefined, 5_000) } catch { return null }
}

/** Why the daemon cannot be used, or null when it can. */
export function unusableReason(s: DaemonStatus | null): string | null {
  if (!s) return 'no pw code daemon answers on this host'
  if (s.protocolVersion !== PROTOCOL_VERSION) return `the pw code daemon speaks protocol ${s.protocolVersion}; this Studio speaks ${PROTOCOL_VERSION}`
  return null
}

let starting: Promise<DaemonStatus> | null = null

/**
 * The daemon, started if none is running. It is started pinned to the
 * Studio's own platform context: an unpinned daemon binds to whatever
 * context is current for the user, which on a host that also signs in to
 * another platform is the wrong one.
 */
export function ensureDaemon(): Promise<DaemonStatus> {
  starting ??= (async () => {
    let s = await daemonStatus()
    if (!s) {
      const ctx = process.env.PW_CONTEXT
      await new Promise<void>((resolve, reject) => {
        execFile(PW_CLI, ['code', 'daemon', 'start', ...(ctx ? ['--context', ctx] : [])], { timeout: 30_000 },
          (err, _out, stderr) => (err ? reject(new DaemonError(`could not start the pw code daemon: ${String(stderr || err.message).trim().slice(0, 300)}`)) : resolve()))
      })
      for (let i = 0; i < 40 && !s; i++) {
        await new Promise(r => setTimeout(r, 250))
        s = await daemonStatus()
      }
    }
    const why = unusableReason(s)
    if (why) throw new DaemonError(why)
    return s!
  })().finally(() => { starting = null })
  return starting
}

export const createSession = (req: CreateSessionRequest) => call<SessionDetail>('POST', '/v1/sessions', req)
export const getSession = (id: string) => call<SessionDetail>('GET', `/v1/sessions/${encodeURIComponent(id)}`)
export const sessionMcp = (id: string) => call<McpServerStatus[] | { servers?: McpServerStatus[] } | null>('GET', `/v1/sessions/${encodeURIComponent(id)}/mcp`)
export const sendMessage = (id: string, text: string) => call<{ queued: boolean; turnId: string }>('POST', `/v1/sessions/${encodeURIComponent(id)}/messages`, { text })
export const answerApproval = (id: string, approvalId: string, answer: ApprovalAnswer) =>
  call<unknown>('POST', `/v1/sessions/${encodeURIComponent(id)}/approvals/${encodeURIComponent(approvalId)}`, answer)
/** Steers a running turn: the text reaches the agent mid-turn, the way a
 *  person types into a working pw code session, without ending the turn. */
export const steerSession = (id: string, text: string) => call<{ delivered: boolean }>('POST', `/v1/sessions/${encodeURIComponent(id)}/direction`, { text })
export const sessionState = (id: string) => call<{ inputTokens?: number | null; outputTokens?: number | null } | null>('GET', `/v1/sessions/${encodeURIComponent(id)}/state`)
export const interruptSession = (id: string) => call<unknown>('POST', `/v1/sessions/${encodeURIComponent(id)}/interrupt`, {})

/** Waits until every MCP server in the session has connected or failed,
 *  as the CLI's one-shot mode does before its first message: a turn that
 *  starts earlier runs without the tools the session was created with.
 *  "Not connecting" is not enough: right after the session is created a
 *  server has not started connecting yet, and reads as neither. */
export async function waitForMcp(id: string, timeoutMs = 20_000): Promise<McpServerStatus[]> {
  const until = Date.now() + timeoutMs
  for (;;) {
    let servers: McpServerStatus[] = []
    try {
      const r = await sessionMcp(id)
      servers = Array.isArray(r) ? r : (r?.servers ?? [])
    } catch { /* an older daemon without the route: go ahead */ return [] }
    const pending = servers.some(s => s.connecting || (!s.connected && !s.err && !s.disabled))
    if (!pending || Date.now() > until) return servers
    await new Promise(r => setTimeout(r, 250))
  }
}
