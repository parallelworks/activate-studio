/** The Studio's tools in pw code on the person's other machines
 *  (/api/mcp/bridge…): what the Studio knows of each machine, and the
 *  setup it runs there as them over pw ssh. */

/** What the Studio says of its tools on one machine for the viewer. */
export interface MachineTools {
  /** set: pw code there has this Studio; declined: they said not to add it
   *  there; offer: it can be added; unavailable: the Studio cannot, why says. */
  state: 'set' | 'declined' | 'offer' | 'unavailable'
  machine: string
  machineId: string
  why?: string
  available?: boolean
}

/** POST /api/mcp/bridge/setup's answer. */
export interface SetupResult {
  done: boolean
  /** An entry for another Studio is there: replace it only once asked. */
  ask?: boolean
  current?: string
  changed?: boolean
  replaced?: boolean
  already?: boolean
  machine?: string
  tools?: number
  worked?: boolean
  why?: string
  /** now: the session in front has them already; next: sessions started
   *  there from now on do. */
  live?: 'now' | 'next'
  liveWhy?: string
}

export async function runSetup(body: { conversationId?: string; machineId?: string; replace?: boolean; check?: boolean }): Promise<SetupResult> {
  const res = await fetch('/api/mcp/bridge/setup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const out = await res.json().catch(() => ({})) as SetupResult & { error?: string; detail?: string }
  if (!res.ok) throw new Error(out.error ?? out.detail ?? `HTTP ${res.status}`)
  return out
}

/** Whether the person wants the tools added on machine; not wanting them
 *  keeps the Studio from offering them there again until they choose. */
export async function setMachineTools(machineId: string, add: boolean): Promise<void> {
  await fetch('/api/mcp/bridge/prefs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ machineId, add }) }).catch(() => {})
}

const AUTO = 'ade-studio-tools-auto:'

/** Marks a chat just continued with "Add this Studio's tools" ticked, so its
 *  setup card runs the setup as it opens. */
export function markAutoSetup(conversationId: string): void {
  try { sessionStorage.setItem(AUTO + conversationId, '1') } catch { /* the card offers the button instead */ }
}

/** Whether the chat's card should run the setup now, once. */
export function takeAutoSetup(conversationId: string): boolean {
  try {
    const on = sessionStorage.getItem(AUTO + conversationId) === '1'
    sessionStorage.removeItem(AUTO + conversationId)
    return on
  } catch { return false }
}

/** What a finished setup says, in a sentence or two. */
export function setupSaid(r: SetupResult, where: string, inChat = true): string {
  if (!r.worked) return r.why || `The Studio could not check pw code on ${where}.`
  const n = r.tools ? ` (${r.tools} of them)` : ''
  const how = r.already || r.changed === false
    ? `pw code on ${where} already has this Studio's tools${n}, as you.`
    : `pw code on ${where} now has this Studio's tools${n}, as you, through its own ACTIVATE sign-in; no key was made.`
  const live = r.live === 'now'
    ? ' This session has them now.'
    : ` Sessions you start there from now on have them${r.liveWhy ? `; this one could not take them now (${r.liveWhy.replace(/\.$/, '')})` : inChat ? `; one already open, this one included, ${KEEPS_TOOLS}` : `; one already open ${KEEPS_TOOLS}`}.`
  return how + live
}

/** What pw code does with MCP servers: it reads them when a session
 *  starts, so the menu, the setup card and its result say it alike. */
export const KEEPS_TOOLS = 'keeps the tools it started with'

/** The setup card's word for a machine whose pw code has the tools. */
export function machineHasSaid(where: string): string {
  return `pw code on ${where} has this Studio's tools, as you. Sessions started there since it was set up have them; one open from before ${KEEPS_TOOLS}.`
}

/** How a session's tools stand, as the chat's menu says it. */
export function sessionToolsSaid({ tools, continued, relay, machine }: {
  tools: { connected: boolean; count: number; error?: string; machineHas?: boolean } | null
  continued: boolean; relay: boolean; machine?: string
}): { text: string; ok: boolean; offerNew: boolean } {
  const where = machine || 'its machine'
  if (relay) {
    // The platform does not pass a session's MCP state on: what is known
    // is whether pw code on the machine has the Studio.
    return tools?.machineHas
      ? { ok: false, offerNew: true, text: `${machineHasSaid(where)} This chat's session has them only if it started after that.` }
      : { ok: false, offerNew: false, text: `This chat's session runs on ${where} and ${KEEPS_TOOLS}. pw code there does not have this Studio's tools yet; the card in the chat adds them, and sessions started there afterwards have them.` }
  }
  if (tools?.connected) return { ok: true, offerNew: false, text: `This chat's pw code session has the Studio's tools: ${tools.count} of them, the index included.` }
  const why = tools?.error ? `: ${tools.error}` : ''
  if (continued) {
    return tools
      ? { ok: false, offerNew: false, text: `This chat's pw code session does not have the Studio's tools${why}. The next message tries to reconnect them.` }
      : { ok: false, offerNew: false, text: 'This session began outside the Studio, so it works with its own tools and not the Studio\'s library tools: pw code cannot add them to a session that already exists.' }
  }
  return { ok: false, offerNew: false, text: `This chat's pw code session does not have the Studio's tools${why}. The next message reconnects them or starts a session that has them.` }
}
