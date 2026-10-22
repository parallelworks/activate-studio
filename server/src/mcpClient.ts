import { effectiveSettings } from './settings.js'
import type { ToolSpec } from './chat/tools.js'
import { networkFailure, unreachableMessage } from './net.js'

/**
 * The other half of MCP: this Studio as a client of remote MCP servers.
 *
 * /api/mcp publishes this corpus to outside agents. This module is the
 * inverse, letting the deployment attach servers somebody else runs
 * (HubSpot, a ticketing system, an internal service) so their tools join
 * the assistant's own. The corpus tools and the remote tools then sit in
 * one loop, which is what makes "which of these opportunities has a
 * proposal in flight" answerable in a single turn.
 *
 * Remote HTTP servers only. A stdio server would mean this deployment
 * spawning processes named in its settings, which is a much larger
 * surface than fetching a URL, and the hosted servers worth attaching
 * (HubSpot among them) all speak HTTP.
 *
 * Two properties worth stating because they are security-relevant.
 * Attaching a server sends whatever the assistant passes as arguments to
 * a third party, so it is a data-egress decision for the operator, not a
 * convenience toggle. And a remote server's tool names and descriptions
 * are text the model reads while deciding what to call, which makes them
 * untrusted input: they are namespaced under the server's name and
 * length-capped here, and the deployment should attach only servers it
 * trusts.
 */

export interface RemoteMcpServer { name: string; url: string; header?: string }

interface Entry { at: number; tools: ToolSpec[]; error: string | null; session: string | null }

const TTL_MS = 60_000
/** Listing a server (initialize, then tools/list) gets this long per step.
 *  A chat turn waits for the listing, so a server that cannot be reached
 *  costs a few seconds once a minute, not the call timeout every turn. */
const LIST_TIMEOUT_MS = 5_000
const cache = new Map<string, Entry>()

/** Separates the server name from the remote tool name. Double underscore
 *  because MCP tool names commonly contain single ones. */
const SEP = '__'

export function remoteServers(): RemoteMcpServer[] {
  return (effectiveSettings().mcpServers ?? []).filter(s => s?.name && s?.url)
}

function headersFor(s: RemoteMcpServer, session: string | null): Record<string, string> {
  const h: Record<string, string> = {
    'Content-Type': 'application/json',
    // Streamable HTTP servers may answer either shape; accept both.
    Accept: 'application/json, text/event-stream',
  }
  if (s.header) {
    const i = s.header.indexOf(':')
    if (i > 0) h[s.header.slice(0, i).trim()] = s.header.slice(i + 1).trim()
  }
  if (session) h['Mcp-Session-Id'] = session
  return h
}

/** One JSON-RPC round trip. Returns the parsed result, or throws. */
async function rpc(s: RemoteMcpServer, method: string, params: unknown, session: string | null, timeoutMs = 20_000): Promise<{ result: any; session: string | null }> {
  const res = await fetch(s.url, {
    method: 'POST',
    headers: headersFor(s, session),
    body: JSON.stringify({ jsonrpc: '2.0', id: Date.now(), method, params }),
    signal: AbortSignal.timeout(timeoutMs),
  })
  const nextSession = res.headers.get('mcp-session-id') ?? session
  if (!res.ok) throw new Error(`${res.status} ${(await res.text()).slice(0, 160)}`)
  const text = await res.text()
  // A streamable-HTTP server may answer a single request as one SSE
  // event rather than a JSON body.
  const payload = text.trimStart().startsWith('event:') || text.trimStart().startsWith('data:')
    ? text.split('\n').filter(l => l.startsWith('data:')).map(l => l.slice(5).trim()).join('')
    : text
  const msg = JSON.parse(payload)
  if (msg.error) throw new Error(String(msg.error.message ?? JSON.stringify(msg.error)).slice(0, 200))
  return { result: msg.result, session: nextSession }
}

async function listFor(s: RemoteMcpServer): Promise<Entry> {
  const hit = cache.get(s.name)
  if (hit && Date.now() - hit.at < TTL_MS) return hit
  let entry: Entry = { at: Date.now(), tools: [], error: null, session: null }
  try {
    const init = await rpc(s, 'initialize', {
      protocolVersion: '2025-06-18',
      capabilities: {},
      clientInfo: { name: 'activate-studio', version: '0.1.0' },
    }, null, LIST_TIMEOUT_MS)
    entry.session = init.session
    const listed = await rpc(s, 'tools/list', {}, entry.session, LIST_TIMEOUT_MS)
    entry.tools = (listed.result?.tools ?? []).slice(0, 60).map((t: any) => ({
      type: 'function' as const,
      function: {
        name: `${s.name}${SEP}${String(t.name ?? '').replace(/[^a-zA-Z0-9_-]/g, '_')}`.slice(0, 64),
        description: `[${s.name}] ${String(t.description ?? '').slice(0, 400)}`,
        parameters: (t.inputSchema ?? { type: 'object', properties: {} }) as Record<string, unknown>,
      },
    }))
  } catch (e) {
    // An unreachable or misconfigured server contributes no tools and
    // never breaks the assistant's own.
    const f = networkFailure(e)
    let host = s.url
    try { host = new URL(s.url).host } catch { /* shown as written */ }
    entry.error = f ? unreachableMessage(host, f).split('. ')[0] + '.' : String((e as Error).message ?? e).slice(0, 200)
  }
  cache.set(s.name, entry)
  return entry
}

/** Tool specs from every attached server, for the assistant's tool list. */
export async function remoteToolSpecs(): Promise<ToolSpec[]> {
  const all = await Promise.all(remoteServers().map(listFor))
  return all.flatMap(e => e.tools)
}

/** Connection state per server, for the Settings page. */
export async function remoteStatus(): Promise<{ name: string; url: string; tools: number; error: string | null }[]> {
  const servers = remoteServers()
  const all = await Promise.all(servers.map(listFor))
  return servers.map((s, i) => ({ name: s.name, url: s.url, tools: all[i].tools.length, error: all[i].error }))
}

/** Connection state as last seen, without contacting any server, for the
 *  status panel's polling. checkedAt is null for a server not contacted
 *  since the start or the last settings change. */
export function cachedRemoteStatus(): { name: string; url: string; tools: number; error: string | null; checkedAt: string | null }[] {
  return remoteServers().map(s => {
    const hit = cache.get(s.name)
    return { name: s.name, url: s.url, tools: hit?.tools.length ?? 0, error: hit?.error ?? null, checkedAt: hit ? new Date(hit.at).toISOString() : null }
  })
}

export function isRemoteTool(name: string): boolean {
  const i = name.indexOf(SEP)
  return i > 0 && remoteServers().some(s => s.name === name.slice(0, i))
}

/** Run one remote tool. The result is flattened to text, since that is
 *  what the assistant's loop passes back to the model. */
export async function callRemoteTool(name: string, args: unknown): Promise<string> {
  const i = name.indexOf(SEP)
  const serverName = name.slice(0, i)
  const toolName = name.slice(i + SEP.length)
  const s = remoteServers().find(x => x.name === serverName)
  if (!s) throw new Error(`no such MCP server: ${serverName}`)
  const entry = await listFor(s)
  const out = await rpc(s, 'tools/call', { name: toolName, arguments: args ?? {} }, entry.session, 60_000)
  const content = out.result?.content
  if (Array.isArray(content)) {
    return content.map((c: any) => (typeof c?.text === 'string' ? c.text : JSON.stringify(c))).join('\n')
  }
  return typeof out.result === 'string' ? out.result : JSON.stringify(out.result ?? {})
}

/** Drop cached listings, so a settings change takes effect at once. */
export function invalidateRemote(): void { cache.clear() }
