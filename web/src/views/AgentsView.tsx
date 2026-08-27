import { useCallback, useEffect, useState } from 'react'

/**
 * Personas and skills: the two ways a markdown file changes how the
 * assistant behaves, in one place a user can find.
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

interface Persona { name: string; description: string; shared: boolean }
interface Skill { name: string; description: string; file: string }

export function AgentsView() {
  const [personas, setPersonas] = useState<Persona[]>([])
  const [skills, setSkills] = useState<Skill[]>([])
  const [dir, setDir] = useState('')
  const [kind, setKind] = useState<'persona' | 'skill'>('persona')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [body, setBody] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => fetch('/api/extensions').then(r => r.json())
    .then(d => { setPersonas(d.personas ?? []); setSkills(d.skills ?? []); setDir(d.corpusAgentDir ?? '') })
    .catch(() => {}), [])
  useEffect(() => { load() }, [load])

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
        body: JSON.stringify({ name, description, body, shared: true }),
      })
      const d = await res.json()
      if (!res.ok) throw new Error(d.error ?? `${res.status}`)
      setNote(`Saved ${d.name}. It is available in chat now.`)
      setName(''); setDescription(''); setBody('')
      await load()
    } catch (e) {
      setNote(String((e as Error).message))
    } finally { setBusy(false) }
  }

  const remove = async (n: string) => {
    await fetch(`/api/extensions/persona?name=${encodeURIComponent(n)}`, { method: 'DELETE' })
    await load()
  }

  const onFile = (f: File | undefined) => {
    if (!f) return
    f.text().then(t => { if (!name) setName(f.name.replace(/\.md$/, '')); onBody(t) })
  }

  return (
    <div className="overview-view">
      <div className="card ov-head">
        <h1 className="view-title">Agents</h1>
        <p className="muted view-sub">
          Markdown files that change how the assistant behaves. Write one here or drop in a file you already have.
        </p>
      </div>

      <div className="ov-grid">
        <section className="card ov-list">
          <h3>Personas ({personas.length})</h3>
          <p className="muted view-sub">
            A standing stance for a whole conversation, chosen from the Persona control in chat. Personas are stored
            in the knowledge base{dir ? <> at <code>{dir}</code></> : null}, so everyone working over this corpus has
            the same library.
          </p>
          {personas.map(p => (
            <div className="ov-row" key={p.name}>
              <span className="ov-row-path">{p.name}{p.shared && <span className="persona-shared">shared</span>}</span>
              <span className="ov-row-meta">
                {p.description || 'no description'}
                <button className="link-button agents-del" onClick={() => remove(p.name)}>remove</button>
              </span>
            </div>
          ))}
          {personas.length === 0 && <p className="muted">None yet. The first one you add appears in the chat Persona control.</p>}
        </section>

        <section className="card ov-list">
          <h3>Skills ({skills.length})</h3>
          <p className="muted view-sub">
            Task instructions the assistant loads itself, mid-conversation, when the work matches the description.
            No one selects a skill; write the description so the assistant can tell when it applies.
          </p>
          {skills.map(s => (
            <div className="ov-row" key={s.name}>
              <span className="ov-row-path">{s.name}</span>
              <span className="ov-row-meta">{s.description || 'no description'}</span>
            </div>
          ))}
          {skills.length === 0 && <p className="muted">None yet.</p>}
        </section>
      </div>

      <section className="card settings-card">
        <h3>Add one</h3>
        <div className="agents-kind">
          <button className={`btn-secondary ${kind === 'persona' ? 'active' : ''}`} onClick={() => setKind('persona')}>Persona</button>
          <button className={`btn-secondary ${kind === 'skill' ? 'active' : ''}`} onClick={() => setKind('skill')}>Skill</button>
          <label className="btn-secondary agents-file">
            Choose a .md file
            <input type="file" accept=".md,text/markdown" onChange={e => onFile(e.target.files?.[0])} />
          </label>
        </div>
        <div className="settings-grid">
          <div>
            <label className="field-label">Name</label>
            <input className="field" value={name} onChange={e => setName(e.target.value)} placeholder="proposal-reviewer" />
          </div>
          <div>
            <label className="field-label">Description</label>
            <input className="field" value={description} onChange={e => setDescription(e.target.value)}
              placeholder={kind === 'skill' ? 'when the assistant should load this' : 'what this persona is for'} />
          </div>
        </div>
        <label className="field-label">Markdown</label>
        <textarea className="field agents-body" value={body} onChange={e => onBody(e.target.value)}
          placeholder={'Paste the file. Frontmatter is read into the fields above and rewritten on save.'} />
        <div className="query-actions">
          <button className="btn-primary" onClick={() => void save()} disabled={busy || !name || !body}>
            {busy ? 'Saving…' : `Save ${kind}`}
          </button>
          {note && <span className="muted">{note}</span>}
        </div>
      </section>
    </div>
  )
}
