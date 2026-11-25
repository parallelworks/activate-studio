import React, { useCallback, useEffect, useState } from 'react'
import { type StatusLook, statusLook } from '../statusLook'
import {
  Button, Callout, buttonClasses, EmptyState, Field, Input, Pagination, ProgressBar, SearchInput, SegmentedControl,
  StatusBadge, StatusDot, StatusLabel, Tabs, Textarea, toast,
} from '@parallelworks/ui'
import { LogViewer } from '@parallelworks/ui/logviewer'
import { PersonaIcon } from '../components/PersonaIcon'
import { ApplyToFolder, changeFolder, type AgentFolder } from '../components/ApplyToFolder'
import { useWatch } from '../live'
import { FleetPage, topicVariant } from './FleetPage'
import { libraryNames } from '../libraryNames'
import { useAppConfig } from '../config'

/**
 * Agent instructions and skills: the markdown files that shape how agents
 * work, in one place a user can find. Instructions (persona files) are
 * applied to folders, where the Studio writes them into AGENTS.md for pw
 * code, and set a Studio chat's stance; skills are applied the same way
 * and loaded by the Studio assistant mid-conversation.
 *
 * They are separated here because they do different jobs and the
 * difference decides which one somebody should write. A persona is a
 * standing stance chosen for a whole conversation, so one is in force
 * from the first message. A skill is task instructions the assistant
 * loads mid-conversation when the work matches its description, so a
 * conversation can use several without anyone selecting them.
 *
 * This lives beside the Library rather than in Settings because it is
 * material a user authors and grows, not deployment configuration.
 */

interface Persona { name: string; description: string; shared: boolean; icon: string }
interface Skill { name: string; description: string; file: string }
interface Approval { id: string; kind: string; text: string }
interface SubTask { name: string; persona: string; objective: string; parent: string | null; depth: number; state: string; note: string; resultPath: string | null; updatedAt: string; usage?: { input: number; output: number; total: number; cost?: number | null } | null; sessionId?: string | null; approvals?: Approval[] | null; host?: string | null }
export interface TaskRow { id: string; objective: string; state: string; agents: number; running: number; waiting?: number; done?: number; failed?: number; stopped?: number; runtime?: 'runner' | 'session'; usage?: { input: number; output: number; total: number; cost?: number | null } | null }
interface PlatformRun { slug: string; workflow: string; resource: string | null; conversationId: string | null; launchedAt: string; state: string; endedAt: string | null }
interface TaskDetail extends TaskRow { maxAgents: number; maxDepth: number; board: { seq: number; at: string; from: string; topic: string; body: string }[] }

/** How many of a task's agents are done: those that completed, where the
 *  server counts them, else those not running. */
export const doneCount = (mi: TaskRow) => mi.done ?? mi.agents - mi.running

/** A task card's count: done of all, with any that failed or stopped. */
export function agentsSummary(mi: TaskRow): string {
  const parts = [`${doneCount(mi)} of ${mi.agents} done`]
  if (mi.failed) parts.push(`${mi.failed} failed`)
  if (mi.stopped) parts.push(`${mi.stopped} stopped`)
  return parts.join(', ')
}

/** A task's state as its agents ended: a completed task whose agents all
 *  failed reads failed, and one where some failed reads "completed, N
 *  failed" in amber. Any other state reads as it is. */
export function taskOutcome(mi: TaskRow): { label: string; look: StatusLook } {
  const failed = mi.failed ?? 0
  if (mi.state === 'completed' && failed > 0) {
    return doneCount(mi) === 0 ? { label: 'failed', look: statusLook('failed') } : { label: `completed, ${failed} failed`, look: { variant: 'warning' } }
  }
  return { label: mi.state, look: statusLook(mi.state) }
}

/** "take off", or "taken off" once done, always as wide as the wider, so
 *  the actions after it stay where they are. */
function TakeOffLabel({ off }: { off: boolean }) {
  return (
    <span className="agents-swap">
      <span aria-hidden={off} className={off ? 'agents-swap-hidden' : undefined}>take off</span>
      <span aria-hidden={!off} className={off ? undefined : 'agents-swap-hidden'}>taken off</span>
    </span>
  )
}

export function AgentsView({ active = true, onOpen }: { active?: boolean; onOpen: (path: string) => void }) {
  const cfg = useAppConfig()
  const [personas, setPersonas] = useState<Persona[]>([])
  const [skills, setSkills] = useState<Skill[]>([])
  const [dir, setDir] = useState('')
  const [kind, setKind] = useState<'persona' | 'skill'>('persona')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [body, setBody] = useState('')
  const [icon, setIcon] = useState('')
  const [note, setNote] = useState('')
  // Editing loads the file into the same form that writes it, so there is
  // one place to learn rather than a separate editor.
  const [editing, setEditing] = useState<string | null>(null)
  // A library of sixty is a wall of rows; filtering is the difference
  // between a list you scan and one you search.
  const [filter, setFilter] = useState('')
  // Delegated tasks: the tree of subtasks and the agents working them,
  // polled while anything is running, since the point is watching.
  const [taskRuns, setTaskRuns] = useState<TaskRow[]>([])
  // Set when the earlier tasks are kept for the Studio's administrators.
  const [tasksAdminsOnly, setTasksAdminsOnly] = useState('')
  const [openTask, setOpenTask] = useState<string | null>(null)
  const [detail, setDetail] = useState<(Omit<TaskDetail, 'agents'> & { agents: SubTask[] }) | null>(null)
  // Paged rather than one long scroll: a library of sixty is browsed in
  // chunks, and the count line says where you are in it. Filtering resets
  // to the first page, since page four of the old result set means
  // nothing against the new one.
  // #view=agents:<task-id> from a chat reply or a shared link opens that
  // task's tree directly.
  // Two pages under one tab: the live task board, and the persona/skill
  // library. They share the section because both are about agents, and
  // they are separated because watching a fleet and editing markdown are
  // different activities that were fighting for the same screen.
  // Workflow runs the assistant launched, followed by the server across
  // restarts; listed here so long-running work has a place to be seen.
  const [runs, setRuns] = useState<PlatformRun[]>([])
  useWatch<{ runs?: PlatformRun[] }>('/api/runs', 15_000, active, d => setRuns(d.runs ?? []))
  const [page, setPageState] = useState<'fleet' | 'tasks' | 'library'>(() =>
    (localStorage.getItem('ade-agents-page') as 'fleet' | 'tasks' | 'library') || 'fleet')
  const setPage = (v: 'fleet' | 'tasks' | 'library') => { setPageState(v); localStorage.setItem('ade-agents-page', v) }
  useEffect(() => {
    // #view=agents:fleet opens the standing agents; any other section is a
    // task id (they start with a date, so none is "fleet").
    const openSection = (section: string) => {
      if (section === 'fleet') setPageState('fleet')
      else { setOpenTask(section); setPageState('tasks') }
    }
    const on = (e: Event) => {
      const d = (e as CustomEvent).detail as { view?: string; section?: string }
      if (d?.view === 'agents' && d.section) openSection(d.section)
    }
    window.addEventListener('ade-view-section', on)
    const m = location.hash.match(/^#view=agents:([a-z0-9-]+)(?:\?.*)?$/)
    if (m) openSection(m[1])
    return () => window.removeEventListener('ade-view-section', on)
  }, [])
  // The drill-down behind one agent's row: its live output, polled while
  // it works.
  const [openAgent, setOpenAgent] = useState<string | null>(null)
  const [agentTail, setAgentTail] = useState('')
  useEffect(() => {
    if (!openTask || !openAgent) return
    let stop = false
    let t = 0
    const tick = async () => {
      try {
        const d = await fetch(`/api/tasks/${encodeURIComponent(openTask)}/agents/${encodeURIComponent(openAgent)}/output`).then(r => r.json())
        if (!stop) setAgentTail(String(d.tail ?? ''))
      } catch { /* next tick */ }
    }
    t = window.setTimeout(tick, 0)
    return () => { stop = true; clearTimeout(t) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openTask, openAgent])
  useEffect(() => { setOpenAgent(null); setAgentTail('') }, [openTask])


  const [pPage, setPPage] = useState(0)
  const [sPage, setSPage] = useState(0)
  useEffect(() => { setPPage(0); setSPage(0) }, [filter])
  const [busy, setBusy] = useState(false)

  // The earlier tasks are history: read when the page or a task opens.
  useEffect(() => {
    if (page !== 'tasks') return
    let stop = false
    void (async () => {
      try {
        const d = await fetch('/api/tasks').then(r => r.json())
        if (!stop) { setTaskRuns(d.tasks ?? []); setTasksAdminsOnly(typeof d.adminsOnly === 'string' ? d.adminsOnly : '') }
        if (openTask && !stop) {
          const md = await fetch(`/api/tasks/${encodeURIComponent(openTask)}`).then(r => r.json())
          if (!stop && !md.error) setDetail(md)
        }
      } catch { /* nothing to show */ }
    })()
    return () => { stop = true }
  }, [openTask, page])

  const load = useCallback(() => fetch('/api/extensions').then(r => r.json())
    .then(d => { setPersonas(d.personas ?? []); setSkills(d.skills ?? []); setDir(d.corpusAgentDir ?? '') })
    .catch(() => {}), [])
  useEffect(() => { load() }, [load])

  // The folders that carry instructions or skills the Studio wrote there,
  // found by their markers; read when the page opens and after a change.
  const [folders, setFolders] = useState<AgentFolder[]>([])
  const [applying, setApplying] = useState<{ kind: 'persona' | 'skill'; name: string } | null>(null)
  const [folderNote, setFolderNote] = useState('')
  const loadFolders = useCallback(() => fetch('/api/agents-md/folders').then(r => r.json())
    .then(d => setFolders(d.folders ?? [])).catch(() => {}), [])
  useEffect(() => { if (page === 'library') void loadFolders() }, [page, loadFolders])
  // What was taken off stays in its place, marked, while the page is open,
  // so the next action does not move under the pointer, and a list read
  // before the take-off landed cannot show it applied again. Applying it
  // to that folder again clears its mark.
  const [takenOff, setTakenOff] = useState<Set<string>>(new Set())
  const offKey = (f: AgentFolder, kind: 'persona' | 'skill', n: string) => `${f.library}:${f.folder}:${kind}:${n}`
  const isOff = (f: AgentFolder, kind: 'persona' | 'skill', n: string) => takenOff.has(offKey(f, kind, n))
  useEffect(() => { setTakenOff(new Set()) }, [page])
  const carrying = (kind: 'persona' | 'skill', n: string) =>
    folders.filter(f => (kind === 'persona' ? f.instructions : f.skills).includes(n) && !isOff(f, kind, n))
  const stillCarrying = folders.filter(f => f.instructions.some(n => !isOff(f, 'persona', n)) || f.skills.some(n => !isOff(f, 'skill', n)))
  const takeOff = async (f: AgentFolder, kind: 'persona' | 'skill', n: string) => {
    setFolderNote('')
    try {
      await changeFolder(true, f.space ? f.library : '', f.folder, kind === 'persona' ? { instructions: n } : { skills: [n] })
      toast.success(`Took ${n} off ${f.folder || 'the top of ' + f.label}.`)
      setTakenOff(prev => new Set(prev).add(offKey(f, kind, n)))
    } catch (e) { setFolderNote((e as Error).message) }
  }
  const where = (f: AgentFolder) => `${f.folder || '(top)'}${f.space ? ` in ${f.label}` : ''}`

  // Pasting a whole file is the common case, so frontmatter already in the
  // text fills the fields rather than being left to collide with them.
  const onBody = (text: string) => {
    setBody(text)
    const fm = /^---\n([\s\S]*?)\n---/.exec(text)
    if (!fm) return
    const get = (k: string) => new RegExp(`^${k}:\\s*(.+)$`, 'm').exec(fm[1])?.[1]?.trim()
    if (!name) setName(get('name') ?? '')
    if (!description) setDescription(get('description') ?? '')
  }

  const save = async () => {
    setBusy(true); setNote('')
    try {
      const res = await fetch(`/api/extensions/${kind}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, description, body, icon, shared: true }),
      })
      const d = await res.json()
      if (!res.ok) throw new Error(d.error ?? `${res.status}`)
      toast.success(kind === 'persona'
        ? `Saved ${d.name}. Apply it to a folder again to bring a folder's AGENTS.md up to date.`
        : `Saved ${d.name}. It is available in chat now.`)
      setName(''); setDescription(''); setBody(''); setIcon(''); setEditing(null)
      await load()
    } catch (e) {
      setNote(String((e as Error).message))
    } finally { setBusy(false) }
  }

  const edit = async (k: 'persona' | 'skill', n: string) => {
    const res = await fetch(`/api/extensions/${k}?name=${encodeURIComponent(n)}`)
    if (!res.ok) return
    const d = await res.json()
    setKind(k); setEditing(d.name); setName(d.name); setDescription(d.description ?? ''); setBody(d.body ?? ''); setIcon(d.icon ?? '')
    setNote(`Editing ${d.name}. Saving overwrites it.`)
    document.querySelector('.agents-body')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }

  // A copy of a persona, loaded into the editor as a new file: the way a
  // default persona is adapted without losing the original.
  const duplicate = async (n: string) => {
    const res = await fetch(`/api/extensions/persona?name=${encodeURIComponent(n)}`)
    if (!res.ok) return
    const d = await res.json()
    setKind('persona'); setEditing(null); setName(`${d.name}_copy`); setDescription(d.description ?? ''); setBody(d.body ?? ''); setIcon(d.icon ?? '')
    setNote(`Copy of ${d.name}. Rename it and save; the original is untouched.`)
    document.querySelector('.agents-body')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }

  const remove = async (n: string) => {
    await fetch(`/api/extensions/persona?name=${encodeURIComponent(n)}`, { method: 'DELETE' })
    await load()
  }

  const matches = (n: string, d: string) => {
    const q = filter.trim().toLowerCase()
    return !q || n.toLowerCase().includes(q) || (d ?? '').toLowerCase().includes(q)
  }
  const shownPersonas = personas.filter(p => matches(p.name, p.description))
  const shownSkills = skills.filter(k => matches(k.name, k.description))

  const PAGE = 12
  const pageOf = <T,>(rows: T[], page: number) => rows.slice(page * PAGE, page * PAGE + PAGE)

  const onFile = (f: File | undefined) => {
    if (!f) return
    f.text().then(t => { if (!name) setName(f.name.replace(/\.md$/, '')); onBody(t) })
  }

  const fmtTokens = (n: number) => n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
  // Where the agents run. Shown once in the task header when they share a
  // machine, and on each row only when they are spread across machines.
  const hostLabel = (h: string) => h.split('/').pop() || h
  const hosts = new Set((detail?.agents ?? []).map(a => a.host).filter((h): h is string => !!h))
  const sharedHost = hosts.size === 1 ? [...hosts][0] : null

  return (
    <div className="overview-view agents-view">
      <div className="card ov-head">
        <Tabs label="Agents" className="agents-tabs" value={page} onValueChange={setPage} items={[
          { value: 'fleet', label: 'Fleet' },
          { value: 'tasks', label: 'Earlier tasks' },
          { value: 'library', label: 'Instructions and skills' },
        ]} />
      </div>

      {page === 'fleet' && <FleetPage active={active} personas={personas} onOpenTask={id => { setOpenTask(id); setPage('tasks') }} />}

      {page === 'tasks' && (
        <section className="card task-board-card">
          <Callout tone="info" size="sm" className="task-moved">
            Work you delegate now goes to pw code: one session in a library folder, with the agent instructions you choose
            in its <code>AGENTS.md</code>. Find it on the <a href="#view=code">Code page</a> under <b>Delegated</b>. This page
            keeps the tasks of the earlier task engine to read: their agents, board and output, with their results in the
            library under <code>tasks/</code>.
          </Callout>
          {taskRuns.length === 0 && tasksAdminsOnly && (
            <EmptyState variant="bare" className="task-empty" title="Earlier tasks are kept for administrators" description={tasksAdminsOnly} />
          )}
          {taskRuns.length === 0 && !tasksAdminsOnly && (
            <EmptyState variant="bare" className="task-empty" title="No earlier tasks" description="Tasks delegated before delegation moved to pw code sessions are listed here." />
          )}
          {taskRuns.length > 0 && (
            <div className="task-cards">
              {taskRuns.map(mi => (
                <div key={mi.id} className={`task-card${openTask === mi.id ? ' selected' : ''}`}
                  onClick={() => setOpenTask(openTask === mi.id ? null : mi.id)}>
                  <div className="task-card-top">
                    <StatusDot {...taskOutcome(mi).look} />
                    <span className="task-card-state">{taskOutcome(mi).label}</span>
                    {(mi as { execution?: string }).execution === 'campaign' && (
                      <StatusBadge>campaign · {((mi as { resource?: string }).resource ?? '').split('/').pop() || '?'}</StatusBadge>
                    )}
                    {mi.runtime === 'session' && <StatusBadge>pw code sessions</StatusBadge>}
                  </div>
                  <div className="task-card-objective">{mi.objective}</div>
                  <ProgressBar value={doneCount(mi)} max={Math.max(1, mi.agents)} tone={mi.failed ? 'warning' : 'success'} size="sm" aria-label="Agents done" />
                  <div className="task-card-meta">
                    <span>{agentsSummary(mi)}{mi.usage?.total ? ` · ${fmtTokens(mi.usage.total)} tok` : ''}</span>
                    <span>{mi.id.slice(0, 10)}</span>
                  </div>
                </div>
              ))}
            </div>
          )}
          {runs.length > 0 && (
            <div className="run-list">
              <h3>Workflow runs launched from chat</h3>
              {runs.slice(0, 12).map(r => (
                <div key={r.slug} className="run-row">
                  <StatusDot {...statusLook(r.state)} />
                  <span className="run-slug">{r.slug}</span>
                  <span className="muted run-meta">{r.workflow}{r.resource ? ` on ${r.resource.replace(/^pw:\/\/[^/]+\//, '')}` : ''} · {r.state}{r.endedAt ? '' : ' · since ' + new Date(r.launchedAt).toLocaleTimeString()}</span>
                  {r.conversationId && <a href={`#chat=${r.conversationId}`}>chat</a>}
                </div>
              ))}
            </div>
          )}
          {openTask && detail && (
            <div className="task-detail">
              <div className="task-detail-head">
                <StatusDot {...(() => { const row = taskRuns.find(t => t.id === detail.id); return row ? taskOutcome(row).look : statusLook(detail.state) })()} />
                <span className="task-detail-title">
                  <span className="task-detail-objective">{detail.objective}</span>
                  {sharedHost && <span className="tree-host" title={`Every agent in this task runs on ${sharedHost}`}>on {hostLabel(sharedHost)}</span>}
                </span>
                <a className="task-results-link" href={`#open=file:tasks/${detail.id}`} onClick={e => { e.preventDefault(); onOpen(`tasks/${detail.id}`) }}>results in the library</a>
              </div>
              <div className="task-columns">
                <div className="task-tree">
                  {detail.agents.map(a => (
                    <React.Fragment key={a.name}>
                      <div className={`tree-row${openAgent === a.name ? ' open' : ''}`}
                        style={{ paddingLeft: `${10 + a.depth * 20}px` }}
                        onClick={() => setOpenAgent(openAgent === a.name ? null : a.name)}
                        title="Show this agent's output">
                        {a.depth > 0 && <span className="tree-elbow" style={{ left: `${a.depth * 20 - 8}px` }} />}
                        <StatusDot {...statusLook(a.state)} />
                        <span className="tree-name">{a.name}</span>
                        {a.host && !sharedHost && <span className="tree-host" title={`Runs on ${a.host}`}>{hostLabel(a.host)}</span>}
                        <span className="tree-note muted">{a.note && a.state === 'canceled' ? a.note : a.state}{a.usage?.total ? ` · ${fmtTokens(a.usage.total)} tok${a.usage.cost != null ? ` · $${a.usage.cost.toFixed(2)}` : ''}` : ''}</span>
                        {a.resultPath && (
                          <Button variant="ghost" size="sm" onClick={e => { e.stopPropagation(); onOpen(a.resultPath!) }}>result</Button>
                        )}
                      </div>
                      {openAgent === a.name && (
                        <div className="agent-detail">
                          <p className="muted">{a.persona ? `${a.persona}: ` : ''}{a.objective}</p>
                          {(a as { runSlug?: string | null }).runSlug && (
                            <p className="muted">platform run <code>{(a as { runSlug?: string }).runSlug}</code></p>
                          )}
                          {a.sessionId && (
                            <p className="muted">
                              pw code session <code>{a.sessionId}</code>{a.host ? <> on <code>{hostLabel(a.host)}</code></> : null}; it may
                              still be on the <a href={`#view=code?session=${encodeURIComponent(a.sessionId)}`}>Code page</a>.
                            </p>
                          )}
                          {a.usage?.total ? <p className="muted">tokens: {fmtTokens(a.usage.input)} in, {fmtTokens(a.usage.output)} out, {fmtTokens(a.usage.total)} total{a.usage.cost != null ? `, $${a.usage.cost.toFixed(3)}` : ''}</p> : null}
                          {/* The server sends the last 16 KB, so the viewer follows the end
                              as each poll replaces it; there is nothing older to page in. */}
                          {agentTail
                            ? <div className="agent-live"><LogViewer log={agentTail} height={300} downloadName={`${a.name}.log`} /></div>
                            : <p className="muted">No output was captured for this agent.</p>}
                        </div>
                      )}
                    </React.Fragment>
                  ))}
                </div>
                <div className="task-feed">
                  <div className="task-feed-title muted">Board</div>
                  {detail.board.slice(-40).map(b => (
                    <div key={b.seq} className="task-msg">
                      <span className="task-msg-meta">
                        <StatusBadge variant={topicVariant(b.topic)} className="task-topic">{b.topic}</StatusBadge>
                        {b.at.slice(11, 19)} · {b.from}
                      </span>
                      <span>{b.body.slice(0, 300)}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}
        </section>
      )}

      {page === 'library' && (<>
      {(personas.length + skills.length) > 8 && (
        <div className="card agents-filter">
          <SearchInput value={filter} onValueChange={setFilter}
            label={`Filter ${personas.length + skills.length} definitions by name or description`} />
        </div>
      )}

      <div className="ov-grid">
        <section className="card ov-list">
          <h3>Agent instructions ({shownPersonas.length}{filter ? ` of ${personas.length}` : ''})</h3>
          <p className="muted view-sub">
            How an agent should work, written once and applied to the folders agents work in. <b>Apply to a folder</b> writes them
            into the folder's <code>AGENTS.md</code>, which pw code reads in every session there; your own text in that file stays as it is.
            In Chat, the <b>Persona</b> button chooses one for a conversation: the Studio assistant takes it as its stance, and a pw code
            chat works in <code>agents/&lt;name&gt;</code> with it in that folder's <code>AGENTS.md</code>. They are stored
            in the knowledge base{dir ? <> at <code>{dir}</code></> : null}, browsable under <code>.agents</code> in the Library, and labeled <code>agent-persona</code>.
          </p>
          {pageOf(shownPersonas, pPage).map(p => (
            <div className="ov-row agents-def-row" key={p.name} onClick={() => void edit('persona', p.name)} title="Open these instructions for editing">
              <span className="ov-row-path"><PersonaIcon icon={p.icon} name={p.name} size={20} /><span className="agents-name" title={p.name}>{p.name}</span>{p.shared && <span className="persona-shared">shared</span>}</span>
              <span className="ov-row-meta agents-meta">
                <span className="agents-desc">{p.description || 'no description'}
                  {carrying('persona', p.name).length > 0 && (
                    <span className="agents-carriers" title={carrying('persona', p.name).map(where).join(', ')}>
                      in {carrying('persona', p.name).length} folder{carrying('persona', p.name).length === 1 ? '' : 's'}
                    </span>
                  )}
                </span>
                <span className="agents-actions">
                {p.shared && (
                  <Button variant="ghost" size="sm" className="agents-del"
                    onClick={e => { e.stopPropagation(); onOpen(`.agents/${p.name}.md`) }}>in library</Button>
                )}
                <Button variant="ghost" size="sm" className="agents-del" title="Write these instructions into a folder's AGENTS.md for pw code"
                  onClick={e => { e.stopPropagation(); setApplying({ kind: 'persona', name: p.name }) }}>apply to a folder</Button>
                <Button variant="ghost" size="sm" className="agents-del" title="Open a copy in the editor, to change without touching the original"
                  onClick={e => { e.stopPropagation(); void duplicate(p.name) }}>duplicate</Button>
                <Button variant="ghost" size="sm" className="agents-del"
                  onClick={e => { e.stopPropagation(); remove(p.name) }}>remove</Button>
                </span>
              </span>
            </div>
          ))}
          {shownPersonas.length === 0 && <p className="muted">{filter ? 'None match that filter.' : 'None yet. The first ones you add can be applied to a folder and appear in the chat Persona control.'}</p>}
          <Pagination className="agents-pager" page={pPage} pageSize={PAGE} total={shownPersonas.length} onPageChange={setPPage} />
        </section>

        <section className="card ov-list">
          <h3>Skills ({shownSkills.length}{filter ? ` of ${skills.length}` : ''})</h3>
          <p className="muted view-sub">
            Task instructions loaded when the work matches the description. The Studio assistant loads them mid-conversation:
            ask for the work, or name the skill. <b>Apply to a folder</b> also writes one to <code>.agents/skills</code> there for pw code,
            which uses a folder's skills once you have opened that folder in pw code and approved it. Write the
            description so it can tell when the skill applies.
          </p>
          {pageOf(shownSkills, sPage).map(s => (
            <div className="ov-row agents-def-row" key={s.name} onClick={() => void edit('skill', s.name)} title="Open this skill for editing">
              <span className="ov-row-path"><span className="agents-name" title={s.name}>{s.name}</span></span>
              <span className="ov-row-meta agents-meta">
                <span className="agents-desc">{s.description || 'no description'}
                  {carrying('skill', s.name).length > 0 && (
                    <span className="agents-carriers" title={carrying('skill', s.name).map(where).join(', ')}>
                      in {carrying('skill', s.name).length} folder{carrying('skill', s.name).length === 1 ? '' : 's'}
                    </span>
                  )}
                </span>
                <span className="agents-actions">
                <Button variant="ghost" size="sm" className="agents-del" title="Write this skill into a folder for pw code"
                  onClick={e => { e.stopPropagation(); setApplying({ kind: 'skill', name: s.name }) }}>apply to a folder</Button>
                </span>
              </span>
            </div>
          ))}
          {shownSkills.length === 0 && <p className="muted">{filter ? 'None match that filter.' : 'None yet.'}</p>}
          <Pagination className="agents-pager" page={sPage} pageSize={PAGE} total={shownSkills.length} onPageChange={setSPage} />
        </section>
      </div>

      <section className="card ov-list agents-folders">
        <h3>Folders with agent instructions ({stillCarrying.length})</h3>
        <p className="muted view-sub">
          The folders whose <code>AGENTS.md</code> or <code>.agents/skills</code> carry instructions or skills applied here, in {libraryNames(cfg).writable.replace(' or ', ' and in ')}. Take one off and its section goes; the rest of the file stays.
        </p>
        {folders.map(f => (
          <div className="ov-row" key={`${f.library}:${f.folder}`}>
            <span className="ov-row-path">
              {!f.space ? <a href="#" onClick={e => { e.preventDefault(); onOpen(f.path) }}>{where(f)}</a> : <span>{where(f)}</span>}
            </span>
            <span className="ov-row-meta">
              {f.problem && <StatusLabel variant="warning">{f.problem}</StatusLabel>}
              {f.instructions.map(n => (
                <Button key={`i:${n}`} variant="ghost" size="sm" className={`agents-del${isOff(f, 'persona', n) ? ' agents-off' : ''}`}
                  disabled={isOff(f, 'persona', n)} title={isOff(f, 'persona', n) ? undefined : `Take the instructions ${n} off this folder`}
                  onClick={() => void takeOff(f, 'persona', n)}><span>{n} · <TakeOffLabel off={isOff(f, 'persona', n)} /></span></Button>
              ))}
              {f.skills.map(n => (
                <Button key={`s:${n}`} variant="ghost" size="sm" className={`agents-del${isOff(f, 'skill', n) ? ' agents-off' : ''}`}
                  disabled={isOff(f, 'skill', n)} title={isOff(f, 'skill', n) ? undefined : `Take the skill ${n} off this folder`}
                  onClick={() => void takeOff(f, 'skill', n)}><span>skill {n} · <TakeOffLabel off={isOff(f, 'skill', n)} /></span></Button>
              ))}
            </span>
          </div>
        ))}
        {folders.length === 0 && <p className="muted">None yet. Use <b>apply to a folder</b> on instructions or a skill above.</p>}
        {folderNote && <p className="muted">{folderNote}</p>}
      </section>

      {applying && (
        <ApplyToFolder kind={applying.kind} name={applying.name} onClose={() => setApplying(null)}
          onDone={f => {
            setApplying(null)
            toast.success(`Applied ${applying.name} to ${f.folder || 'the top of ' + f.label}.`)
            setTakenOff(prev => { const next = new Set(prev); next.delete(offKey(f, applying.kind, applying.name)); return next })
            void loadFolders()
          }} />
      )}

      <section className="card settings-card">
        <h3>{editing ? `Editing ${editing}` : 'Add one'}</h3>
        <div className="agents-kind">
          <SegmentedControl label="Kind" value={kind} onValueChange={setKind}
            options={[{ value: 'persona', label: 'Instructions' }, { value: 'skill', label: 'Skill' }]} />
          <label className={`${buttonClasses()} agents-file`}>
            Choose a .md file
            <input type="file" accept=".md,text/markdown" onChange={e => onFile(e.target.files?.[0])} />
          </label>
        </div>
        <div className="agents-form">
          <div className="settings-grid">
            <Input label="Name" value={name} onChange={e => setName(e.target.value)} placeholder="proposal-reviewer" />
            <Input label="Description" value={description} onChange={e => setDescription(e.target.value)}
              placeholder={kind === 'skill' ? 'when the assistant should load this' : 'what these instructions are for'} />
          </div>
          {kind === 'persona' && (
            <Field label="Icon" description={<>
              An image is stored in the knowledge base under <code>.agents/icons</code>, so it travels with the
              instructions. Name them first, since the file is named after them. With no icon, the initials stand in.
            </>}>
              {control => (
                <div className="persona-icon-row">
                  <PersonaIcon icon={icon} name={name || '?'} size={34} />
                  <Input {...control} className="persona-icon-field" value={icon} onChange={e => setIcon(e.target.value)}
                    placeholder="an emoji, or upload an image" />
                  {/* aria-disabled draws the button disabled; the input inside is what is disabled. */}
                  <label className={`${buttonClasses()} agents-file`} aria-disabled={!name}
                    title={name ? 'Choose an image for these instructions' : 'Name the instructions first: the image file is named after them'}>
                    {name ? 'Upload image' : 'Upload image (name them first)'}
                    <input type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml,image/gif"
                      disabled={!name}
                      onChange={async e => {
                        const f = e.target.files?.[0]
                        if (!f || !name) return
                        const fd = new FormData()
                        fd.append('file', f)
                        const res = await fetch(`/api/extensions/persona-icon?name=${encodeURIComponent(name)}`, { method: 'POST', body: fd })
                        const d = await res.json()
                        if (res.ok) {
                          setIcon(d.icon)
                          setNote(d.attached ? 'Icon uploaded and attached.' : 'Icon uploaded. Save the instructions to attach it.')
                          if (d.attached) await load()
                        }
                        else setNote(d.error ?? `upload failed (${res.status})`)
                        e.target.value = ''
                      }} />
                  </label>
                  {icon && <Button variant="ghost" size="sm" onClick={() => setIcon('')}>clear</Button>}
                </div>
              )}
            </Field>
          )}
          <Textarea label="Markdown" className="agents-body" value={body} onChange={e => onBody(e.target.value)}
            placeholder={'Paste the file. Frontmatter is read into the fields above and rewritten on save.'} />
        </div>
        <div className="query-actions">
          <Button variant="primary" loading={busy} onClick={() => void save()} disabled={!name || !body}>
            {busy ? 'Saving…' : editing ? `Save ${editing}` : kind === 'persona' ? 'Save instructions' : 'Save skill'}
          </Button>
          {editing && (
            <>
              <Button variant="secondary" onClick={() => { setEditing(null); setName(''); setDescription(''); setBody(''); setNote('') }}>Cancel</Button>
              {kind === 'persona' && personas.find(p => p.name === editing)?.shared && (
                <Button variant="secondary" onClick={() => onOpen(`.agents/${editing}.md`)}>Read it rendered</Button>
              )}
            </>
          )}
          {note && <span className="muted">{note}</span>}
        </div>
      </section>
      </>)}
    </div>
  )
}
