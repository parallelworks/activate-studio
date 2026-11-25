import { useEffect, useState, type ReactNode } from 'react'
import { type PwCodeMachine, sessionLabel, sessionRowState, useResumeClock } from '@parallelworks/code'
import { Button, IconButton, Loader, statusTextClass } from '@parallelworks/ui'
import { CloseIcon } from '@parallelworks/ui/icons'
import { SWRConfig } from 'swr'
import { useTranslations } from 'use-intl'
import { clearOpenPane, setOpenPane } from '../pwcode/api'
import { PWCodeSession } from '../pwcode/PWCodeSession'
import type { PageSession } from './api'
import type { Starting } from './CodeStart'
import { type Delegated, DelegatedNote } from './LinkedSession'
import { useMachineLook } from '../machines'
import { MachineLabel } from '../components/MachineLabel'
import { SessionMark } from './parts'
import { PWCodeToolsNote } from '../pwcode/ToolsNote'
import { useChatLink } from '../pwcode/useChatLink'

/**
 * A session open in the Code page's own pane, beside the list: what it is
 * and where, the way to a Studio chat, and the session as a pw code chat
 * shows it (PWCodeSession). A session being started shows here at once,
 * with its first message, until pw code has made it.
 */

// The pane's own cache: a chat's client and the page's reach the same paths
// through different routes.
const cache = new Map()
const paneCache = { provider: () => cache }

/** The folder's last two parts. */
function shortFolder(p: string): string {
  const parts = p.split('/').filter(Boolean)
  return parts.length > 2 ? `…/${parts.slice(-2).join('/')}` : p
}

export function CodeSession({ session, machine, pane, sessionId, first, where, username, delegated, inChat, elsewhere = false, why, url, onContinue, onClose, onMoved }: {
  /** Its row in the page's listing, once it has one. */
  session: PageSession | undefined
  machine: PwCodeMachine | undefined
  /** The machine id its view is under, and the chat that holds it. */
  pane: { machine: string; conversationId?: string }
  sessionId: string
  /** The message it was started with here. */
  first?: string
  /** Where it was started, until it is listed. */
  where?: Starting | null
  username: string
  delegated: Delegated | undefined
  inChat: boolean
  /** On a machine other than the Studio's own. */
  elsewhere?: boolean
  /** Why it cannot be continued in a chat; empty when it can. */
  why: string
  url: string
  onContinue: () => void
  onClose: () => void
  onMoved: (session: string) => void
}) {
  const t = useTranslations('agents')
  const resumeClock = useResumeClock()
  const state = session ? sessionRowState(session, machine, t, resumeClock) : null
  // Before the view reads its listing, which then has this session too.
  setOpenPane(pane.machine, sessionId)
  useEffect(() => {
    setOpenPane(pane.machine, sessionId)
    return () => clearOpenPane(pane.machine, sessionId)
  }, [pane.machine, sessionId])
  return (
    <section className="code-session" data-testid="code-session" aria-label={session ? sessionLabel(session, t) : 'Session'}>
      <SessionHead
        title={session ? sessionLabel(session, t) : firstLine(first) || 'New session'}
        mark={session && <SessionMark session={session} machine={machine} size="row" />}
        state={state && <span className={statusTextClass(state.tone)}>{state.stateText}</span>}
        place={session ? { machine: machine?.name ?? '', folder: session.workspace ?? '', on: machine } : where ?? null}
        onClose={onClose}
      >
        <Button variant="secondary" size="sm" data-testid="code-continue" onClick={onContinue} disabled={!!why} title={why || undefined}>
          {inChat ? 'Open in its Studio chat' : 'Continue in a Studio chat'}
        </Button>
        {url && <Button variant="ghost" size="sm" href={url} target="_blank" rel="noopener noreferrer">Open in ACTIVATE</Button>}
      </SessionHead>
      {session?.archivedAt && <p className="muted code-session-note">This session is archived. Restore it from its menu in the list to go on with it.</p>}
      {delegated && <div className="code-session-note"><DelegatedNote d={delegated} /></div>}
      <SWRConfig value={paneCache}>
        <PWCodeSession key={`${pane.machine}/${sessionId}`} conversationId={pane.conversationId} machine={pane.machine} sessionId={sessionId}
          username={username} first={first} onMoved={onMoved}
          notes={elsewhere && pane.conversationId ? <PaneTools conversationId={pane.conversationId} /> : undefined} />
      </SWRConfig>
    </section>
  )
}

/** What the chat that holds the session says of the Studio's tools in it. */
function PaneTools({ conversationId }: { conversationId: string }) {
  const { link } = useChatLink(conversationId)
  return link?.relay ? <PWCodeToolsNote conversationId={conversationId} link={link} /> : null
}

function firstLine(s: string | undefined): string {
  return (s ?? '').trim().split('\n')[0]!.slice(0, 120)
}

/** Where a session works: its machine and the folder there. */
function SessionPlace({ place }: { place: { machine: string; folder: string; on?: PwCodeMachine } }) {
  const look = useMachineLook(place.on ?? { name: place.machine })
  const title = `${look.title || look.name}:${place.folder}`
  const folder = place.folder ? `:${shortFolder(place.folder)}` : ''
  if (!place.machine) return <span className="code-mono muted code-session-place" title={title}>{folder}</span>
  return <MachineLabel machine={place.on ?? { name: place.machine }} className="code-mono muted code-session-place" title={title}>{folder}</MachineLabel>
}

function SessionHead({ title, mark, state, place, onClose, children }: {
  title: string
  mark?: ReactNode
  state?: ReactNode
  place: { machine: string; folder: string; on?: PwCodeMachine } | null
  onClose?: () => void
  children?: ReactNode
}) {
  return (
    <header className="code-session-head">
      <div className="code-session-what">
        <div className="code-session-name">
          {mark}
          <h2 className="code-session-title" title={title}>{title}</h2>
        </div>
        {state}
        {place && (place.machine || place.folder) && (
          <SessionPlace place={place} />
        )}
      </div>
      <div className="code-session-actions">
        {children}
        {onClose && <IconButton size="sm" icon={<CloseIcon aria-hidden />} label="Close this session" data-testid="code-session-close" onClick={onClose} />}
      </div>
    </header>
  )
}

/** A session being started: its message, and the step it is on. */
export function StartingSession({ starting }: { starting: Starting }) {
  const [waited, setWaited] = useState(0)
  useEffect(() => {
    const t0 = Date.now()
    const t = setInterval(() => setWaited(Math.round((Date.now() - t0) / 1000)), 1000)
    return () => clearInterval(t)
  }, [])
  return (
    <section className="code-session" data-testid="code-starting" aria-label="Starting a session">
      <SessionHead title={firstLine(starting.prompt) || 'New session'} place={starting} />
      <div className="code-starting">
        {starting.prompt.trim() && <div className="code-starting-said">{starting.prompt}</div>}
        <p className="code-starting-step" role="status">
          <Loader inline decorative size={13} />
          {starting.tools ? "Adding the Studio's tools, then starting on " : 'Starting on '}{starting.machine || 'its machine'}{starting.folder ? <>, in <span className="code-mono">{starting.folder}</span></> : null}
        </p>
        {waited >= 3 && (
          <p className="muted code-starting-wait">
            {waited} s: waiting for pw code there to make the session. {waited >= 15 ? 'A machine that has been idle can take longer. ' : ''}You can open other sessions meanwhile.
          </p>
        )}
      </div>
    </section>
  )
}
