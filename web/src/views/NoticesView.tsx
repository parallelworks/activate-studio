import { Button, EmptyState, RelativeTime, StatusDot } from '@parallelworks/ui'
import { NotificationIcon } from '@parallelworks/ui/icons'
import { MachineLabel } from '../components/MachineLabel'
import type { MachineRef } from '../machines'
import { dismissNotices, type Notice, type Notices, useNotices } from '../notices'

/** What each notice is of, in a word. */
const KINDS: Record<Notice['kind'], string> = {
  chat: 'Chat', code: 'Code session', fleet: 'Fleet agent', delegated: 'Delegated work', transfer: 'Transfer', library: 'Library index',
}

/** A notice that goes once it is read, rather than once it is answered. */
const news = (n: Notice) => n.kind === 'delegated' && n.what !== 'waiting'

/**
 * What needs the person across the Studio, what waits on them first. Each
 * opens where it is dealt with, goes once it is, and can be dismissed.
 */
export function NoticesView() {
  const { items, loaded } = useNotices()
  const waiting = items.filter(n => n.what === 'waiting')
  const rest = items.filter(n => n.what !== 'waiting')
  if (!loaded) return <div className="notices-page" aria-busy="true" />
  if (!items.length) {
    return (
      <div className="notices-page">
        <EmptyState icon={<NotificationIcon />} title="Nothing needs you" headingLevel={2}
          description="Chats, sessions and agents waiting on you are listed here, with replies you have not seen, delegated work that ended, and transfers and library indexes that failed." />
      </div>
    )
  }
  return (
    <div className="notices-page">
      <div className="notices-head">
        <Button variant="secondary" size="sm" onClick={() => void dismissNotices(items.map(n => n.id))}>Dismiss all</Button>
      </div>
      {waiting.length > 0 && <NoticeGroup title="Waiting on you" items={waiting} />}
      {rest.length > 0 && <NoticeGroup title="New" items={rest} />}
    </div>
  )
}

function NoticeGroup({ title, items }: { title: string; items: Notice[] }) {
  return (
    <section className="notices-group">
      <h2>{title} <span className="muted">{items.length}</span></h2>
      <ul>{items.map(n => <NoticeRow key={n.id} notice={n} />)}</ul>
    </section>
  )
}

/** The machine of the session a notice is of, where the listing has it. */
function noticeMachine(n: Notice, sessions: Notices['sessions'], machines: Notices['machines'], chats: Notices['chats']): MachineRef | null {
  const chat = n.conversation ? chats[n.conversation] : undefined
  const id = n.session && sessions.find(s => s.id === n.session)?.machineId
  const m = id ? machines.find(x => x.id === id) : undefined
  if (m) return m
  return chat?.machineName ? { id: chat.machine, name: chat.machineName, kind: chat.local ? 'local' : undefined } : null
}

function NoticeRow({ notice: n }: { notice: Notice }) {
  const { sessions, machines, chats } = useNotices()
  const machine = noticeMachine(n, sessions, machines, chats)
  // The server ends a code session's line with its machine's name, for a
  // notice outside the page.
  const text = machine && n.text.endsWith(` · ${machine.name}`) ? n.text.slice(0, -` · ${machine.name}`.length) : n.text
  return (
    <li className="notice-row" data-testid="notice" data-kind={n.kind}>
      <StatusDot variant={n.what === 'waiting' ? 'warning' : n.what === 'failed' ? 'error' : 'info'} pulse={n.what === 'waiting'} />
      <a className="notice-open" href={n.href} onClick={() => { if (news(n)) void dismissNotices([n.id]) }}>
        <span className="notice-title">{n.title}</span>
        <span className="notice-text">{text}</span>
      </a>
      <span className="notice-meta muted"><span className="notice-of">{machine && <><MachineLabel machine={machine} /> · </>}{KINDS[n.kind]}</span><RelativeTime value={n.at} /></span>
      <Button variant="ghost" size="sm" className="notice-dismiss" aria-label={`Dismiss ${n.title}`} onClick={() => void dismissNotices([n.id])}>Dismiss</Button>
    </li>
  )
}
