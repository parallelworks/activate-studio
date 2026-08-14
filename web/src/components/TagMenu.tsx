import { useEffect, useState } from 'react'
import { api } from '../api'

export function TagMenu({ paths, onClose, onApplied }: {
  paths: string[]
  onClose: () => void
  onApplied: () => void
}) {
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  const [current, setCurrent] = useState<string[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
    Promise.all(paths.slice(0, 30).map(p => api.tagsFor(p).catch(() => ({ own: [] as string[], inherited: [] as string[] }))))
      .then(all => setCurrent([...new Set(all.flatMap(t => t.own))]))
  }, [paths])

  const apply = async (add: string[], remove: string[]) => {
    setBusy(true)
    setNote('')
    try {
      await api.applyTags(paths, add, remove)
      setCurrent(c => [...new Set([...c.filter(t => !remove.includes(t)), ...add])])
      onApplied()
      setNote('Applied and re-indexed.')
    } catch (e) {
      setNote(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const addFromInput = () => {
    const t = input.trim()
    if (t) { void apply([t], []); setInput('') }
  }

  return (
    <div className="card-pop tag-pop">
      <div className="tag-pop-head">
        <span className="field-label">Labels for {paths.length === 1 ? paths[0].split('/').pop() : `${paths.length} items`}</span>
        <button className="btn-secondary" onClick={onClose}>Close</button>
      </div>
      {current.length > 0 && (
        <div className="tag-row">
          {current.map(t => (
            <span key={t} className="tag-pill on">
              {t}
              <button title="Remove label" onClick={() => void apply([], [t])} disabled={busy}>×</button>
            </span>
          ))}
        </div>
      )}
      {vocab.filter(v => !current.includes(v.tag)).length > 0 && (
        <>
          <label className="field-label">Existing labels</label>
          <div className="tag-row">
            {vocab.filter(v => !current.includes(v.tag)).slice(0, 14).map(v => (
              <button key={v.tag} className="tag-pill" onClick={() => void apply([v.tag], [])} disabled={busy}>
                {v.tag} <span className="tag-count">{v.count}</span>
              </button>
            ))}
          </div>
        </>
      )}
      <label className="field-label">New label</label>
      <div className="url-row">
        <input className="field" value={input} onChange={e => setInput(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && addFromInput()} placeholder="e.g. validated, research, experiment" />
        <button className="btn-primary" onClick={addFromInput} disabled={busy || !input.trim()}>Add</button>
      </div>
      {note && <p className="muted tag-note">{note}</p>}
    </div>
  )
}
