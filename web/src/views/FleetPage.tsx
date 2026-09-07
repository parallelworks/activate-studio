import { useEffect, useState } from 'react'
import { PersonaIcon } from '../components/PersonaIcon'

/**
 * The operator base: standing agents with goals, their state, their
 * journals, and the decisions they are waiting on. Everything here reads
 * from the fleet routes; a tick is one delegated task, so the Tasks page
 * shows the same work from the other side.
 */
interface Agent {
  id: string; name: string; goal: string; persona: string | null; model: string
  execution: 'local' | 'campaign'; resource: string | null
  triggers: { every: string; onRunEnd: boolean; watchPath: string | null }
  budget: { maxTicks: number | null; maxTokens: number | null }
  state: string; note: string; lastTickAt: string | null; nextTickAt: string | null
  ticks: number; tokens: number; currentTaskId: string | null
}
interface Journal { at: string; kind: string; text: string; taskId?: string | null }
interface Template { key: string; name: string; persona: string; every: string; onRunEnd: boolean; watchPath: string | null; execution: 'local' | 'campaign'; goal: string }
interface Persona { name: string; description: string; icon: string }

const CADENCES = ['15m', '30m', '1h', '2h', '6h', '12h', '1d', 'manual']
const fmtTokens = (n: number) => n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)}M` : n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
const when = (iso: string | null) => iso ? new Date(iso).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : 'never'
const dotFor = (st: string) => st === 'working' ? 'ok pulse' : st === 'idle' ? 'ok' : st === 'input-required' ? 'warn' : 'off'

export function FleetPage({ personas, onOpenTask }: { personas: Persona[]; onOpenTask: (taskId: string) => void }) {
  const [agents, setAgents] = useState<Agent[]>([])
  const [templates, setTemplates] = useState<Template[]>([])
  const [models, setModels] = useState<string[]>([])
  const [open, setOpen] = useState<string | null>(null)
  const [detail, setDetail] = useState<{ agent: Agent; journal: Journal[] } | null>(null)
  const [message, setMessage] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState({ name: '', goal: '', persona: '', model: '', execution: 'local' as 'local' | 'campaign', resource: '', every: '1h', onRunEnd: true, watchPath: '', maxTicks: '', maxTokens: '' })

  const refresh = () => fetch('/api/fleet').then(r => r.json()).then(d => { setAgents(d.agents ?? []); setError(null) }).catch(() => {})
  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 10_000)
    fetch('/api/fleet/templates').then(r => r.json()).then(d => setTemplates(d.templates ?? [])).catch(() => {})
    fetch('/api/chat/models').then(r => r.json()).then(d => {
      const ids = (d.models ?? []).filter((m: { callable?: boolean }) => m.callable !== false).map((m: { id: string }) => m.id)
      setModels(ids)
      setForm(f => f.model ? f : { ...f, model: ids[0] ?? '' })
    }).catch(() => {})
    return () => clearInterval(t)
  }, [])
  useEffect(() => {
    if (!open) { setDetail(null); return }
    let stop = false
    const load = () => fetch(`/api/fleet/${encodeURIComponent(open)}`).then(r => r.json()).then(d => { if (!stop && d.agent) setDetail({ agent: d.agent, journal: d.journal ?? [] }) }).catch(() => {})
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
    setForm(f => ({ ...f, name: t.name, goal: t.goal, persona: t.persona, execution: t.execution, every: t.every, onRunEnd: t.onRunEnd, watchPath: t.watchPath ?? '' }))
    setCreating(true)
    setTimeout(() => document.querySelector('.fleet-form')?.scrollIntoView({ behavior: 'smooth', block: 'start' }), 50)
  }
  const create = async () => {
    setError(null)
    const r = await fetch('/api/fleet', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      name: form.name, goal: form.goal, persona: form.persona || null, model: form.model,
      execution: form.execution, resource: form.resource || null, every: form.every, onRunEnd: form.onRunEnd, watchPath: form.watchPath || null,
      maxTicks: form.maxTicks ? Number(form.maxTicks) : null, maxTokens: form.maxTokens ? Number(form.maxTokens) : null,
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
      {error && <p className="fleet-error">{error}</p>}
      {needsYou.length > 0 && (
        <div className="needs-you">
          <strong>Needs you</strong>
          {needsYou.map(a => (
            <button key={a.id} className="needs-you-item" onClick={() => setOpen(a.id)}>
              <span className="status-dot warn" /> {a.name}: {a.note}
            </button>
          ))}
        </div>
      )}
      <div className="fleet-head">
        <h3>Standing agents ({live.length})</h3>
        <button className="btn-primary" onClick={() => setCreating(c => !c)}>{creating ? 'Close' : 'New agent'}</button>
      </div>
      {live.length === 0 && !creating && (
        <div className="task-empty">
          <p className="muted view-sub">
            A standing agent keeps a goal across time. It wakes on a schedule, when a run ends, or when files change, does one bounded round of
            work with the same tools the assistant has, writes what it did to its journal, and asks you only when a decision needs a person.
          </p>
          <p className="muted view-sub">Start from a template, or write a goal of your own.</p>
        </div>
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
          <label className="field-label">Name</label>
          <input className="field" value={form.name} onChange={e => setForm({ ...form, name: e.target.value })} placeholder="Nightly benchmark campaign" />
          <label className="field-label">Goal</label>
          <textarea className="field fleet-goal" value={form.goal} onChange={e => setForm({ ...form, goal: e.target.value })} placeholder="What this agent keeps doing, and what it should never do." />
          <div className="fleet-grid">
            <label className="field-label">Persona
              <select className="field" value={form.persona} onChange={e => setForm({ ...form, persona: e.target.value })}>
                <option value="">none</option>
                {personas.map(p => <option key={p.name} value={p.name}>{p.name}</option>)}
              </select>
            </label>
            <label className="field-label">Model
              <select className="field" value={form.model} onChange={e => setForm({ ...form, model: e.target.value })}>
                {models.map(m => <option key={m} value={m}>{m}</option>)}
              </select>
            </label>
            <label className="field-label">Runs
              <select className="field" value={form.execution} onChange={e => setForm({ ...form, execution: e.target.value as 'local' | 'campaign' })}>
                <option value="local">beside the studio</option>
                <option value="campaign">as a workflow run on a system</option>
              </select>
            </label>
            <label className="field-label">System (for campaign ticks, and for the goal)
              <input className="field" value={form.resource} onChange={e => setForm({ ...form, resource: e.target.value })} placeholder="a30gpuserver" />
            </label>
            <label className="field-label">Every
              <select className="field" value={form.every} onChange={e => setForm({ ...form, every: e.target.value })}>
                {CADENCES.map(c => <option key={c} value={c}>{c}</option>)}
              </select>
            </label>
            <label className="field-label">Watch folder (knowledge base path)
              <input className="field" value={form.watchPath} onChange={e => setForm({ ...form, watchPath: e.target.value })} placeholder="tasks" />
            </label>
            <label className="field-label">Max ticks
              <input className="field" value={form.maxTicks} onChange={e => setForm({ ...form, maxTicks: e.target.value })} placeholder="unlimited" />
            </label>
            <label className="field-label">Max tokens
              <input className="field" value={form.maxTokens} onChange={e => setForm({ ...form, maxTokens: e.target.value })} placeholder="unlimited" />
            </label>
          </div>
          <label className="fleet-check"><input type="checkbox" checked={form.onRunEnd} onChange={e => setForm({ ...form, onRunEnd: e.target.checked })} /> Wake when a workflow run ends</label>
          <div className="query-actions">
            <button className="btn-primary" onClick={() => void create()} disabled={!form.name.trim() || !form.goal.trim() || !form.model}>Create agent</button>
            <button className="btn-secondary" onClick={() => setCreating(false)}>Cancel</button>
          </div>
        </div>
      )}
      {live.length > 0 && (
        <div className="fleet-cards">
          {live.map(a => (
            <div key={a.id} className={`task-card fleet-card${open === a.id ? ' selected' : ''}${a.state === 'working' ? ' live' : ''}`} onClick={() => setOpen(open === a.id ? null : a.id)}>
              <div className="task-card-top">
                <span className={`status-dot ${dotFor(a.state)}`} />
                <span className="task-card-state">{a.state}</span>
                <PersonaIcon icon={iconOf(a.persona)} name={a.persona ?? a.name} size={18} />
              </div>
              <div className="task-card-objective">{a.name}</div>
              <div className="muted fleet-note">{a.note}</div>
              <div className="task-card-meta">
                <span>{a.ticks} tick{a.ticks === 1 ? '' : 's'}{a.tokens ? ` · ${fmtTokens(a.tokens)} tok` : ''}</span>
                <span>{a.state === 'working' ? 'running' : a.nextTickAt ? `next ${when(a.nextTickAt)}` : a.triggers.every === 'manual' ? 'on events' : ''}</span>
              </div>
            </div>
          ))}
        </div>
      )}
      {open && detail && (
        <div className="task-detail">
          <div className="task-detail-head">
            <span className={`status-dot ${dotFor(detail.agent.state)}`} />
            <span className="task-detail-objective">{detail.agent.name}</span>
            <span className="muted">{detail.agent.persona ?? 'no persona'} · {detail.agent.model} · every {detail.agent.triggers.every}{detail.agent.triggers.onRunEnd ? ' · on run end' : ''}{detail.agent.triggers.watchPath ? ` · watches ${detail.agent.triggers.watchPath}/` : ''}</span>
            {detail.agent.state !== 'working' && detail.agent.state !== 'retired' && <button className="btn-secondary" onClick={() => void act(detail.agent.id, 'tick')}>Tick now</button>}
            {detail.agent.state === 'paused' ? <button className="btn-secondary" onClick={() => void act(detail.agent.id, 'resume')}>Resume</button>
              : detail.agent.state !== 'retired' && <button className="btn-secondary" onClick={() => void act(detail.agent.id, 'pause')}>Pause</button>}
            {detail.agent.state !== 'retired' && <button className="btn-danger-outline" onClick={() => { void act(detail.agent.id, 'retire'); setOpen(null) }}>Retire</button>}
            {detail.agent.currentTaskId && <button className="link-button" onClick={() => onOpenTask(detail.agent.currentTaskId!)}>view the running tick</button>}
          </div>
          <p className="muted fleet-goal-text">{detail.agent.goal}</p>
          <div className="fleet-journal">
            {detail.journal.length === 0 && <p className="muted">No journal yet.</p>}
            {detail.journal.slice().reverse().map((e, i) => (
              <div key={i} className="task-msg">
                <span className="task-msg-meta"><span className={`task-topic t-${e.kind}`}>{e.kind}</span>{e.at.slice(0, 16).replace('T', ' ')}{e.taskId ? <> · <button className="link-button" onClick={() => onOpenTask(e.taskId!)}>task</button></> : null}</span>
                <span className="fleet-journal-text">{e.text}</span>
              </div>
            ))}
          </div>
          <div className="fleet-message">
            <input className="field" value={message} placeholder="Tell the agent something; it answers on its next tick, which starts now." onChange={e => setMessage(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter' && message.trim()) { void act(detail.agent.id, 'message', { text: message }); setMessage('') } }} />
            <button className="btn-secondary" disabled={!message.trim()} onClick={() => { void act(detail.agent.id, 'message', { text: message }); setMessage('') }}>Send</button>
          </div>
        </div>
      )}
    </section>
  )
}
