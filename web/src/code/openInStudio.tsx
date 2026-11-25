import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { toast, useConfirm } from '@parallelworks/ui'
import { AllowsNote } from '../components/MachineToolsAllow'
import { type MachineTools, markAutoSetup, setMachineTools } from '../components/studioTools'
import { startedSession } from './api'

/** A machine as the Code page's listing gives it. */
export interface MachineRef { id: string; name: string; hostname?: string; daemonId?: string }
/** A session as the Code page's listing gives it, as much as opening it needs. */
export interface SessionRef { machineId: string; id: string; archivedAt?: string | null }

/** GET /api/chat/pwcode/here: the daemon this viewer's pw code chat
 *  reaches (or why there is none), and their chats that run in a session. */
export interface Here {
  available: boolean; reason?: string; daemonId?: string; hostname?: string
  /** machine is the id the chat's session is shown under. */
  chats: { session: string; conversationId: string; title?: string; continued?: boolean; machine?: string }[]
  /** Sessions on the viewer's other machines can be continued through
   *  ACTIVATE (the platform's relay), and why not when they cannot. */
  relay?: boolean; relayReason?: string | null
}

/** What still works for a session the Studio cannot carry on in a chat. */
const STILL_HERE = 'You can still answer what it waits on here, and open it in ACTIVATE or where it runs.'

/** Whether m is the machine whose daemon this viewer's pw code chat reaches,
 *  by the daemon id the platform keeps for it, else its host name. */
export function sameMachine(m: MachineRef, here: Here): boolean {
  return here.available && (m.daemonId || !here.daemonId
    ? !!m.daemonId && m.daemonId === here.daemonId
    : !!m.hostname && m.hostname.toLowerCase() === (here.hostname ?? '').toLowerCase())
}

/** Whether the Studio can continue s in a chat for this viewer, and why
 *  not. On the machine whose daemon the chat reaches it is driven there;
 *  on another of the viewer's machines it is driven through ACTIVATE and
 *  keeps running where it is (relay). */
export function continuable(s: SessionRef, machines: readonly MachineRef[], here: Here | null): { ok: boolean; why: string; relay?: boolean } {
  if (!here) return { ok: false, why: 'Checking where this Studio runs pw code…' }
  if (s.archivedAt) return { ok: false, why: 'This session is archived, so it cannot be continued in a chat. Restore it first.' }
  const m = machines.find(x => x.id === s.machineId)
  if (m && sameMachine(m, here)) return { ok: true, why: '' }
  if (here.relay) return { ok: true, why: '', relay: true }
  if (!here.available && !here.relayReason) return { ok: false, why: `${here.reason || 'pw code chat is not offered to you on this Studio.'} ${STILL_HERE}` }
  const why = here.relayReason || here.reason
  const name = m?.name ?? s.machineId
  return { ok: false, why: `This session runs on ${name}, and this Studio cannot carry a chat's messages to it for you${why ? ` (${why.replace(/\.$/, '')})` : ''}. ${STILL_HERE}` }
}

/** Opens a Studio chat by its id. */
export function openChat(id: string) { location.hash = `#chat=${id}` }

interface ContinueAnswer {
  confirm?: string; conversationId?: string; existing?: boolean; error?: string; detail?: string
  /** For a session on another machine: the Studio's tools in pw code there. */
  studioTools?: MachineTools
}

async function postContinue(body: object): Promise<ContinueAnswer> {
  const res = await fetch('/api/chat/pwcode/continue', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const out = (await res.json().catch(() => ({}))) as ContinueAnswer
  if (!res.ok) throw new Error(out.error ?? out.detail ?? `HTTP ${res.status}`)
  return out
}

/** Where the Code page shows a session in its own pane: the machine id its
 *  view is under (a pw code chat's, or "studio" on the machine whose daemon
 *  the Studio reaches as the viewer, else its own, through ACTIVATE), and
 *  the chat that holds it; or why it cannot be shown. */
export function paneFor(s: SessionRef, machines: readonly MachineRef[], here: Here | null): { machine: string; conversationId?: string; why: string } {
  const started = startedSession(s.id)
  if (started?.shownOn) return { machine: started.shownOn, conversationId: started.conversationId, why: '' }
  const chat = here?.chats.find(c => c.session === s.id)
  if (chat?.machine) return { machine: chat.machine, conversationId: chat.conversationId, why: '' }
  const can = continuable({ ...s, archivedAt: null }, machines, here)
  return { machine: can.ok ? (can.relay ? s.machineId : 'studio') : '', why: can.why }
}

/**
 * Continuing a session in a Studio chat, an action of its own: a session
 * with a chat opens that chat. Otherwise the Studio checks it is the
 * viewer's and says what to ask, the person confirms, and the Studio makes
 * a chat linked to the session, which opens.
 */
export function useOpenInStudio(machines: readonly MachineRef[]): {
  here: Here | null
  /** False when it cannot be continued here: why() says so. */
  open: (s: SessionRef) => Promise<boolean>
  why: (s: SessionRef) => string
  /** Reads here again. */
  reload: () => Promise<void>
  dialog: ReactNode
} {
  const { confirm, dialog } = useConfirm()
  const [here, setHere] = useState<Here | null>(null)
  const loadHere = useCallback(async () => {
    try {
      const res = await fetch('/api/chat/pwcode/here')
      const d = await res.json()
      setHere(res.ok ? d as Here : { available: false, reason: (d as { error?: string }).error, chats: [] })
    } catch (e) {
      setHere({ available: false, reason: (e as Error).message, chats: [] })
    }
  }, [])
  useEffect(() => {
    void loadHere()
    const t = setInterval(() => { if (document.visibilityState === 'visible') void loadHere() }, 20_000)
    return () => clearInterval(t)
  }, [loadHere])

  const opened = useCallback((id: string) => {
    // The chat answers in its session: the Studio keeps that for it.
    window.dispatchEvent(new CustomEvent('ade:chats-changed'))
    void loadHere()
    openChat(id)
  }, [loadHere])

  const open = useCallback(async (s: SessionRef) => {
    const chat = here?.chats.find(c => c.session === s.id)?.conversationId ?? startedSession(s.id)?.conversationId
    if (chat) { opened(chat); return true }
    const can = continuable(s, machines, here)
    if (!can.ok) return false
    const body = { machine: s.machineId, session: s.id }
    let first: ContinueAnswer
    try {
      first = await postContinue(body)
    } catch (e) {
      toast.error((e as Error).message, { id: 'code-open' })
      return true
    }
    if (first.conversationId) { opened(first.conversationId); return true }
    // The Studio's tools in pw code on the session's machine: added as the
    // chat opens, ticked unless the person said not to for that machine.
    const tools = first.studioTools
    const offered = tools?.state === 'offer' || tools?.state === 'declined'
    const add = { on: tools?.state !== 'declined' }
    void confirm({
      title: 'Continue this session in a Studio chat?',
      description: first.confirm ?? 'Your messages from this chat go to the same session.',
      children: offered && tools
        ? (
            <>
              <label className="studio-tools-tick">
                <input type="checkbox" defaultChecked={add.on} data-testid="studio-tools-tick" onChange={e => { add.on = e.currentTarget.checked }} />
                {' '}Add this Studio's tools to pw code on {tools.machine}
                <span className="muted"> (the Studio sets it up there over pw ssh as you, with that machine's own ACTIVATE sign-in)</span>
              </label>
              <AllowsNote allows={tools.allows} />
            </>
          )
        : undefined,
      confirmLabel: 'Continue in Studio',
      errorMessage: e => (e as Error).message || 'The Studio could not continue this session.',
      onConfirm: async () => {
        const a = await postContinue({ ...body, confirmed: true })
        if (!a.conversationId) throw new Error('The Studio made no chat for this session.')
        if (tools && offered && add.on !== (tools.state !== 'declined')) void setMachineTools(tools.machineId, add.on)
        if (tools && (tools.state === 'set' || (offered && add.on))) markAutoSetup(a.conversationId)
        opened(a.conversationId)
      },
    })
    return true
  }, [here, machines, confirm, opened])

  const why = useCallback((s: SessionRef) => {
    if (here?.chats.some(c => c.session === s.id) || startedSession(s.id)?.conversationId) return ''
    return continuable(s, machines, here).why
  }, [here, machines])

  return { here, open, why, reload: loadHere, dialog }
}

/** The Code page's address for a session, which opens it in its pane. */
export function sessionHash(machine: string, session: string): string {
  return `#view=code?${new URLSearchParams({ machine, session })}`
}

/** The session a link to the Code page names (#view=code?machine=…&session=…), or null. */
export function sessionInHash(hash: string): { machine: string; session: string } | null {
  const m = hash.match(/^#view=code\?(.*)$/)
  if (!m) return null
  const q = new URLSearchParams(m[1])
  const machine = q.get('machine') ?? '', session = q.get('session') ?? ''
  return session ? { machine, session } : null
}
