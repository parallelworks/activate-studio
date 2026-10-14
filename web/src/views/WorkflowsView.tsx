import { Suspense, useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { DynamicForm, initializeValues } from '@parallelworks/ui/form'
import { Streamdown } from 'streamdown'
import { useWorkflowFields, forgetPlatformData } from '../components/WorkflowFields'
import { openSessionWindow, sessionOfRun, type SessionInfo } from '../components/SessionView'

/**
 * The Workflows tab: this Studio's curated set of ACTIVATE workflows, as
 * tiles, each run from its own form. Runs act as the viewer, on their own
 * copy of the workflow; a curated workflow the viewer lacks can be added
 * to their account from here.
 */

interface Tile {
  name: string; displayName: string; description: string; tags: string[]
  icon: string | null; configurations: string[]; installed: boolean; available: boolean
  kind?: 'account' | 'marketplace' | 'github'
}
interface FormDoc {
  name: string; displayName: string; description: string
  form: Record<string, unknown> | null
  configurations: { name: string; inputs: Record<string, unknown> }[]
  permissions?: string[]
  source?: WorkflowSource | null
}
type WorkflowSource =
  | { kind: 'local' }
  | { kind: 'remote'; repo: string; branch: string | null; path: string | null }
  | { kind: 'marketplace'; slug: string }

/** Where a workflow's definition comes from, in one line. */
function SourceLine({ source }: { source: WorkflowSource }) {
  if (source.kind === 'local') return <p className="wf-source">Local: stored in your ACTIVATE account</p>
  if (source.kind === 'marketplace') return <p className="wf-source">Marketplace: <code>{source.slug}</code></p>
  return (
    <p className="wf-source">
      Remote: <code>{source.repo}</code>{source.branch && <> on <code>{source.branch}</code></>}{source.path && <>, <code>{source.path}</code></>}
    </p>
  )
}
interface RunRow { slug: string; workflow: string; launchedAt: string; state: string; endedAt: string | null }

async function json<T>(res: Response): Promise<T> {
  const d = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((d as { error?: string }).error ?? `${res.status}`)
  return d as T
}

/** A tile's icon, or its first letter when there is none or it fails to
 *  load (a repository without a thumbnail, say). */
function TileIcon({ src, letter }: { src: string | null; letter: string }) {
  const [failed, setFailed] = useState(false)
  if (!src || failed) return <span className="wf-icon-blank">{letter}</span>
  return <img src={src} alt="" loading="lazy" onError={() => setFailed(true)} />
}

export function WorkflowsView() {
  const [tiles, setTiles] = useState<Tile[] | null>(null)
  const [configured, setConfigured] = useState(true)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [runs, setRuns] = useState<RunRow[]>([])
  // Running sessions: the apps workflows open, and endpoints. Each opens
  // in a window of its own.
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const loadTiles = () => {
    setError('')
    fetch('/api/workflows/collection').then(r => json<{ configured: boolean; workflows: Tile[] }>(r))
      .then(d => { setConfigured(d.configured); setTiles(d.workflows) })
      .catch(e => setError(String((e as Error).message)))
  }
  const loadRuns = () => {
    fetch('/api/workflows/runs').then(r => json<{ runs: RunRow[] }>(r)).then(d => setRuns(d.runs)).catch(() => {})
    fetch('/api/sessions').then(r => json<{ sessions: SessionInfo[] }>(r)).then(d => setSessions(d.sessions.filter(x => x.status === 'running'))).catch(() => {})
  }
  useEffect(() => { loadTiles(); loadRuns(); const id = window.setInterval(loadRuns, 15_000); return () => window.clearInterval(id) }, [])
  // The sidebar's Workflows item always leads back to this page.
  useEffect(() => {
    const home = () => setOpen(null)
    window.addEventListener('ade:workflows-home', home)
    return () => window.removeEventListener('ade:workflows-home', home)
  }, [])

  if (open) {
    const t = tiles?.find(x => x.name === open)
    return <WorkflowRunner name={open} kind={t?.kind ?? 'account'} title={t?.displayName ?? open} onBack={() => { setOpen(null); loadRuns() }} onLaunched={loadRuns} />
  }
  return (
    <div className="overview-view workflows-view">
      {error && <p className="banner-error">{error}</p>}
      {!configured && <p className="muted">No workflows are offered yet. An administrator picks them under Settings, Workflows.</p>}
      {tiles && tiles.length > 0 && (
        <div className="wf-tiles">
          {tiles.map(t => (
            <button key={t.name} className={`wf-tile${t.available ? '' : ' unavailable'}`} disabled={!t.available}
              title={t.description || t.displayName} onClick={() => setOpen(t.name)}>
              <span className="wf-icon"><TileIcon src={t.icon} letter={t.displayName.slice(0, 1)} /></span>
              <span className="wf-name">{t.displayName}</span>
              {t.kind && t.kind !== 'account' && <span className="wf-kind">{t.kind === 'marketplace' ? 'Marketplace' : 'GitHub'}</span>}
              {!t.installed && t.available && <span className="wf-note">Not in your account yet</span>}
              {!t.available && <span className="wf-note">Not found on the platform</span>}
            </button>
          ))}
        </div>
      )}
      {sessions.length > 0 && (
        <>
          <div className="tool-group">Sessions</div>
          <div className="session-list">
            {sessions.map(x => (
              <div key={`${x.user}/${x.name}`} className="session-row">
                <span className="status-dot ok" />
                <button type="button" className="link-button session-row-name" onClick={() => openSessionWindow(x)} title="Open in a window of its own">{x.name}</button>
                <span className="muted session-meta">
                  {[x.run ? `${x.run.workflow} · ${x.run.slug}` : x.type, x.resource ? `on ${x.resource}` : '', x.user].filter(Boolean).join(' · ')}
                </span>
                {x.url && <a className="btn-ghost" href={x.url} target="_blank" rel="noopener noreferrer">New tab</a>}
              </div>
            ))}
          </div>
        </>
      )}
      {runs.length > 0 && (
        <>
          <div className="tool-group">Recent runs</div>
          <div className="rag-calls-wrap">
            <table className="rag-calls-table">
              <thead><tr><th>Run</th><th>Workflow</th><th>Started</th><th>State</th><th>Session</th></tr></thead>
              <tbody>
                {runs.map(r => (
                  <tr key={r.slug}>
                    <td><code>{r.slug}</code></td>
                    <td>{tiles?.find(t => t.name === r.workflow)?.displayName ?? r.workflow}</td>
                    <td>{new Date(r.launchedAt).toLocaleString()}</td>
                    <td className={`wf-state ${r.state}`}>{r.state}</td>
                    <td>{sessions.filter(x => sessionOfRun(x, r.slug)).map(x => (
                      <button key={x.name} type="button" className="link-button" onClick={() => openSessionWindow(x)} title="Open in a window of its own">{x.name}</button>
                    ))}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  )
}

function WorkflowRunner({ name, kind, title, onBack, onLaunched }: { name: string; kind: string; title: string; onBack: () => void; onLaunched: () => void }) {
  const [doc, setDoc] = useState<FormDoc | null>(null)
  const [error, setError] = useState('')
  const [preset, setPreset] = useState('')
  const [busy, setBusy] = useState<'' | 'validate' | 'run' | 'install'>('')
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [needsInstall, setNeedsInstall] = useState(false)
  const [trust, setTrust] = useState(false)
  // The workflow's README, beside the form: it says how the workflow works
  // and what its inputs change. Hidden or shown by the viewer's choice.
  const [readme, setReadme] = useState<{ markdown: string | null; web: string | null } | null>(null)
  const [showReadme, setShowReadme] = useState(() => { try { return localStorage.getItem('ade-wf-readme') !== 'hidden' } catch { return true } })
  const toggleReadme = () => setShowReadme(v => { try { localStorage.setItem('ade-wf-readme', v ? 'hidden' : 'shown') } catch { /* per session then */ } return !v })
  useEffect(() => {
    setReadme(null)
    fetch(`/api/workflows/item/readme?w=${encodeURIComponent(name)}`).then(r => json<{ markdown: string | null; web: string | null }>(r))
      .then(setReadme).catch(() => setReadme({ markdown: null, web: null }))
  }, [name])
  const hasReadme = !!readme?.markdown
  const form = useForm<Record<string, unknown>>()
  const fields = useWorkflowFields()
  const load = () => {
    setError(''); setNeedsInstall(false); setTrust(false)
    fetch(`/api/workflows/item/form?w=${encodeURIComponent(name)}`).then(r => json<FormDoc>(r)).then(setDoc)
      .catch(e => { const m = String((e as Error).message); if (kind === 'account' && /not found|404/i.test(m)) setNeedsInstall(true); else setError(m) })
  }
  useEffect(() => { forgetPlatformData(); load() }, [name])

  // The host builds the starting values: the workflow's defaults, with a
  // saved configuration's inputs laid over them.
  const initial = useMemo(() => {
    if (!doc?.form) return {}
    const saved = doc.configurations.find(c => c.name === preset)?.inputs ?? {}
    try { return initializeValues(doc.form, saved) ?? saved } catch { return saved }
  }, [doc, preset])
  const submit = async (dryRun: boolean) => {
    const { editorKeyPostfix: _, ...values } = form.getValues()
    setBusy(dryRun ? 'validate' : 'run'); setResult(null)
    try {
      const r = await json<{ ok: boolean; message: string; slug?: string | null }>(await fetch(`/api/workflows/item/run?w=${encodeURIComponent(name)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ inputs: values, dryRun, trust }),
      }))
      setResult({ ok: r.ok, text: r.ok && dryRun ? 'Validation passed. The platform accepts these inputs.' : r.message })
      if (r.ok && !dryRun) onLaunched()
    } catch (e) { setResult({ ok: false, text: String((e as Error).message) }) } finally { setBusy('') }
  }
  const install = async () => {
    setBusy('install')
    try { await json(await fetch(`/api/workflows/item/install?w=${encodeURIComponent(name)}`, { method: 'POST' })); load() }
    catch (e) { setError(String((e as Error).message)) } finally { setBusy('') }
  }

  return (
    <div className={`overview-view workflows-view wf-runner${hasReadme && showReadme ? ' with-readme' : ''}`}>
      <header className="wf-runner-head">
        <button className="btn-link wf-back" onClick={onBack}>&larr; All workflows</button>
        <div className="wf-title-row">
          <h1 className="wf-title">{doc?.displayName ?? title}</h1>
          {hasReadme && <button type="button" className="btn-ghost wf-readme-toggle" onClick={toggleReadme}>{showReadme ? 'Hide README' : 'Show README'}</button>}
        </div>
        {doc?.description && <p className="wf-desc">{doc.description}</p>}
        {doc?.source && <SourceLine source={doc.source} />}
      </header>
      <div className="wf-runner-body">
      <div className="wf-runner-main">
      {error && <p className="banner-error">{error}</p>}
      {needsInstall && (
        <div className="card wf-install">
          <p>This workflow is not in your ACTIVATE account yet. Adding it copies the Studio's version into your account, so it runs under your allocation and permissions.</p>
          <button className="btn-primary" disabled={!!busy} onClick={() => void install()}>{busy === 'install' ? 'Adding…' : 'Add to my workflows'}</button>
        </div>
      )}
      {doc && (
        <>
          {doc.configurations.length > 0 && (
            <div className="wf-preset">
              <label className="field-label">Start from a saved configuration</label>
              <select className="field" value={preset} onChange={e => setPreset(e.target.value)}>
                <option value="">None, use the workflow's defaults</option>
                {doc.configurations.map(c => <option key={c.name} value={c.name}>{c.name}</option>)}
              </select>
            </div>
          )}
          <div className="wf-form">
            {doc.form && Object.keys(doc.form).length
              ? <Suspense fallback={<p className="muted">Loading form…</p>}>
                  <DynamicForm key={`${name}:${preset}`} form={form} formJSONs={doc.form} initialValues={initial} reinitialize
                    fields={fields} workflowForm labelPosition="left" contextKey={preset || 'defaults'} allowLayoutCSS />
                </Suspense>
              : <p className="muted">This workflow takes no inputs.</p>}
          </div>
          {!!doc.permissions?.length && (
            <div className="card wf-trust">
              <p>
                This workflow's repository asks for access to your account variables
                ({doc.permissions.includes('*') ? 'all of them' : doc.permissions.join(', ')}). The platform runs it only once you
                approve that access, which stays granted to the repository until you revoke it
                with <code>pw workflows permissions revoke</code>.
              </p>
              <label className="wf-trust-check">
                <input type="checkbox" checked={trust} onChange={e => setTrust(e.target.checked)} />
                Allow this access when I validate or run it
              </label>
            </div>
          )}
          <div className="wf-actions">
            <button className="btn-primary" disabled={!!busy || (!!doc.permissions?.length && !trust)} onClick={() => void submit(false)}>{busy === 'run' ? 'Starting…' : 'Run'}</button>
            <button className="btn-secondary" disabled={!!busy || (!!doc.permissions?.length && !trust)} onClick={() => void submit(true)}>{busy === 'validate' ? 'Validating…' : 'Validate'}</button>
            {result && <span className={`wf-result ${result.ok ? 'wf-ok' : 'wf-fail'}`}>{result.text}</span>}
          </div>
        </>
      )}
      </div>
      {hasReadme && showReadme && (
        <aside className="wf-readme" aria-label="README">
          <div className="wf-readme-head">
            <span>README</span>
            {readme?.web && <a href={readme.web} target="_blank" rel="noopener noreferrer">View in repository</a>}
          </div>
          <div className="md-body"><Streamdown>{readme!.markdown!}</Streamdown></div>
        </aside>
      )}
      </div>
    </div>
  )
}
