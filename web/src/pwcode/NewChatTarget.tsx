import { type RefObject, useCallback, useEffect, useMemo, useState, useSyncExternalStore } from 'react'
import { createPortal } from 'react-dom'
import createClient from 'openapi-fetch'
import { createPwCodeApi, type PwCodePaths, useStartLocation, useStartPermissionMode } from '@parallelworks/code'
import { PermissionPicker, useChat } from '@parallelworks/ui/ai'
import { useTranslations } from 'use-intl'
import { type Machine, MachineChip, machineMenuEntries } from '../code/CodeStart'
import { DirectoryPicker } from '../code/DirectoryPicker'
import { asksTools, ToolsChoice, ToolsMarkButton, useMachineTools } from '../code/machineTools'
import { useStartMode } from '../code/parts'
import type { PageSession } from '../code/api'
import { VIEWER_CACHES_CLEARED } from '../viewerCache'
import { useAnsweredByPWCode } from './NewChatModel'
import { clearModelPick } from './newChatPick'
import { onStart, setStart, startOf, STUDIO_MACHINE } from './newChatStart'
import { PwCodeWords } from './PWCodeSession'

/** Lists a machine's folders as the Studio reads them for this chat: on its
 *  own pw code machine as the person, on another through ACTIVATE. */
export const startApi = createPwCodeApi(createClient<PwCodePaths>({
  baseUrl: typeof location === 'undefined' ? '' : location.origin,
  fetch: (request: Request) => {
    const url = new URL(request.url)
    url.pathname = url.pathname.replace(/^\/api\/code\//, '/api/chat/pwcode/start/')
    return fetch(url, { method: request.method, headers: request.headers, signal: request.signal, credentials: 'same-origin' })
  },
}), { hidden: () => document.hidden })

/** GET /api/chat/pwcode/start: where a new pw code chat may start. */
interface StartListing {
  available: boolean
  machines: Machine[]
  /** The folders sessions worked in lately. */
  sessions: PageSession[]
  /** Where it starts by default. */
  machine?: string
  folder?: string
}

const isOwn = (m: { id: string }) => m.id === STUDIO_MACHINE

const REREAD_MS = 5_000

function useStartListing(persona: string | null): StartListing | null {
  const [listing, setListing] = useState<StartListing | null>(null)
  useEffect(() => {
    let gone = false, at = 0
    const read = () => {
      at = Date.now()
      fetch(`/api/chat/pwcode/start${persona ? `?persona=${encodeURIComponent(persona)}` : ''}`)
        .then(r => (r.ok ? r.json() as Promise<StartListing> : null))
        .then(d => { if (!gone && d) setListing(d) })
        .catch(() => {})
    }
    read()
    // A machine may have come on while the person was away from the tab.
    const back = () => { if (!document.hidden && Date.now() - at > REREAD_MS) read() }
    const changed = () => { setListing(null); read() }
    document.addEventListener('visibilitychange', back)
    window.addEventListener(VIEWER_CACHES_CLEARED, changed)
    return () => {
      gone = true
      document.removeEventListener('visibilitychange', back)
      window.removeEventListener(VIEWER_CACHES_CLEARED, changed)
    }
  }, [persona])
  return listing
}

/**
 * Where a new pw code chat starts, over its composer until its first
 * message: the machine and the folder, with the Code page's own controls,
 * and, on another of the person's machines, the permission mode, held to
 * what that machine takes from ACTIVATE. They start at what the Studio does
 * by default, and the first message carries a choice (newChatStart.ts).
 * From then on the session's own composer names them.
 */
export function NewChatTarget({ conversationId, persona }: { conversationId: string | null; persona: string | null }) {
  const t = useTranslations('agents')
  const { isStreaming } = useChat()
  const listing = useStartListing(persona)
  const start = useSyncExternalStore(onStart, () => startOf(conversationId))
  const machines = useMemo(() => listing?.machines ?? [], [listing])
  const sessions = useMemo(() => listing?.sessions ?? [], [listing])
  const pickMachine = useCallback((id: string) => {
    clearModelPick(conversationId)
    const m = machines.find(x => x.id === id)
    setStart(conversationId, m && id !== STUDIO_MACHINE ? { machine: { id, claudeCodeAllowed: m.claudeCodeAllowed, claudeInstalled: m.claudeInstalled } } : {})
  }, [conversationId, machines])
  const at = useStartLocation(machines, sessions, { saved: start.machine?.id ?? STUDIO_MACHINE, remember: () => {} })
  const machine = at.machine
  const other = !!machine && machine.id !== STUDIO_MACHINE
  const permission = useStartPermissionMode(useStartMode(), other ? machine : undefined)
  const tools = useMachineTools(isOwn)
  const toolsThere = tools.view(machine)
  const asks = asksTools(toolsThere, machine)
  const ticked = start.tools ?? true

  // The machine chosen is no longer there to start on: back to the default,
  // as the row shows it.
  useEffect(() => {
    if (listing && start.machine && machine?.id !== start.machine.id) setStart(conversationId, {})
  }, [listing, start.machine, machine?.id, conversationId])

  // On another machine nothing is the default: the folder used there last,
  // else the home folder where it lists its folders.
  const recent = at.directories[0]?.path ?? ''
  const [home, setHome] = useState<{ id: string; path: string } | null>(null)
  const wantsHome = other && !start.folder && !recent && machine.directories === true ? machine.id : ''
  useEffect(() => {
    if (!wantsHome || home?.id === wantsHome) return
    let gone = false
    void startApi.listDirectories(wantsHome, '').then(({ data }) => {
      const path = data?.home ?? data?.path
      if (!gone && path) setHome({ id: wantsHome, path })
    }).catch(() => {})
    return () => { gone = true }
  }, [wantsHome, home])
  const folder = other
    ? start.folder || recent || (home?.id === machine.id ? home.path : '')
    : start.folder || listing?.folder || ''
  // What the row shows on another machine is what the message carries.
  useEffect(() => {
    if (other && start.machine?.id === machine.id) setStart(conversationId, { machine: start.machine, ...(folder ? { folder } : {}), mode: permission.mode, ...(asks ? { tools: ticked } : {}) })
  }, [other, machine, start.machine, folder, permission.mode, asks, ticked, conversationId])

  if (!listing?.available) return null
  const location = { ...at, pickMachine, directory: folder }
  const pickFolder = (path: string) => {
    if (other) setStart(conversationId, { ...start, folder: path })
    else setStart(conversationId, path === listing.folder ? {} : { folder: path })
  }
  return (
    <fieldset className="pwcode-start" data-testid="pwcode-start" disabled={isStreaming} aria-label="Where this chat starts">
      {other && persona && (
        <p className="pwcode-start-note warn" role="status" data-testid="pwcode-start-persona">
          The {persona} persona works only on this Studio's own pw code machine: a session on {machine.name} has no way to take it from the Studio. Choose Default under Persona, or the Studio's machine.
        </p>
      )}
      <MachineChip finding={false} machine={machine} items={machineMenuEntries(location, t, tools)} t={t} />
      {machine && <ToolsMarkButton machine={machine} control={tools} mode={permission.mode} />}
      {machine && <DirectoryPicker key={machine.id} api={startApi} machine={machine} directory={folder} recents={at.directories} onPick={pickFolder} />}
      {other && <PermissionPicker mode={permission.mode} ceiling={permission.ceiling} accepted={permission.accepted} onPick={permission.pick} />}
      {asks && toolsThere?.tools && <ToolsChoice tools={toolsThere.tools} checked={ticked} disabled={isStreaming} onChange={on => setStart(conversationId, { ...start, tools: on })} />}
      {tools.dialog}
    </fieldset>
  )
}

/** The place over the open chat's composer, as a portal's host: the package
 *  has no slot there. In the empty state it is the row the package keeps. */
function useOverComposer(canvas: RefObject<HTMLElement | null>, on: boolean): HTMLElement {
  const [host] = useState(() => {
    const el = document.createElement('div')
    el.className = 'pwcode-start-host'
    return el
  })
  useEffect(() => {
    const root = canvas.current
    if (!on || !root) { host.remove(); return }
    const place = () => {
      const box = [...root.querySelectorAll('.chat-composer')].find(e => !e.closest('.pwcode-session'))?.parentElement
      const row = box?.parentElement
      if (!row || !box) { host.remove(); return }
      const kept = row.querySelector(':scope > .min-h-7')
      if (kept) { if (host.parentElement !== kept) kept.append(host) } else if (host.nextElementSibling !== box) row.insertBefore(host, box)
    }
    place()
    const mo = new MutationObserver(place)
    mo.observe(root, { childList: true, subtree: true })
    return () => { mo.disconnect(); host.remove() }
  }, [canvas, on, host])
  return host
}

/** The row, where it belongs: a pw code chat with no session yet. */
export function NewChatTargetSlot({ conversationId, hasSession, persona, canvas }: {
  conversationId: string | null
  hasSession: boolean
  persona: string | null
  canvas: RefObject<HTMLElement | null>
}) {
  const on = useAnsweredByPWCode() && !hasSession
  const host = useOverComposer(canvas, on)
  return on ? createPortal(<PwCodeWords><NewChatTarget key={conversationId ?? ''} conversationId={conversationId} persona={persona} /></PwCodeWords>, host) : null
}
