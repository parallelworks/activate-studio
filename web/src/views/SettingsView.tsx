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
  ragDefaultModel: string
  ragTopK: number
  ragAllowDeploymentKey: boolean
  ragEndpointAutoStart: boolean
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

type SectionId = 'general' | 'access' | 'tools' | 'rag' | 'ext'

/** Dropdown over the live model catalog; a saved value not in the catalog
 *  stays selectable so settings never silently break. */
function ModelSelect({ value, models, allowEmpty, emptyLabel, onChange }: {
  value: string
  models: string[]
  allowEmpty?: boolean
  emptyLabel?: string
  onChange: (v: string) => void
}) {
  const opts = [...new Set([...(value && !models.includes(value) ? [value] : []), ...models])]
  return (
    <select className="field" value={value} onChange={e => onChange(e.target.value)}>
      {allowEmpty && <option value="">{emptyLabel ?? '(none)'}</option>}
      {opts.map(m => <option key={m} value={m}>{m}</option>)}
    </select>
  )
}

export function SettingsView() {
  const [form, setForm] = useState<Effective | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [catalog, setCatalog] = useState<CatalogTool[]>([])
  const [ext, setExt] = useState<Extensions | null>(null)
  const [specOpen, setSpecOpen] = useState<string | null>(null)
  const [me, setMe] = useState<{ authEnabled?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'; last4?: string; addedAt?: string; sessionExpiresAt?: string } | null>(null)
  const [keyInput, setKeyInput] = useState('')
  const [persistKey, setPersistKey] = useState(true)
  const [keyBusy, setKeyBusy] = useState(false)
  const [keyNote, setKeyNote] = useState('')
  const [section, setSection] = useState<SectionId>('general')
  const [ragEp, setRagEp] = useState<{ state: string; name: string | null; url: string | null; detail: string | null } | null>(null)

  const refreshRagEp = () => fetch('/api/rag/endpoint').then(r => r.json()).then(setRagEp).catch(() => {})
  const ragEpAction = async (verb: 'start' | 'stop') => {
    await fetch(`/api/rag/endpoint/${verb}`, { method: 'POST' }).then(r => r.json()).then(setRagEp).catch(() => {})
    setTimeout(refreshRagEp, 2500)
    setTimeout(refreshRagEp, 8000)
  }

  useEffect(() => {
    fetch('/api/settings').then(r => r.json()).then(d => setForm(d.effective)).catch(() => setNote('Could not load settings.'))
    fetch('/api/chat/tools').then(r => r.json())
      .then(d => setCatalog(d.tools ?? []))
      .catch(() => {})
    fetch('/api/extensions').then(r => r.json()).then(setExt).catch(() => {})
    fetch('/api/chat/models').then(r => r.json())
      .then(d => setModels((d.models ?? []).map((m: { id: string }) => m.id)))
      .catch(() => {})
    fetch('/api/me/model-key').then(r => r.json()).then(setMe).catch(() => {})
    refreshRagEp()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const keyAction = async (method: string, body?: unknown) => {
    setKeyBusy(true)
    setKeyNote('')
    try {
      const res = await fetch('/api/me/model-key', {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? undefined : JSON.stringify(body),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error ?? `${res.status}`)
      setMe(data)
      return true
    } catch (e) {
      setKeyNote(String((e as Error).message ?? e))
      return false
    } finally {
      setKeyBusy(false)
    }
  }

  const testKey = async (candidate?: string) => {
    setKeyBusy(true)
    setKeyNote('')
    try {
      const res = await fetch('/api/me/model-key/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(candidate ? { key: candidate } : {}),
      })
      const h = await res.json()
      if (!res.ok) throw new Error(h.error ?? `${res.status}`)
      setKeyNote(h.status === 'ok' ? `Key works: ${h.models} models available.` : `Key check failed (${h.status}): ${h.message ?? ''}`)
    } catch (e) {
      setKeyNote(String((e as Error).message ?? e))
    } finally {
      setKeyBusy(false)
    }
  }

  const save = async (reload = true) => {
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
      if (reload) {
        setNote('Saved. Reloading…')
        setTimeout(() => location.reload(), 600)
      } else {
        setNote('Saved.')
        setBusy(false)
      }
    } catch (e) {
      setNote(String((e as Error).message ?? e))
      setBusy(false)
    }
  }

  if (!form) return <div className="settings-view"><div className="card settings-card"><p className="muted pad">{note || 'Loading…'}</p></div></div>

  const pickableModels = models.filter(m => !/studio-(agent|rag)/.test(m))

  const sections: { id: SectionId; label: string }[] = [
    { id: 'general', label: 'General' },
    ...(me?.authEnabled ? [{ id: 'access' as SectionId, label: 'Your model access' }] : []),
    { id: 'tools', label: 'Assistant tools' },
    { id: 'rag', label: 'RAG endpoint' },
    { id: 'ext', label: 'Extensions' },
  ]

  const saveRow = (reload = true) => (
    <div className="query-actions">
      <button className="btn-primary" onClick={() => void save(reload)} disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
      {note && <span className="muted">{note}</span>}
    </div>
  )

  return (
    <div className="settings-view">
      <div className="help-docs card">
        <nav className="help-nav">
          <div className="help-nav-head"><span>Settings</span></div>
          {sections.map(s => (
            <button key={s.id} className={section === s.id ? 'active' : ''} onClick={() => setSection(s.id)}>
              <span>{s.label}</span>
            </button>
          ))}
        </nav>
        <article className="help-content settings-content">

          {section === 'general' && (
            <>
              <h1>General</h1>
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
                  <label className="field-label">Vision model for image captioning</label>
                  <ModelSelect
                    value={form.visionModel}
                    models={pickableModels}
                    allowEmpty
                    emptyLabel="disabled (no image captioning)"
                    onChange={v => setForm({ ...form, visionModel: v })}
                  />
                </div>
              </div>
              <label className="field-label">Chat starter prompts (one per line)</label>
              <textarea
                className="field prompts-box"
                rows={10}
                value={form.suggestedPrompts.join('\n')}
                onChange={e => setForm({ ...form, suggestedPrompts: e.target.value.split('\n') })}
              />
              {saveRow()}
            </>
          )}

          {section === 'access' && (
            <>
              <h1>Your model access</h1>
              {!me?.verified ? (
                <p className="muted view-sub">
                  Personal model keys attach to your platform-verified identity, which this connection does not carry. Open the app through the platform session to add one; until then, requests use the deployment's credential.
                </p>
              ) : (
                <>
                  <p className="muted view-sub">
                    {me.mode === 'none' && 'You are using the deployment’s model credential. Add your own key to call the gateway (and platform tools) as yourself.'}
                    {me.mode === 'stored' && `Using your key ending ${me.last4}, added ${me.addedAt?.slice(0, 10)}, stored encrypted on this server.`}
                    {me.mode === 'session' && `Using your key ending ${me.last4}, held in memory for this session only (expires ${me.sessionExpiresAt ? new Date(me.sessionExpiresAt).toLocaleTimeString() : 'soon'}).`}
                  </p>
                  <div className="key-row">
                    <input
                      className="field grow"
                      type="password"
                      value={keyInput}
                      placeholder="Paste your PW API key"
                      autoComplete="off"
                      onChange={e => setKeyInput(e.target.value)}
                    />
                    <button className="btn-primary" disabled={keyBusy || !keyInput.trim()}
                      onClick={async () => { if (await keyAction('PUT', { key: keyInput.trim(), persist: persistKey })) { setKeyInput(''); void testKey() } }}>
                      Save and test
                    </button>
                    <button className="btn-secondary" disabled={keyBusy || (!keyInput.trim() && me.mode === 'none')}
                      onClick={() => void testKey(keyInput.trim() || undefined)}>Test</button>
                    {me.mode !== 'none' && (
                      <button className="btn-danger-outline" disabled={keyBusy} onClick={() => void keyAction('DELETE')}>Remove</button>
                    )}
                  </div>
                  <label className="key-persist">
                    <input type="checkbox" checked={persistKey} onChange={e => setPersistKey(e.target.checked)} />
                    Remember on this server (encrypted at rest); unchecked keeps it in memory for about 12 hours only
                  </label>
                  <p className="muted key-trust">
                    A key added here is available to this server and its operator; encryption at rest protects the stored file, not the running process. Use a key you can revoke. If your key needs periodic unlocking (genai.mil keys unlock every 8 hours), Test tells you its current state.
                  </p>
                  {keyNote && <p className="muted key-note">{keyNote}</p>}
                </>
              )}
            </>
          )}

          {section === 'tools' && (
            <>
              <h1>Assistant tools</h1>
              <p className="muted view-sub">
                Everything the chat assistant can call. Built-in tools are part of the server; unchecking one hides it from the model, and clicking a name shows what it actually calls plus its exact specification, so you can copy the shape into a custom tool or extension file with your own changes. Custom tools are commands you define here (saved with these settings, not as files); each appears to the assistant as a callable tool and runs on this server with its output returned to the conversation.
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
              {saveRow()}
            </>
          )}

          {section === 'rag' && (
            <>
              <h1>RAG endpoint</h1>
              <p className="muted view-sub">
                This deployment serves an OpenAI-compatible endpoint at <code>{location.origin}/v1</code>, so any OpenAI-speaking client (pw code, SDKs, other agents) can use the knowledge base as a grounded model. Callers authenticate with their own gateway API key as the bearer token; the endpoint holds no credentials of its own.
              </p>
              <ul className="rag-modes">
                <li><code>studio-agent</code> runs the full assistant pipeline (system prompt, search, read, query, labels) on the underlying model and returns the grounded answer.</li>
                <li><code>studio-rag</code> injects retrieved context blocks with citation instructions and makes one model call; any other model id behaves the same with that model.</li>
                <li>Pick the underlying model per request as <code>studio-agent/&lt;model-id&gt;</code>, or set the default below. Headers <code>X-RAG-Top-K</code>, <code>X-RAG-Tags</code>, and <code>X-RAG-Off: 1</code> tune retrieval per request.</li>
              </ul>
              <div className="settings-grid">
                <div>
                  <label className="field-label">Default underlying model (for bare studio-agent / studio-rag)</label>
                  <ModelSelect
                    value={form.ragDefaultModel}
                    models={pickableModels}
                    allowEmpty
                    emptyLabel="none (callers must pass studio-agent/<model-id>)"
                    onChange={v => setForm({ ...form, ragDefaultModel: v })}
                  />
                </div>
                <div>
                  <label className="field-label">Retrieved context blocks (top K)</label>
                  <input className="field" type="number" min={1} max={20} value={form.ragTopK}
                    onChange={e => setForm({ ...form, ragTopK: Number(e.target.value) })} />
                </div>
              </div>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragAllowDeploymentKey}
                  onChange={e => setForm({ ...form, ragAllowDeploymentKey: e.target.checked })} />
                Allow callers without a bearer key to use the deployment credential (off means every caller must present their own key)
              </label>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragEndpointAutoStart}
                  onChange={e => setForm({ ...form, ragEndpointAutoStart: e.target.checked })} />
                Register on the platform automatically at startup
              </label>
              {saveRow(false)}
              <div className="tool-group">Platform registration</div>
              <p className="muted view-sub">
                Publishes this /v1 surface into the platform chat and AI provider catalog (via <code>pw endpoints run --openai</code> around a local forwarder), so users pick the corpus-grounded models like any other. The registered provider authenticates with the deployment credential. Requires an authenticated pw CLI on the host.
              </p>
              <div className="key-row">
                <span className="rag-ep-status">
                  <span className={`status-dot ${ragEp?.state === 'running' ? 'ok' : ragEp?.state === 'error' ? 'warn' : 'off'}`} />
                  {ragEp?.state ?? 'unknown'}{ragEp?.name ? ` (${ragEp.name})` : ''}
                  {ragEp?.url ? <> at <code>{ragEp.url}</code></> : null}
                </span>
                {ragEp?.state === 'running' || ragEp?.state === 'starting'
                  ? <button className="btn-secondary" onClick={() => void ragEpAction('stop')}>Stop</button>
                  : <button className="btn-primary" onClick={() => void ragEpAction('start')}>Register now</button>}
                <button className="btn-secondary" onClick={refreshRagEp}>Refresh</button>
              </div>
              {ragEp?.detail && <p className="muted key-note">{ragEp.detail}</p>}
            </>
          )}

          {section === 'ext' && (
            <>
              <h1>Extensions</h1>
              <p className="muted view-sub">
                File-based extensions load from <code>{ext?.dir ?? 'the extensions directory beside the index'}</code> and take effect without a restart. <code>tools/*.json</code> files (name, description, command) become chat tools like the custom tools above. <code>skills/*.md</code> files are instruction sets the assistant loads on demand through the use_skill tool, by name or when a task matches their description. <code>agents/default.md</code> is appended to the system prompt as this deployment's standing instructions.
              </p>
              <div className="ext-cols">
                <div>
                  <div className="tool-group">Tool files ({ext?.tools.length ?? 0})</div>
                  {ext?.tools.length
                    ? ext.tools.map(t => <div className="ext-row" key={t.file}><code>{t.name}</code><span className="tool-desc">{t.description}{t.command ? <> (<code>{t.command}</code>)</> : null}</span></div>)
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
            </>
          )}

        </article>
      </div>
    </div>
  )
}
