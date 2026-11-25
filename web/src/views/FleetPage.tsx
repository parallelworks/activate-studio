import { statusLook } from '../statusLook'
import { useEffect, useState } from 'react'
import {
  Button, Callout, Checkbox, ConfirmModal, EmptyState, Field, Input, Select, StatusBadge, StatusDot, Textarea,
  type BadgeVariant,
} from '@parallelworks/ui'
import { Streamdown } from 'streamdown'
import { PersonaIcon } from '../components/PersonaIcon'
import { commandHead } from '../commandHead'
import { NeedsActivate } from '../components/NeedsActivate'
import { needsActivate } from '../accounts'
import { readNow, shownInterval, useWatch } from '../live'
import { MachineLabel } from '../components/MachineLabel'

/**
 * The operator base: standing agents with goals, their state, their
 * journals, and the decisions they are waiting on. Each is a long-lived
 * pw code session in its own library folder, whose AGENTS.md carries its
 * instructions and goal; a wake-up is a message to that session, which
 * the Code page shows too.
 */
interface Agent {
  id: string; name: string; goal: string; persona: string | null; model: string
  resource: string | null
  triggers: { every: string; onRunEnd: boolean; watchPath: string | null }
  budget: { maxTicks: number | null; maxTokens: number | null; maxTurns?: number | null }
  state: string; note: string; lastTickAt: string | null; nextTickAt: string | null
  ticks: number; tokens: number
  folder?: string; session?: string; sessions?: string[]; migrated?: string
}
/** What an agent's session waits on (GET /api/fleet/<id>): a call to
 *  approve when kind is confirm, else a plan or a question. */
export interface Approval {
  id: string; kind: string; tool?: string; command?: string; commandCut?: boolean; commandLength?: number
  reason?: string; approvable: boolean
}
interface Journal { at: string; kind: string; text: string; taskId?: string | null; session?: string }
interface SessionView { id: string; earlier?: string[]; recent: string[] }
interface Template { key: string; name: string; persona: string; every: string; onRunEnd: boolean; watchPath: string | null; goal: string }
interface Persona { name: string; description: string; icon: string }

const CADENCES = ['15m', '30m', '1h', '2h', '6h', '12h', '1d', 'manual']
const fmtTokens = (n: number) => n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
/** A card's next wake-up: the time alone today, so the footer keeps to one
 *  line on a card 240 px wide. */
export function nextWake(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const today = d.toDateString() === now.toDateString()
  return d.toLocaleString([], today ? { hour: 'numeric', minute: '2-digit' } : { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}
const when = (iso: string | null) => iso ? new Date(iso).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'never'
// Journal kinds here and board topics on the Tasks page share one set of tones.
const TOPIC_TONE: Record<string, BadgeVariant> = {
  complete: 'accent', result: 'accent',
  failed: 'warning', refused: 'warning', error: 'warning',
  spawn: 'info', message: 'info', event: 'info',
}
export const topicVariant = (topic: string): BadgeVariant => TOPIC_TONE[topic] ?? 'muted'

/** Tells the Status panel the fleet changed, so it reads it again now. */
export const FLEET_CHANGED = 'ade:fleet-changed'

/** A journal time in the reader's own time. */
export const journalTime = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso.slice(0, 16).replace('T', ' ') : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function FleetPage({ active = true, personas, onOpenTask }: { active?: boolean; personas: Persona[]; onOpenTask: (taskId: string) => void }) {
  const [agents, setAgents] = useState<Agent[]>([])
  const [templates, setTemplates] = useState<Template[]>([])
  const [open, setOpen] = useState<string | null>(null)
  // #view=agents:fleet?agent=<id> (Status Activity's link) opens that agent.
  // A hand-made link whose id does not decode opens none.
  useEffect(() => {
    const pick = () => {
      const m = location.hash.match(/^#view=agents:fleet\?(?:.*&)?agent=([^&]+)/)
      if (!m) return
      try { setOpen(decodeURIComponent(m[1])) } catch { /* not an id */ }
    }
    pick()
    window.addEventListener('hashchange', pick)
    return () => window.removeEventListener('hashchange', pick)
  }, [])
  // The approval whose whole command is on screen: Approve runs it whole,
  // so it is offered only then, as on the Code page. A call with a tool and
  // no command has nothing to open and is approvable as it is.
  const [whole, setWhole] = useState('')
  const [detail, setDetail] = useState<{ agent: Agent; journal: Journal[]; session: SessionView | null; approval?: Approval | null } | null>(null)
  const [message, setMessage] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Why no agent runs here now (no ACTIVATE in use, or a Studio several
  // people use), from GET /api/fleet; null when they may.
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const [retiring, setRetiring] = useState<Agent | null>(null)
  // Bumped after an action, so the open agent is read again at once.
  const [acted, setActed] = useState(0)
  const [form, setForm] = useState({ name: '', goal: '', persona: '', model: '', resource: '', every: '1h', onRunEnd: true, watchPath: '', maxTicks: '', maxTokens: '', maxTurns: '' })

  useWatch<{ agents?: Agent[]; unavailable?: string | null }>('/api/fleet', 10_000, active, d => { setAgents(d.agents ?? []); setUnavailable(d.unavailable ?? null); setError(null) })
  const refresh = () => { readNow('/api/fleet').catch(() => {}) }
  useEffect(() => {
    fetch('/api/fleet/templates').then(r => r.json()).then(d => setTemplates(d.templates ?? [])).catch(() => {})
  }, [])
  useEffect(() => {
    if (!open) { setDetail(null); return }
    if (!active) return
    let stop = false
    const load = () => fetch(`/api/fleet/${encodeURIComponent(open)}`).then(r => r.json()).then(d => { if (!stop && d.agent) setDetail({ agent: d.agent, journal: d.journal ?? [], session: d.session ?? null, approval: d.approval ?? null }) }).catch(() => {})
    load()
    const t = shownInterval(load, 5_000)
    return () => { stop = true; t() }
  }, [open, acted, active])

  const act = async (id: string, name: string, body?: Record<string, unknown>) => {
    const r = await fetch(`/api/fleet/${encodeURIComponent(id)}/${name}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })
    if (!r.ok) { const d = await r.json().catch(() => ({})); setError(d.error ?? `${name} failed`) }
    refresh()
    setActed(n => n + 1)
    window.dispatchEvent(new Event(FLEET_CHANGED))
  }
  const useTemplate = (t: Template) => {
    setForm(f => ({ ...f, name: t.name, goal: t.goal, persona: t.persona, every: t.every, onRunEnd: t.onRunEnd, watchPath: t.watchPath ?? '' }))
    setCreating(true)
    setTimeout(() => document.querySelector('.fleet-form')?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50)
  }
  const create = async () => {
    setError(null)
    const r = await fetch('/api/fleet', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      name: form.name, goal: form.goal, persona: form.persona || null, model: form.model.trim(),
      resource: form.resource || null, every: form.every, onRunEnd: form.onRunEnd, watchPath: form.watchPath || null,
      maxTicks: form.maxTicks ? Number(form.maxTicks) : null, maxTokens: form.maxTokens ? Number(form.maxTokens) : null,
      maxTurns: form.maxTurns ? Number(form.maxTurns) : null,
    }) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) { setError(d.error ?? d.message ?? 'could not create the agent'); return }
    setCreating(false); setForm(f => ({ ...f, name: '', goal: '' })); refresh(); setOpen(d.agent?.id ?? null)
  }

  const needsYou = agents.filter(a => a.state === 'input-required')
  const live = agents.filter(a => a.state !== 'retired')
  const iconOf = (name: string | null) => personas.find(p => p.name === name)?.icon ?? ''

  return (
    <section className="card task-board-card">
      {error && <Callout tone="error" size="sm" live>{error}</Callout>}
      {/* Who needs you sits in the header row, so it comes and goes
          without moving anything below it. */}
      <div className="fleet-head">
        <h3>Standing agents ({live.length})</h3>
        {needsYou.length > 0 && (
          <div className="needs-you" role="group" aria-label="Needs you">
            <strong>Needs you</strong>
            {needsYou.map(a => (
              <button key={a.id} className="needs-you-item" onClick={() => setOpen(a.id)} title={a.note}>
                <StatusDot variant="warning" /> {a.name}
              </button>
            ))}
          </div>
        )}
        {!unavailable && <Button variant="primary" onClick={() => setCreating(c => !c)}>{creating ? 'Close' : 'New agent'}</Button>}
      </div>
      {unavailable && (needsActivate(unavailable)
        ? <NeedsActivate>{unavailable}</NeedsActivate>
        : <Callout tone="info" size="sm">{unavailable}</Callout>)}
      {live.length === 0 && !creating && !unavailable && (
        <EmptyState variant="bare" className="task-empty" title="A standing agent keeps a goal across time."
          description={<>
            <span>
              It is a pw code session in a library folder of its own, whose <code>AGENTS.md</code> holds its instructions and goal.
              It wakes on a schedule, when a run ends, or when files change, does one bounded round of work with pw code's tools and
              the Studio's, reports what it did, and asks you only when a decision needs a person.
            </span>
            <span>Start from a template, or write a goal of your own.</span>
          </>} />
      )}
      {!unavailable && templates.length > 0 && (live.length === 0 || creating) && (
        <div className="fleet-templates">
          {templates.map(t => (
            <button key={t.key} className="fleet-template" onClick={() => useTemplate(t)} title={t.goal}>
              <PersonaIcon icon={iconOf(t.persona)} name={t.persona} size={18} />
              <span className="fleet-template-name">{t.name}</span>
              <span className="muted">{t.persona} · {t.every}{t.onRunEnd ? ' · on run end' : ''}{t.watchPath ? ` · watches ${t.watchPath}/` : ''}</span>
            </button>
          ))}
        </div>
      )}
      {creating && !unavailable && (
        <div className="fleet-form">
          <Input label="Name" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="Nightly benchmark campaign" />
          <Textarea label="Goal" className="fleet-goal" value={form.goal} onChange={e => setForm({ ...form, goal: e.target.value })} placeholder="What this agent keeps doing, and what it should never do." />
          <div className="fleet-grid">
            <Field label="Instructions">
              <Select value={form.persona} onValueChange={v => setForm({ ...form, persona: v ?? '' })}
                items={[{ value: '', label: 'none' }, ...personas.map(p => ({ value: p.name, label: p.name }))]} />
            </Field>
            <Input label="pw code model" value={form.model} onChange={e => setForm({ ...form, model: e.target.value })} placeholder="pw code's default" />
            <Input label="System it works on (named in its instructions)" value={form.resource} onChange={e => setForm({ ...form, resource: e.target.value })} placeholder="my-cluster" />
            <Field label="Every">
              <Select value={form.every} onValueChange={v => v && setForm({ ...form, every: v })}
                items={CADENCES.map(c => ({ value: c, label: c }))} />
            </Field>
            <Input label="Watch folder (knowledge base path)" value={form.watchPath} onChange={e => setForm({ ...form, watchPath: e.target.value })} placeholder="tasks" />
            <Input label="Turns per wake-up" value={form.maxTurns} onChange={e => setForm({ ...form, maxTurns: e.target.value })} placeholder="10" />
            <Input label="Max wake-ups" value={form.maxTicks} onChange={e => setForm({ ...form, maxTicks: e.target.value })} placeholder="unlimited" />
            <Input label="Max tokens" value={form.maxTokens} onChange={e => setForm({ ...form, maxTokens: e.target.value })} placeholder="unlimited" />
          </div>
          <label className="fleet-check"><Checkbox checked={form.onRunEnd} onCheckedChange={c => setForm({ ...form, onRunEnd: c })} /> Wake when a workflow run ends</label>
          <div className="query-actions">
            <Button variant="primary" onClick={() => void create()} disabled={!form.name.trim() || !form.goal.trim()}>Create agent</Button>
            <Button variant="secondary" onClick={() => setCreating(false)}>Cancel</Button>
          </div>
        </div>
      )}
      {live.length > 0 && (
        <div className="fleet-cards">
          {live.map(a => (
            <div key={a.id} className={`task-card fleet-card${open === a.id ? ' selected' : ''}${a.state === 'working' ? ' live' : ''}`} onClick={() => setOpen(open === a.id ? null : a.id)}>
              <div className="task-card-top">
                <StatusDot {...statusLook(a.state)} />
                <span className="task-card-state">{a.state}</span>
                {a.resource && <MachineLabel machine={{ name: a.resource }} className="task-card-machine" />}
                <PersonaIcon icon={iconOf(a.persona)} name={a.persona ?? a.name} size={18} />
              </div>
              <div className="task-card-objective">{a.name}</div>
              <div className="muted fleet-note">{a.note}</div>
              <div className="task-card-meta">
                <span title={a.nextTickAt ? `next wake-up ${when(a.nextTickAt)}` : undefined}>{a.ticks} wake-up{a.ticks === 1 ? '' : 's'}{a.tokens ? ` · ${fmtTokens(a.tokens)} tok` : ''}</span>
                <span>{a.state === 'working' ? 'running' : a.nextTickAt ? `next ${nextWake(a.nextTickAt)}` : a.triggers.every === 'manual' ? 'on events' : ''}</span>
              </div>
            </div>
          ))}
        </div>
      )}
      {open && detail && (
        <div className="task-detail">
          <div className="fleet-detail-head">
            <div className="fleet-detail-title">
              <span className="fleet-detail-name"><StatusDot {...statusLook(detail.agent.state)} />{detail.agent.name}</span>
              <span className="muted fleet-detail-meta">{detail.agent.persona ?? 'no instructions'} · {detail.agent.model || "pw code's default model"} · {detail.agent.triggers.every === 'manual' ? 'no schedule' : `every ${detail.agent.triggers.every}`}{detail.agent.triggers.onRunEnd ? ' · on run end' : ''}{detail.agent.triggers.watchPath ? ` · watches ${detail.agent.triggers.watchPath}/` : ''}</span>
            </div>
            <div className="fleet-detail-actions">
              {/* The actions keep their places: Wake now stays, disabled, while a
                  round runs, and the session link's place is held before there is one. */}
              {detail.agent.state !== 'retired' && <Button variant="secondary" size="sm" disabled={detail.agent.state === 'working'}
                title={detail.agent.state === 'working' ? 'It is working now' : undefined} onClick={() => void act(detail.agent.id, 'tick')}>Wake now</Button>}
              {detail.agent.state === 'paused' ? <Button variant="secondary" size="sm" onClick={() => void act(detail.agent.id, 'resume')}>Resume</Button>
                : detail.agent.state !== 'retired' && <Button variant="secondary" size="sm" onClick={() => void act(detail.agent.id, 'pause')}>Pause</Button>}
              {detail.agent.state !== 'retired' && <Button variant="danger" size="sm" onClick={() => setRetiring(detail.agent)}>Retire</Button>}
              {detail.agent.session
                ? <Button variant="ghost" size="sm" href={`#view=code?session=${encodeURIComponent(detail.agent.session)}`}>Its session on the Code page</Button>
                : <span className="muted fleet-no-session">No session until its first wake-up</span>}
            </div>
          </div>
          {detail.agent.state === 'input-required' && (
            <Callout tone="warning" size="sm" className="fleet-needs">
              {detail.approval?.kind === 'confirm'
                ? <>
                    It waits for you to approve this call in its session{detail.approval.tool ? <> ({detail.approval.tool})</> : null}:
                    {detail.approval.command && <WholeCommand key={detail.approval.id} text={detail.approval.command} length={detail.approval.commandLength} onWhole={shown => setWhole(shown ? detail.approval!.id : '')} />}
                    {detail.approval.commandCut && (
                      <span className="fleet-approval-cut">The command is {detail.approval.commandLength?.toLocaleString()} characters, too long to show whole here, so it can only be denied.</span>
                    )}
                    {!detail.approval.command && detail.approval.reason && <span className="fleet-approval-reason">{detail.approval.reason}</span>}
                    <span className="fleet-approval-actions">
                      {detail.approval.approvable && (!detail.approval.command || whole === detail.approval.id) && <Button variant="primary" size="sm" onClick={() => void act(detail.agent.id, 'approve', { approval: detail.approval!.id, allowed: true })}>Approve</Button>}
                      <Button variant="secondary" size="sm" onClick={() => void act(detail.agent.id, 'approve', { approval: detail.approval!.id, allowed: false })}>Deny</Button>
                    </span>
                  </>
                : detail.approval
                ? <>It waits for you on a {detail.approval.kind || 'request'} in its session{detail.approval.reason ? <>: {detail.approval.reason}</> : null}. Answer it in <a href={`#view=code?session=${encodeURIComponent(detail.agent.session ?? '')}`}>its session on the Code page</a>.</>
                : detail.agent.note.startsWith('waiting for your approval')
                ? <>It waits for you to approve a call in its session: approve or deny it on <a href={`#view=code?session=${encodeURIComponent(detail.agent.session ?? '')}`}>the Code page</a>.</>
                : <>It asks: {detail.agent.note}{/[.?!]$/.test(detail.agent.note) ? '' : '.'} Answer below.</>}
            </Callout>
          )}
          {detail.agent.migrated && <Callout tone="info" size="sm" className="fleet-migrated">{detail.agent.migrated}</Callout>}
          <p className="muted fleet-goal-text">{detail.agent.goal}</p>
          {detail.agent.folder && (
            <p className="muted fleet-folder">Works in <a href={`#open=file:${detail.agent.folder}/AGENTS.md`}>{detail.agent.folder}</a>, whose AGENTS.md holds its instructions and goal.</p>
          )}
          {detail.session && detail.session.recent.length > 0 && (
            <details className="fleet-session">
              <summary>Its session, latest first ({detail.session.recent.length} lines)</summary>
              {detail.session.recent.slice().reverse().map((l, i) => <p key={i} className="fleet-session-line">{l}</p>)}
            </details>
          )}
          <div className="fleet-journal">
            {detail.journal.length === 0 && <p className="muted">No journal yet.</p>}
            {detail.journal.slice().reverse().map((e, i) => (
              <div key={i} className="task-msg">
                <span className="task-msg-meta"><StatusBadge variant={topicVariant(e.kind)} className="task-topic">{e.kind}</StatusBadge>{journalTime(e.at)}{e.taskId ? <> · <Button variant="ghost" size="sm" onClick={() => onOpenTask(e.taskId!)}>task</Button></> : null}</span>
                {e.kind === 'result'
                  ? <div className="fleet-journal-text md-body"><Streamdown>{e.text}</Streamdown></div>
                  : <span className="fleet-journal-text">{e.text}</span>}
              </div>
            ))}
          </div>
          <div className="fleet-message">
            <Input size="sm" value={message} placeholder="Tell the agent something: it hears it now while it works, or wakes now to answer." onChange={e => setMessage(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && message.trim()) { void act(detail.agent.id, 'message', { text: message }); setMessage('') } }} />
            <Button variant="secondary" size="sm" disabled={!message.trim()} onClick={() => { void act(detail.agent.id, 'message', { text: message }); setMessage('') }}>Send</Button>
          </div>
        </div>
      )}
      <ConfirmModal open={!!retiring} destructive onClose={() => setRetiring(null)}
        title={retiring ? `Retire ${retiring.name}?` : 'Retire this agent?'}
        description="It stops for good: no more wake-ups, and its pw code session is archived, where the Code page still shows it. Its folder, AGENTS.md and journal stay."
        confirmLabel="Retire"
        onConfirm={() => { if (retiring) { void act(retiring.id, 'retire'); setOpen(null) } setRetiring(null) }} />
    </section>
  )
}

/** A command waiting for approval: whole when short, else its first lines
 *  until opened. onWhole names the approval once the whole command shows. */
function WholeCommand({ text, length, onWhole }: { text: string; length?: number; onWhole: (shown: boolean) => void }) {
  const { head, long, lines } = commandHead(text)
  const [open, setOpen] = useState(false)
  const shown = open || !long
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { onWhole(shown) }, [shown])
  return (
    <>
      <pre className="fleet-approval-text">{shown ? text : `${head}…`}</pre>
      {!shown && (
        <span className="fleet-approval-cut">
          <button type="button" className="code-approval-more" onClick={() => setOpen(true)}>
            Show the whole command ({lines.toLocaleString()} lines, {(length ?? text.length).toLocaleString()} characters)
          </button> to approve it.
        </span>
      )}
    </>
  )
}
