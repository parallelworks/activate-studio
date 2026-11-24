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
    : ` Sessions you start there from now on have them${r.liveWhy ? `; this one could not take them now (${r.liveWhy.replace(/\.$/, '')})` : inChat ? '; one already open, this one included, keeps the tools it started with' : '; one already open keeps the tools it started with'}.`
  return how + live
}
