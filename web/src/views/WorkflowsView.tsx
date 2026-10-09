import { Suspense, useEffect, useMemo, useRef, useState } from 'react'
import type { FormikProps, FormikValues } from 'formik'
import { DynamicForm, initializeValues } from '@parallelworks/ui/form'
import { useWorkflowFields, forgetPlatformData } from '../components/WorkflowFields'

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
  const loadTiles = () => {
    setError('')
    fetch('/api/workflows/collection').then(r => json<{ configured: boolean; workflows: Tile[] }>(r))
      .then(d => { setConfigured(d.configured); setTiles(d.workflows) })
      .catch(e => setError(String((e as Error).message)))
  }
  const loadRuns = () => { fetch('/api/workflows/runs').then(r => json<{ runs: RunRow[] }>(r)).then(d => setRuns(d.runs)).catch(() => {}) }
  useEffect(() => { loadTiles(); loadRuns(); const id = window.setInterval(loadRuns, 15_000); return () => window.clearInterval(id) }, [])

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
      {runs.length > 0 && (
        <>
          <div className="tool-group">Recent runs</div>
          <div className="rag-calls-wrap">
            <table className="rag-calls-table">
              <thead><tr><th>Run</th><th>Workflow</th><th>Started</th><th>State</th></tr></thead>
              <tbody>
                {runs.map(r => (
                  <tr key={r.slug}>
                    <td><code>{r.slug}</code></td>
                    <td>{tiles?.find(t => t.name === r.workflow)?.displayName ?? r.workflow}</td>
                    <td>{new Date(r.launchedAt).toLocaleString()}</td>
                    <td className={`wf-state ${r.state}`}>{r.state}</td>
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
  const formik = useRef<FormikProps<FormikValues> | null>(null)
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
    const values = formik.current?.values ?? {}
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
    <div className="overview-view workflows-view wf-runner">
      <header className="wf-runner-head">
        <button className="btn-link wf-back" onClick={onBack}>&larr; All workflows</button>
        <h1 className="wf-title">{doc?.displayName ?? title}</h1>
        {doc?.description && <p className="wf-desc">{doc.description}</p>}
      </header>
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
                  <DynamicForm key={`${name}:${preset}`} formJSONs={doc.form} initialValues={initial} formikRef={formik}
                    fields={fields} workflowForm labelPosition="left" contextKey={preset || 'defaults'} />
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
  )
}
