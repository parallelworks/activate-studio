import { useEffect, useState } from 'react'
import { ACCENTS, SURFACES, applyAccent, applySurface } from '../accents'

interface Effective {
  appName: string
  kbLabel: string
  theme: 'light' | 'dark'
  accent: string
  surface: string
  sweepIntervalSec: number
  suggestedPrompts: string[]
  visionModel: string
  disabledTools: string[]
  customTools: { name: string; description: string; command: string }[]
}

interface CatalogTool { name: string; description: string; builtin: boolean; enabled: boolean; parameters?: unknown; command?: string; implementation?: string; calls?: string }
interface ExtDoc { name: string; description: string; file: string; active?: boolean; command?: string }
interface Extensions { dir: string; tools: ExtDoc[]; skills: ExtDoc[]; agents: ExtDoc[] }

const TOOL_GROUPS: [string, string[]][] = [
  ['Knowledge base', ['search_kb', 'read_kb_file', 'list_kb_dir', 'query_corpus', 'get_labels', 'apply_labels']],
  ['Workflows', ['list_workflows', 'get_workflow', 'run_workflow', 'workflow_runs', 'workflow_run_detail', 'pw_help']],
  ['Clusters', ['list_clusters', 'cluster_command']],
  ['Display and skills', ['show_in_viewer', 'use_skill', 'studio_docs']],
]

export function SettingsView() {
  const [form, setForm] = useState<Effective | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [catalog, setCatalog] = useState<CatalogTool[]>([])
  const [ext, setExt] = useState<Extensions | null>(null)
  const [specOpen, setSpecOpen] = useState<string | null>(null)

  useEffect(() => {
    fetch('/api/settings').then(r => r.json()).then(d => setForm(d.effective)).catch(() => setNote('Could not load settings.'))
    fetch('/api/chat/tools').then(r => r.json())
      .then(d => setCatalog(d.tools ?? []))
      .catch(() => {})
    fetch('/api/extensions').then(r => r.json()).then(setExt).catch(() => {})
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
            <label className="field-label">Default theme</label>
            <select className="field" value={form.theme} onChange={e => setForm({ ...form, theme: e.target.value as 'light' | 'dark' })}>
              <option value="light">light</option>
              <option value="dark">dark</option>
            </select>
          </div>
          <div>
            <label className="field-label">Accent color</label>
            <div className="accent-row">
              {Object.entries(ACCENTS).map(([k, a]) => (
                <button
                  key={k}
                  type="button"
                  className={`accent-swatch ${form.accent === k ? 'active' : ''}`}
                  title={a.label}
                  style={{ background: a.light[0] }}
                  onClick={() => { setForm({ ...form, accent: k }); applyAccent(k) }}
                />
              ))}
              <input
                type="color"
                className={`accent-swatch accent-custom ${form.accent.startsWith('custom:') ? 'active' : ''}`}
                title="Custom accent"
                value={form.accent.startsWith('custom:') ? form.accent.slice(7) : '#06354f'}
                onChange={e => { const v = `custom:${e.target.value}`; setForm({ ...form, accent: v }); applyAccent(v) }}
              />
            </div>
          </div>
          <div>
            <label className="field-label">Surface tone</label>
            <select className="field" value={form.surface}
              onChange={e => { setForm({ ...form, surface: e.target.value }); applySurface(e.target.value) }}>
              {Object.entries(SURFACES).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
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

      <div className="card settings-card">
        <h2 className="settings-section-title">Assistant tools</h2>
        <p className="muted view-sub">
          Everything the chat assistant can call. Built-in tools are part of the server; unchecking one hides it from the model, and clicking a name shows its exact call specification, so you can copy the shape into a custom tool or extension file with your own changes. Custom tools are commands you define here (saved with these settings, not as files); each appears to the assistant as a callable tool and runs on this server with its output returned to the conversation.
        </p>
        <div className="tools-grid">
        {TOOL_GROUPS.map(([group, names]) => {
          const rows = catalog.filter(t => t.builtin && names.includes(t.name))
          if (!rows.length) return null
          return (
            <div key={group}>
              <div className="tool-group">{group}</div>
              {rows.map(t => (
                <div key={t.name}>
                  <div className="tool-row">
                    <input
                      type="checkbox"
                      checked={!form.disabledTools.includes(t.name)}
                      onChange={e => setForm({
                        ...form,
                        disabledTools: e.target.checked
                          ? form.disabledTools.filter(n => n !== t.name)
                          : [...form.disabledTools, t.name],
                      })}
                    />
                    <button className="tool-name" title="Show the tool call specification"
                      onClick={() => setSpecOpen(o => o === t.name ? null : t.name)}>
                      <code>{t.name}</code>
                    </button>
                    <span className="tool-desc">{t.description}</span>
                  </div>
                  {specOpen === t.name && (
                    <>
                      {t.calls && <p className="tool-calls"><span>Calls:</span> {t.calls}</p>}
                      <pre className="tool-spec">{JSON.stringify({ name: t.name, description: t.description, parameters: t.parameters, ...(t.command ? { command: t.command } : {}), ...(t.implementation ? { implementation: t.implementation } : {}) }, null, 2)}</pre>
                    </>
                  )}
                </div>
              ))}
            </div>
          )
        })}
        </div>
        <div className="tool-group">Custom tools</div>
        <div className="custom-tools">
          {form.customTools.map((t, i) => (
            <div className="custom-tool-row" key={i}>
              <input className="field" placeholder="tool_name" value={t.name}
                onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, name: e.target.value } : x) })} />
              <input className="field" placeholder="What it does, for the model" value={t.description}
                onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, description: e.target.value } : x) })} />
              <input className="field mono" placeholder="command to run" value={t.command}
                onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, command: e.target.value } : x) })} />
              <button className="btn-danger-outline" onClick={() => setForm({ ...form, customTools: form.customTools.filter((_, j) => j !== i) })}>Remove</button>
            </div>
          ))}
          <button className="btn-secondary" onClick={() => setForm({ ...form, customTools: [...form.customTools, { name: '', description: '', command: '' }] })}>Add custom tool</button>
        </div>
        <div className="query-actions">
          <button className="btn-primary" onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
          {note && <span className="muted">{note}</span>}
        </div>
      </div>

      <div className="card settings-card">
        <h2 className="settings-section-title">Extensions</h2>
        <p className="muted view-sub">
          File-based extensions load from <code>{ext?.dir ?? 'the extensions directory beside the index'}</code> and take effect without a restart. <code>tools/*.json</code> files (name, description, command) become chat tools like the custom tools above. <code>skills/*.md</code> files are instruction sets the assistant loads on demand through the use_skill tool, by name or when a task matches their description. <code>agents/default.md</code> is appended to the system prompt as this deployment's standing instructions.
        </p>
        <div className="ext-cols">
          <div>
            <div className="tool-group">Tool files ({ext?.tools.length ?? 0})</div>
            {ext?.tools.length
              ? ext.tools.map(t => <div className="ext-row" key={t.file}><code>{t.name}</code><span className="tool-desc">{t.description}{t.command ? <> — <code>{t.command}</code></> : null}</span></div>)
              : <p className="muted ext-empty">None yet. Drop a JSON file in tools/.</p>}
          </div>
          <div>
            <div className="tool-group">Skills ({ext?.skills.length ?? 0})</div>
            {ext?.skills.length
              ? ext.skills.map(t => <div className="ext-row" key={t.file}><code>{t.name}</code><span className="tool-desc">{t.description}</span></div>)
              : <p className="muted ext-empty">None yet. Drop a markdown file in skills/.</p>}
          </div>
          <div>
            <div className="tool-group">Agents ({ext?.agents.length ?? 0})</div>
            {ext?.agents.length
              ? ext.agents.map(t => <div className="ext-row" key={t.file}><code>{t.file}</code><span className="tool-desc">{t.active ? 'active (appended to the system prompt)' : 'inactive; rename to default.md to activate'}</span></div>)
              : <p className="muted ext-empty">None yet. Drop default.md in agents/.</p>}
          </div>
        </div>
      </div>
    </div>
  )
}
