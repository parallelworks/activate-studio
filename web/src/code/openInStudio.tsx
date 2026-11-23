import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { toast, useConfirm } from '@parallelworks/ui'
import { keepEngineFor } from '../labelScope'

/** A machine as the Code page's listing gives it. */
export interface MachineRef { id: string; name: string; hostname?: string; daemonId?: string }
/** A session as the Code page's listing gives it, as much as opening it needs. */
export interface SessionRef { machineId: string; id: string; archivedAt?: string | null }

/** GET /api/chat/pwcode/here: the daemon this viewer's pw code chat
 *  reaches (or why there is none), and their chats that run in a session. */
export interface Here {
  available: boolean; reason?: string; daemonId?: string; hostname?: string
  chats: { session: string; conversationId: string; title?: string; continued?: boolean }[]
  /** Sessions on the viewer's other machines can be continued through
   *  ACTIVATE (the platform's relay), and why not when they cannot. */
  relay?: boolean; relayReason?: string | null
}

/** What still works for a session the Studio cannot carry on in a chat. */
const STILL_HERE = 'You can still answer what it waits on here, and open it in ACTIVATE or where it runs.'

/** Whether the Studio can continue s in a chat for this viewer, and why
 *  not. On the machine whose daemon the chat reaches it is driven there;
 *  on another of the viewer's machines it is driven through ACTIVATE and
 *  keeps running where it is (relay). */
export function continuable(s: SessionRef, machines: readonly MachineRef[], here: Here | null): { ok: boolean; why: string; relay?: boolean } {
  if (!here) return { ok: false, why: 'Checking where this Studio runs pw code…' }
  if (s.archivedAt) return { ok: false, why: 'This session is archived, so it cannot be continued in a chat. Restore it first.' }
  const m = machines.find(x => x.id === s.machineId)
  // By the daemon id the platform keeps for the machine, else its host name.
  const same = here.available && !!m && (m.daemonId || !here.daemonId
    ? !!m.daemonId && m.daemonId === here.daemonId
    : !!m.hostname && m.hostname.toLowerCase() === (here.hostname ?? '').toLowerCase())
  if (same) return { ok: true, why: '' }
  if (here.relay) return { ok: true, why: '', relay: true }
  if (!here.available && !here.relayReason) return { ok: false, why: `${here.reason || 'pw code chat is not offered to you on this Studio.'} ${STILL_HERE}` }
  const why = here.relayReason || here.reason
  const name = m?.name ?? s.machineId
  return { ok: false, why: `This session runs on ${name}, and this Studio cannot carry a chat's messages to it for you${why ? ` (${why.replace(/\.$/, '')})` : ''}. ${STILL_HERE}` }
}

/** Opens a Studio chat by its id. */
export function openChat(id: string) { location.hash = `#chat=${id}` }

interface ContinueAnswer { confirm?: string; conversationId?: string; existing?: boolean; error?: string; detail?: string }

async function postContinue(body: object): Promise<ContinueAnswer> {
  const res = await fetch('/api/chat/pwcode/continue', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const out = (await res.json().catch(() => ({}))) as ContinueAnswer
  if (!res.ok) throw new Error(out.error ?? out.detail ?? `HTTP ${res.status}`)
  return out
}

/**
 * Opening a session: it goes to a Studio chat (Continue in Studio). A
 * session with a chat opens that chat. Otherwise the Studio checks it is the
 * viewer's and says what to ask, the person confirms, and the Studio makes
 * a chat linked to the session, which opens. A session the person has just
 * started here is theirs to continue, so it is not asked again.
 */
export function useOpenInStudio(machines: readonly MachineRef[]): {
  here: Here | null
  /** False when it cannot be continued here: why() says so. */
  open: (s: SessionRef, opts?: { started?: boolean }) => Promise<boolean>
  why: (s: SessionRef) => string
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
    keepEngineFor(id, 'pwcode')
    window.dispatchEvent(new CustomEvent('ade:chats-changed'))
    void loadHere()
    openChat(id)
  }, [loadHere])

  const open = useCallback(async (s: SessionRef, opts: { started?: boolean } = {}) => {
    const chat = here?.chats.find(c => c.session === s.id)
    if (chat) { openChat(chat.conversationId); return true }
    const can = continuable(s, machines, here)
    if (!can.ok && !opts.started) return false
    const body = { machine: s.machineId, session: s.id }
    if (opts.started) {
      try {
        const a = await postContinue({ ...body, confirmed: true })
        if (!a.conversationId) throw new Error('The Studio made no chat for this session.')
        opened(a.conversationId)
      } catch (e) {
        toast.error(`The session started, but the Studio could not open it in a chat: ${(e as Error).message}`)
      }
      return true
    }
    let first: ContinueAnswer
    try {
      first = await postContinue(body)
    } catch (e) {
      toast.error((e as Error).message, { id: 'code-open' })
      return true
    }
    if (first.conversationId) { opened(first.conversationId); return true }
    void confirm({
      title: 'Continue this session in a Studio chat?',
      description: first.confirm ?? 'Your messages from this chat go to the same session.',
      confirmLabel: 'Continue in Studio',
      errorMessage: e => (e as Error).message || 'The Studio could not continue this session.',
      onConfirm: async () => {
        const a = await postContinue({ ...body, confirmed: true })
        if (!a.conversationId) throw new Error('The Studio made no chat for this session.')
        opened(a.conversationId)
      },
    })
    return true
  }, [here, machines, confirm, opened])

  const why = useCallback((s: SessionRef) => {
    if (here?.chats.some(c => c.session === s.id)) return ''
    return continuable(s, machines, here).why
  }, [here, machines])

  return { here, open, why, dialog }
}

/** The session a link to the Code page names (#view=code?machine=…&session=…), or null. */
export function sessionInHash(hash: string): { machine: string; session: string } | null {
  const m = hash.match(/^#view=code\?(.*)$/)
  if (!m) return null
  const q = new URLSearchParams(m[1])
  const machine = q.get('machine') ?? '', session = q.get('session') ?? ''
  return session ? { machine, session } : null
}
