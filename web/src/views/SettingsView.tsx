import { useEffect, useState } from 'react'

interface Effective {
  appName: string
  kbLabel: string
  theme: 'light' | 'dark'
  sweepIntervalSec: number
  suggestedPrompts: string[]
  visionModel: string
}

export function SettingsView() {
  const [form, setForm] = useState<Effective | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [models, setModels] = useState<string[]>([])

  useEffect(() => {
    fetch('/api/settings').then(r => r.json()).then(d => setForm(d.effective)).catch(() => setNote('Could not load settings.'))
    fetch('/api/chat/models').then(r => r.json())
      .then(d => setModels((d.models ?? []).map((m: { id: string }) => m.id)))
      .catch(() => {})
  }, [])

  const save = async () => {
    if (!form) return
    setBusy(true)
    setNote('')
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error ?? `${res.status}`)
      setNote('Saved. Reloading…')
      setTimeout(() => location.reload(), 600)
    } catch (e) {
      setNote(String((e as Error).message ?? e))
      setBusy(false)
    }
  }

  if (!form) return <div className="settings-view"><div className="card settings-card"><p className="muted pad">{note || 'Loading…'}</p></div></div>

  return (
    <div className="settings-view">
      <div className="card settings-card">
        <h1 className="view-title">Settings</h1>
        <p className="muted view-sub">
          Saved settings apply immediately and override the deployment's environment defaults; they live beside the index, not in the repository. Brand icon and model credentials stay in the environment (see docs/CUSTOMIZATION.md).
        </p>

        <div className="settings-grid">
          <div>
            <label className="field-label">Product name</label>
            <input className="field" value={form.appName} onChange={e => setForm({ ...form, appName: e.target.value })} />
          </div>
          <div>
            <label className="field-label">Knowledge base label</label>
            <input className="field" value={form.kbLabel} onChange={e => setForm({ ...form, kbLabel: e.target.value })} />
          </div>
          <div>
            <label className="field-label">Default theme</label>
            <select className="field" value={form.theme} onChange={e => setForm({ ...form, theme: e.target.value as 'light' | 'dark' })}>
              <option value="light">light</option>
              <option value="dark">dark</option>
            </select>
          </div>
          <div>
            <label className="field-label">Background sync interval (seconds, 0 pauses)</label>
            <input className="field" type="number" min={0} max={86400} value={form.sweepIntervalSec}
              onChange={e => setForm({ ...form, sweepIntervalSec: Number(e.target.value) })} />
          </div>
          <div>
            <label className="field-label">Vision model for image captioning (empty disables)</label>
            <input className="field" value={form.visionModel} list="vision-models"
              placeholder={models.length ? 'pick from the connected endpoint or type an id' : 'e.g. me:provider/model'}
              onChange={e => setForm({ ...form, visionModel: e.target.value })} />
            <datalist id="vision-models">
              {models.map(m => <option key={m} value={m} />)}
            </datalist>
          </div>
        </div>

        <label className="field-label">Chat starter prompts (one per line)</label>
        <textarea
          className="field prompts-box"
          rows={5}
          value={form.suggestedPrompts.join('\n')}
          onChange={e => setForm({ ...form, suggestedPrompts: e.target.value.split('\n') })}
        />

        <div className="query-actions">
          <button className="btn-primary" onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
          {note && <span className="muted">{note}</span>}
        </div>
      </div>
    </div>
  )
}
