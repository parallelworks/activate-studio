import { statusLook } from '../statusLook'
import { useEffect, useState } from 'react'
import {
  Button, Callout, Checkbox, EmptyState, Field, Input, Select, StatusBadge, StatusDot, Textarea,
  type BadgeVariant,
} from '@parallelworks/ui'
import { PersonaIcon } from '../components/PersonaIcon'

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
interface Journal { at: string; kind: string; text: string; taskId?: string | null; session?: string }
interface SessionView { id: string; earlier?: string[]; recent: string[] }
interface Template { key: string; name: string; persona: string; every: string; onRunEnd: boolean; watchPath: string | null; goal: string }
interface Persona { name: string; description: string; icon: string }

const CADENCES = ['15m', '30m', '1h', '2h', '6h', '12h', '1d', 'manual']
const fmtTokens = (n: number) => n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
const when = (iso: string | null) => iso ? new Date(iso).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'never'
// Journal kinds here and board topics on the Tasks page share one set of tones.
const TOPIC_TONE: Record<string, BadgeVariant> = {
  complete: 'accent', result: 'accent',
  failed: 'warning', refused: 'warning', error: 'warning',
  spawn: 'info', message: 'info', event: 'info',
}
export const topicVariant = (topic: string): BadgeVariant => TOPIC_TONE[topic] ?? 'muted'

export function FleetPage({ personas, onOpenTask }: { personas: Persona[]; onOpenTask: (taskId: string) => void }) {
  const [agents, setAgents] = useState<Agent[]>([])
  const [templates, setTemplates] = useState<Template[]>([])
  const [open, setOpen] = useState<string | null>(null)
  const [detail, setDetail] = useState<{ agent: Agent; journal: Journal[]; session: SessionView | null } | null>(null)
  const [message, setMessage] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState({ name: '', goal: '', persona: '', model: '', resource: '', every: '1h', onRunEnd: true, watchPath: '', maxTicks: '', maxTokens: '', maxTurns: '' })

  const refresh = () => fetch('/api/fleet').then(r => r.json()).then(d => { setAgents(d.agents ?? []); setError(null) }).catch(() => {})
  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 10_000)
    fetch('/api/fleet/templates').then(r => r.json()).then(d => setTemplates(d.templates ?? [])).catch(() => {})
    return () => clearInterval(t)
  }, [])
  useEffect(() => {
    if (!open) { setDetail(null); return }
    let stop = false
    const load = () => fetch(`/api/fleet/${encodeURIComponent(open)}`).then(r => r.json()).then(d => { if (!stop && d.agent) setDetail({ agent: d.agent, journal: d.journal ?? [], session: d.session ?? null }) }).catch(() => {})
    load()
    const t = setInterval(load, 5_000)
    return () => { stop = true; clearInterval(t) }
  }, [open])

  const act = async (id: string, name: string, body?: Record<string, unknown>) => {
    const r = await fetch(`/api/fleet/${encodeURIComponent(id)}/${name}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body ?? {}) })
    if (!r.ok) { const d = await r.json().catch(() => ({})); setError(d.error ?? `${name} failed`) }
    refresh()
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
      {needsYou.length > 0 && (
        <div className="needs-you">
          <strong>Needs you</strong>
          {needsYou.map(a => (
            <button key={a.id} className="needs-you-item" onClick={() => setOpen(a.id)}>
              <StatusDot variant="warning" /> {a.name}: {a.note}
            </button>
          ))}
        </div>
      )}
      <div className="fleet-head">
        <h3>Standing agents ({live.length})</h3>
        <Button variant="primary" onClick={() => setCreating(c => !c)}>{creating ? 'Close' : 'New agent'}</Button>
      </div>
      {live.length === 0 && !creating && (
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
      {templates.length > 0 && (live.length === 0 || creating) && (
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
      {creating && (
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
                <PersonaIcon icon={iconOf(a.persona)} name={a.persona ?? a.name} size={18} />
              </div>
              <div className="task-card-objective">{a.name}</div>
              <div className="muted fleet-note">{a.note}</div>
              <div className="task-card-meta">
                <span>{a.ticks} wake-up{a.ticks === 1 ? '' : 's'}{a.tokens ? ` · ${fmtTokens(a.tokens)} tok` : ''}</span>
                <span>{a.state === 'working' ? 'running' : a.nextTickAt ? `next ${when(a.nextTickAt)}` : a.triggers.every === 'manual' ? 'on events' : ''}</span>
              </div>
            </div>
          ))}
        </div>
      )}
      {open && detail && (
        <div className="task-detail">
          <div className="task-detail-head">
            <StatusDot {...statusLook(detail.agent.state)} />
            <span className="task-detail-objective">{detail.agent.name}</span>
            <span className="muted">{detail.agent.persona ?? 'no instructions'} · {detail.agent.model || "pw code's default model"} · every {detail.agent.triggers.every}{detail.agent.triggers.onRunEnd ? ' · on run end' : ''}{detail.agent.triggers.watchPath ? ` · watches ${detail.agent.triggers.watchPath}/` : ''}</span>
            {detail.agent.state !== 'working' && detail.agent.state !== 'retired' && <Button variant="secondary" onClick={() => void act(detail.agent.id, 'tick')}>Wake now</Button>}
            {detail.agent.state === 'paused' ? <Button variant="secondary" onClick={() => void act(detail.agent.id, 'resume')}>Resume</Button>
              : detail.agent.state !== 'retired' && <Button variant="secondary" onClick={() => void act(detail.agent.id, 'pause')}>Pause</Button>}
            {detail.agent.state !== 'retired' && <Button variant="danger" onClick={() => { void act(detail.agent.id, 'retire'); setOpen(null) }}>Retire</Button>}
            {detail.agent.session && <Button variant="ghost" size="sm" href={`#view=code?session=${encodeURIComponent(detail.agent.session)}`}>its session on the Code page</Button>}
          </div>
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
                <span className="task-msg-meta"><StatusBadge variant={topicVariant(e.kind)} className="task-topic">{e.kind}</StatusBadge>{e.at.slice(0, 16).replace('T', ' ')}{e.taskId ? <> · <Button variant="ghost" size="sm" onClick={() => onOpenTask(e.taskId!)}>task</Button></> : null}</span>
                <span className="fleet-journal-text">{e.text}</span>
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
    </section>
  )
}
