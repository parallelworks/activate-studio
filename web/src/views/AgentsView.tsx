import { useCallback, useEffect, useState } from 'react'
import { PersonaIcon } from '../components/PersonaIcon'

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

interface Persona { name: string; description: string; shared: boolean; icon: string }
interface Skill { name: string; description: string; file: string }
interface MissionAgent { name: string; persona: string; objective: string; parent: string | null; depth: number; status: string; note: string; resultPath: string | null; updatedAt: string }
interface MissionRow { id: string; objective: string; status: string; agents: number; running: number }
interface MissionDetail extends MissionRow { maxAgents: number; maxDepth: number; agents2?: never; board: { seq: number; at: string; from: string; topic: string; body: string }[] }

export function AgentsView({ onOpen }: { onOpen: (path: string) => void }) {
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
  // Missions: the fleet view. Polled while one is running, since the
  // point is watching agents work.
  const [missions, setMissions] = useState<MissionRow[]>([])
  const [openMission, setOpenMission] = useState<string | null>(null)
  const [detail, setDetail] = useState<(Omit<MissionDetail, 'agents'> & { agents: MissionAgent[] }) | null>(null)
  // Paged rather than one long scroll: a library of sixty is browsed in
  // chunks, and the count line says where you are in it. Filtering resets
  // to the first page, since page four of the old result set means
  // nothing against the new one.
  const [pPage, setPPage] = useState(0)
  const [sPage, setSPage] = useState(0)
  useEffect(() => { setPPage(0); setSPage(0) }, [filter])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let stop = false
    const tick = async () => {
      try {
        const d = await fetch('/api/missions').then(r => r.json())
        if (!stop) setMissions(d.missions ?? [])
        if (openMission && !stop) {
          const md = await fetch(`/api/missions/${encodeURIComponent(openMission)}`).then(r => r.json())
          if (!stop && !md.error) setDetail(md)
        }
      } catch { /* next tick */ }
      const anyRunning = missions.some(x => x.status === 'running') || detail?.status === 'running'
      if (!stop) t = window.setTimeout(tick, anyRunning ? 3000 : 15000)
    }
    let t = window.setTimeout(tick, 0)
    return () => { stop = true; clearTimeout(t) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openMission])

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
        body: JSON.stringify({ name, description, body, icon, shared: true }),
      })
      const d = await res.json()
      if (!res.ok) throw new Error(d.error ?? `${res.status}`)
      setNote(`Saved ${d.name}. It is available in chat now.`)
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
  const pager = (rows: unknown[], page: number, setPage: (n: number) => void) => {
    if (rows.length <= PAGE) return null
    const last = Math.ceil(rows.length / PAGE) - 1
    const from = page * PAGE + 1
    const to = Math.min(rows.length, page * PAGE + PAGE)
    return (
      <div className="agents-pager">
        <span className="muted">{from}–{to} of {rows.length}</span>
        <span className="agents-pager-btns">
          <button className="btn-secondary" disabled={page === 0} onClick={() => setPage(page - 1)}>Previous</button>
          <button className="btn-secondary" disabled={page >= last} onClick={() => setPage(page + 1)}>Next</button>
        </span>
      </div>
    )
  }

  const onFile = (f: File | undefined) => {
    if (!f) return
    f.text().then(t => { if (!name) setName(f.name.replace(/\.md$/, '')); onBody(t) })
  }

  return (
    <div className="overview-view agents-view">
      <div className="card ov-head">
        <h1 className="view-title">Agents</h1>
        <p className="muted view-sub">
          Markdown files that change how the assistant behaves. Write one here or drop in a file you already have.
        </p>
      </div>

      {missions.length > 0 && (
        <section className="card ov-list">
          <h3>Missions ({missions.length})</h3>
          <p className="muted view-sub">
            Coordinated agent runs started from chat. Each agent is a headless run; results are files under
            <code> missions/</code> in the knowledge base, and the board records what happened.
          </p>
          {missions.map(mi => (
            <div className="ov-row" key={mi.id} onClick={() => setOpenMission(openMission === mi.id ? null : mi.id)}>
              <span className="ov-row-path">
                <span className={`status-dot ${mi.status === 'running' ? 'ok' : mi.status === 'done' ? 'off' : 'warn'}`} /> {mi.id}
              </span>
              <span className="ov-row-meta">{mi.running} running of {mi.agents} · {mi.status}</span>
            </div>
          ))}
          {openMission && detail && (
            <div className="mission-detail">
              <p className="muted">{detail.objective}</p>
              <table className="mission-table">
                <thead><tr><th>agent</th><th>status</th><th>lineage</th><th>result</th></tr></thead>
                <tbody>
                  {detail.agents.map(a => (
                    <tr key={a.name}>
                      <td>{'\u00a0'.repeat(a.depth * 2)}{a.name}</td>
                      <td><span className={`status-dot ${a.status === 'running' ? 'ok' : a.status === 'done' ? 'off' : 'warn'}`} /> {a.status === 'running' ? a.note : a.status}</td>
                      <td className="muted">{a.parent ?? 'root'}</td>
                      <td>{a.resultPath
                        ? <button className="link-button" onClick={() => onOpen(a.resultPath!)}>{a.resultPath.split('/').pop()}</button>
                        : <span className="muted">—</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <div className="mission-board">
                {detail.board.slice(-20).map(b => (
                  <div key={b.seq} className="mission-msg">
                    <span className="mission-msg-meta">{b.at.slice(11, 19)} {b.from} · {b.topic}</span>
                    <span>{b.body.slice(0, 240)}</span>
                  </div>
                ))}
              </div>
              {detail.status === 'running' && (
                <button className="btn-secondary" onClick={async () => { await fetch(`/api/missions/${encodeURIComponent(detail.id)}/stop`, { method: 'POST' }) }}>Stop mission</button>
              )}
            </div>
          )}
        </section>
      )}

      {(personas.length + skills.length) > 8 && (
        <div className="card agents-filter">
          <input className="field" value={filter} onChange={e => setFilter(e.target.value)}
            placeholder={`Filter ${personas.length + skills.length} definitions by name or description`} />
          {filter && <button className="link-button" onClick={() => setFilter('')}>clear</button>}
        </div>
      )}

      <div className="ov-grid">
        <section className="card ov-list">
          <h3>Personas ({shownPersonas.length}{filter ? ` of ${personas.length}` : ''})</h3>
          <p className="muted view-sub">
            A standing stance for a whole conversation. Choose one from the <b>Persona</b> button above the message box in
            Chat; it stays in force until you change it, and <b>Default</b> puts it back to how this workspace normally answers. Personas are stored
            in the knowledge base{dir ? <> at <code>{dir}</code></> : null}, so everyone working over this corpus has
            the same library. They are ordinary corpus files: browsable under <code>.agents</code> in the Library,
            rendered as Markdown in the viewer, searchable, and labeled <code>agent-persona</code>.
          </p>
          {pageOf(shownPersonas, pPage).map(p => (
            <div className="ov-row" key={p.name} onClick={() => void edit('persona', p.name)} title="Open this persona for editing">
              <span className="ov-row-path"><PersonaIcon icon={p.icon} name={p.name} size={20} />{p.name}{p.shared && <span className="persona-shared">shared</span>}</span>
              <span className="ov-row-meta">
                {p.description || 'no description'}
                {p.shared && (
                  <button className="link-button agents-del"
                    onClick={e => { e.stopPropagation(); onOpen(`.agents/${p.name}.md`) }}>in library</button>
                )}
                <button className="link-button agents-del"
                  onClick={e => { e.stopPropagation(); remove(p.name) }}>remove</button>
              </span>
            </div>
          ))}
          {shownPersonas.length === 0 && <p className="muted">{filter ? 'None match that filter.' : 'None yet. The first one you add appears in the chat Persona control.'}</p>}
          {pager(shownPersonas, pPage, setPPage)}
        </section>

        <section className="card ov-list">
          <h3>Skills ({shownSkills.length}{filter ? ` of ${skills.length}` : ''})</h3>
          <p className="muted view-sub">
            Task instructions the assistant loads itself, mid-conversation, when the work matches the description. There is
            nothing to select in Chat: ask for the work, or name the skill, and the assistant calls it. Write the
            description so it can tell when the skill applies.
          </p>
          {pageOf(shownSkills, sPage).map(s => (
            <div className="ov-row" key={s.name} onClick={() => void edit('skill', s.name)} title="Open this skill for editing">
              <span className="ov-row-path">{s.name}</span>
              <span className="ov-row-meta">{s.description || 'no description'}</span>
            </div>
          ))}
          {shownSkills.length === 0 && <p className="muted">{filter ? 'None match that filter.' : 'None yet.'}</p>}
          {pager(shownSkills, sPage, setSPage)}
        </section>
      </div>

      <section className="card settings-card">
        <h3>{editing ? `Editing ${editing}` : 'Add one'}</h3>
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
        {kind === 'persona' && (
          <>
            <label className="field-label">Icon</label>
            <div className="persona-icon-row">
              <PersonaIcon icon={icon} name={name || '?'} size={34} />
              <input className="field persona-icon-field" value={icon} onChange={e => setIcon(e.target.value)}
                placeholder="an emoji, or upload an image" />
              <label className={`btn-secondary agents-file${name ? '' : ' disabled'}`}
                title={name ? 'Choose an image for this persona' : 'Name the persona first: the image file is named after it'}>
                {name ? 'Upload image' : 'Upload image (name it first)'}
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
                      setNote(d.attached ? 'Icon uploaded and attached.' : 'Icon uploaded. Save the persona to attach it.')
                      if (d.attached) await load()
                    }
                    else setNote(d.error ?? `upload failed (${res.status})`)
                    e.target.value = ''
                  }} />
              </label>
              {icon && <button className="link-button" onClick={() => setIcon('')}>clear</button>}
            </div>
            <p className="muted key-note">
              An image is stored in the knowledge base under <code>.agents/icons</code>, so it travels with the
              persona. Name the persona first, since the file is named after it. With no icon, the initials stand in.
            </p>
          </>
        )}
        <label className="field-label">Markdown</label>
        <textarea className="field agents-body" value={body} onChange={e => onBody(e.target.value)}
          placeholder={'Paste the file. Frontmatter is read into the fields above and rewritten on save.'} />
        <div className="query-actions">
          <button className="btn-primary" onClick={() => void save()} disabled={busy || !name || !body}>
            {busy ? 'Saving…' : editing ? `Save ${editing}` : `Save ${kind}`}
          </button>
          {editing && (
            <>
              <button className="btn-secondary" onClick={() => { setEditing(null); setName(''); setDescription(''); setBody(''); setNote('') }}>Cancel</button>
              {kind === 'persona' && personas.find(p => p.name === editing)?.shared && (
                <button className="btn-secondary" onClick={() => onOpen(`.agents/${editing}.md`)}>Read it rendered</button>
              )}
            </>
          )}
          {note && <span className="muted">{note}</span>}
        </div>
      </section>
    </div>
  )
}
