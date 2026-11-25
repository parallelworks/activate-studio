import { type PwCodeMachine, type PwCodeStartScreen, sessionLabel, sessionRowState, useResumeClock, useTranscriptWords, waitingQuestions } from '@parallelworks/code'
import { Button, statusTextClass, toast } from '@parallelworks/ui'
import { ApprovalBlock, NoticeCard } from '@parallelworks/ui/ai'
import { useTranslations } from 'use-intl'
import type { PageSession } from './api'
import { MachineIcon, SessionMark } from './parts'

/** Work a chat delegated to pw code: the session, the library folder it
 *  works in and what it wrote there (GET /api/delegated). */
export interface Delegated {
  site: string; session: string; workspace: string; folder?: string; instructions?: string
  objective: string; goal?: string; started: string; state: 'working' | 'finished' | 'stopped' | 'waiting' | 'gone'
  finished?: string; error?: string; files?: string[]; more?: number
}

const DELEGATED_STATE: Record<Delegated['state'], string> = {
  working: 'working', finished: 'finished', stopped: 'failed', waiting: 'waiting for you', gone: 'no longer on its machine',
}

/** A library file's link in the Studio. */
const fileHref = (rel: string) => `#open=file:${encodeURIComponent(rel).replace(/%2F/gi, '/')}`

/**
 * A session the Code page cannot show in its pane (its machine is one this
 * Studio does not reach for the person, say): its state, what it waits on, answered in
 * place, the work a chat delegated to it, and the ways to open it.
 */
export function LinkedSession({ session, machine, missing, delegated, why, inChat, url, onAnswer, onOpen, onClose }: {
  session: PageSession | undefined
  machine: PwCodeMachine | undefined
  /** The listing has come and the session is not in it. */
  missing: boolean
  delegated: Delegated | undefined
  /** Why it cannot be continued in a chat; empty when it can. */
  why: string
  inChat: boolean
  url: string
  onAnswer: PwCodeStartScreen['answer']
  onOpen: () => void
  onClose: () => void
}) {
  const t = useTranslations('agents')
  const words = useTranscriptWords()
  const resumeClock = useResumeClock()
  if (!session) {
    return missing
      ? <NoticeCard tone="warning" testId="agents-link-notice" onDismiss={onClose}>This session is not among your pw code sessions any more. It may have been deleted, or it is on a machine you no longer reach.</NoticeCard>
      : null
  }
  const { stateText, tone } = sessionRowState(session, machine, t, resumeClock)
  const waits = (session.approvals?.length ?? 0) > 0
  const { questions, inSession } = waits ? waitingQuestions(session, words, t) : { questions: [], inSession: null }
  const answer = async (approvalId: string, value: Parameters<PwCodeStartScreen['answer']>[2]) => {
    const refused = await onAnswer(session, approvalId, value)
    if (refused) toast.error(refused)
  }
  return (
    <NoticeCard tone={questions.length > 0 ? 'warning' : 'neutral'} testId="code-linked-session" onDismiss={onClose}>
      <div className="code-linked-head">
        <SessionMark session={session} machine={machine} size="row" />
        <strong className="code-linked-title">{sessionLabel(session, t)}</strong>
        <span className={statusTextClass(tone)}>{stateText}</span>
        {machine && <span className="code-machine-name code-mono muted"><MachineIcon machine={machine} />{machine.name}</span>}
      </div>
      {session.workspace && <div className="code-mono muted code-linked-path">{session.workspace}</div>}
      {delegated && <DelegatedNote d={delegated} />}
      {questions.length > 0 && inSession === null && questions.map(part => (
        <ApprovalBlock key={part.id} part={part} onApprovalAnswer={(id, value) => void answer(id, value)} />
      ))}
      {inSession !== null && <p className="muted">{inSession}</p>}
      <div className="code-linked-actions">
        <Button variant="primary" size="sm" onClick={onOpen} disabled={!!why} title={why || undefined}>{inChat ? 'Open in its Studio chat' : 'Continue in a Studio chat'}</Button>
        {url && <Button variant="secondary" size="sm" href={url} target="_blank" rel="noopener noreferrer">Open in ACTIVATE</Button>}
      </div>
      {why && <p className="muted code-linked-why">{why}</p>}
    </NoticeCard>
  )
}

export function DelegatedNote({ d }: { d: Delegated }) {
  return (
    <section className="code-delegated" aria-label="Delegated work">
      <p>
        Delegated from a chat, {DELEGATED_STATE[d.state] ?? d.state}
        {d.instructions ? <>, with the instructions <strong>{d.instructions}</strong>{d.folder ? ' in its AGENTS.md' : ''}</> : null}
        {d.folder ? <>, in the library folder <span className="code-mono">{d.folder}</span></> : null}.
        {d.folder && d.state === 'working' ? ' Its files are indexed and listed here when it finishes.' : ''}
        {d.state === 'stopped' && d.error ? <> It stopped on an error: {d.error}</> : null}
      </p>
      {d.files && d.files.length > 0 && (
        <ul className="code-delegated-files">
          {d.files.map(f => <li key={f}><a href={fileHref(f)}>{d.folder && f.startsWith(d.folder + '/') ? f.slice(d.folder.length + 1) : f}</a></li>)}
          {(d.more ?? 0) > 0 && <li className="muted">and {d.more} more</li>}
        </ul>
      )}
      {d.folder && d.state !== 'working' && !d.files?.length && <p className="muted">It wrote no files in its folder.</p>}
    </section>
  )
}
