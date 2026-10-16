import { useEffect, useMemo, useState } from 'react'
import { Button, Callout, Checkbox, IconButton, Input, Loader, SearchInput, Table, toast } from '@parallelworks/ui'
import { ArrowDownIcon, ArrowUpIcon } from '@parallelworks/ui/icons'

/**
 * Settings > Workflows: which platform workflows this Studio offers on its
 * Workflows tab. The list to pick from is every workflow on the account
 * the Studio is deployed under, so a site deployment offers from the
 * site's own workflows. Order is the order of the tiles.
 */

interface CatalogRow { name: string; displayName: string; description: string; tags?: string[]; curated: boolean }
interface GithubRow { entry: string; title: string; description: string }

export function WorkflowsSection() {
  const [catalog, setCatalog] = useState<CatalogRow[] | null>(null)
  const [source, setSource] = useState<'deployment' | 'viewer'>('deployment')
  const [market, setMarket] = useState<CatalogRow[]>([])
  const [gh, setGh] = useState('')
  const [ghNote, setGhNote] = useState('')
  const [ghBusy, setGhBusy] = useState(false)
  const [ghRows, setGhRows] = useState<GithubRow[]>([])
  const [ghBase, setGhBase] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const [filter, setFilter] = useState('')
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    fetch('/api/settings').then(r => r.json()).then(d => setPicked(d.effective?.workflowCollection ?? [])).catch(() => {})
    fetch('/api/workflows/catalog').then(async r => {
      const d = await r.json()
      if (!r.ok) throw new Error(d.error ?? `${r.status}`)
      setCatalog(d.workflows)
      setSource(d.source === 'viewer' ? 'viewer' : 'deployment')
      setMarket(Array.isArray(d.marketplace) ? d.marketplace : [])
    }).catch(e => setLoadError(`Cannot list the platform's workflows: ${(e as Error).message}`))
  }, [])

  const byName = useMemo(() => new Map<string, { displayName: string }>([
    ...[...(catalog ?? []), ...market].map(w => [w.name, w] as const),
    ...ghRows.map(w => [w.entry, { displayName: w.title }] as const),
  ]), [catalog, market, ghRows])
  const matches = (w: CatalogRow) => {
    const q = filter.trim().toLowerCase()
    return !q || w.name.toLowerCase().includes(q) || w.displayName.toLowerCase().includes(q) || (w.tags ?? []).some(t => t.toLowerCase().includes(q))
  }
  const shown = (catalog ?? []).filter(matches)
  const shownMarket = market.filter(matches)
  const shownGh = ghRows.filter(w => matches({ name: w.entry, displayName: w.title, description: w.description, curated: false }))
  // A found workflow's path below the directory that was searched.
  const relPath = (entry: string) => entry.replace(/@.*$/, '').slice(ghBase.length).replace(/^\//, '') || entry
  // A GitHub web link (github.com/o/r/tree/<ref>/<path> or /blob/...) is
  // rewritten to the entry form github.com/o/r/<path>@<ref>.
  const findGithub = async () => {
    let entry = gh.trim().replace(/^https?:\/\//, '').replace(/\.git$/, '').replace(/\/$/, '')
    const web = /^(github\.com\/[^/]+\/[^/]+)\/(?:tree|blob)\/([^/]+)(?:\/(.*))?$/.exec(entry)
    if (web) entry = `${web[1]}${web[3] ? `/${web[3]}` : ''}@${web[2]}`
    if (!entry) return
    setGhBusy(true); setGhNote(''); setGhRows([])
    try {
      const r = await fetch(`/api/workflows/github/browse?repo=${encodeURIComponent(entry)}`)
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error ?? `${r.status}`)
      const rows: GithubRow[] = d.workflows ?? []
      setGhRows(rows); setGhBase(entry.replace(/@.*$/, ''))
      setGhNote(rows.length ? `${rows.length} workflow${rows.length === 1 ? '' : 's'} at ${entry}. Tick the ones to offer, then Save.` : `No workflow.yaml or yamls/*.yaml at ${entry}.`)
    } catch (e) { setGhNote(String((e as Error).message)) } finally { setGhBusy(false) }
  }
  const toggle = (name: string) => { setDirty(true); setPicked(p => p.includes(name) ? p.filter(n => n !== name) : [...p, name]) }
  const move = (i: number, d: -1 | 1) => {
    const j = i + d
    if (j < 0 || j >= picked.length) return
    setDirty(true)
    setPicked(p => { const n = [...p]; [n[i], n[j]] = [n[j], n[i]]; return n })
  }
  const save = async () => {
    setBusy(true)
    try {
      const r = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ workflowCollection: picked }) })
      const d = await r.json()
      if (!r.ok) throw new Error(d.error ?? `${r.status}`)
      setDirty(false); toast.success('Saved. The Workflows tab shows the new set on its next load.')
    } catch (e) { toast.error(String((e as Error).message)) } finally { setBusy(false) }
  }

  return (
    <>
      <h1>Workflows</h1>
      <p className="muted view-sub">
        The workflows this Studio offers as tiles on its Workflows tab, each run from its own form under the viewer's
        account. Pick them from the account this Studio is deployed under, from the marketplace, or from a GitHub
        repository. A viewer who lacks an account workflow can add a copy from its tile; marketplace and GitHub
        workflows need no copy.
      </p>

      <div className="tool-group">Offered here ({picked.length})</div>
      {picked.length === 0 && <p className="muted">None yet. Tick workflows below.</p>}
      {picked.length > 0 && (
        <ol className="wf-picked">
          {picked.map((n, i) => (
            <li key={n}>
              <span className="wf-picked-name">{byName.get(n)?.displayName ?? n}</span>
              <code className="muted">{n}</code>
              {catalog && !n.includes('/') && !byName.has(n) && <span className="wf-fail">not on the platform account</span>}
              <span className="wf-picked-actions">
                <IconButton icon={<ArrowUpIcon />} label="Move up" disabled={i === 0} onClick={() => move(i, -1)} />
                <IconButton icon={<ArrowDownIcon />} label="Move down" disabled={i === picked.length - 1} onClick={() => move(i, 1)} />
                <Button variant="ghost" size="sm" onClick={() => toggle(n)}>Remove</Button>
              </span>
            </li>
          ))}
        </ol>
      )}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</Button>
      </div>

      <div className="tool-group">On the account{catalog ? ` (${catalog.length})` : ''}</div>
      {catalog && source === 'viewer' && (
        <p className="muted">Listed from your own account, because the deployment's platform credential is not usable. Until it is renewed, other viewers can run a workflow picked here only if it is already in their own account.</p>
      )}
      <SearchInput label="Filter by name or tag" value={filter} onValueChange={setFilter} />
      {loadError && <Callout tone="error" className="wf-load-error">{loadError}</Callout>}
      {!catalog && !loadError && <p className="muted"><Loader inline size={16} label="Loading…" /></p>}
      {catalog && (
        <Table density="compact" className="wf-catalog" wrapperClassName="rag-calls-wrap">
          <Table.Header />
          <Table.Header>Workflow</Table.Header>
          <Table.Header>Description</Table.Header>
          {shown.map(w => (
            <tr key={w.name}>
              <Table.Item><Checkbox checked={picked.includes(w.name)} onCheckedChange={() => toggle(w.name)} aria-label={`Offer ${w.displayName}`} /></Table.Item>
              <Table.Item><div>{w.displayName}</div><code className="muted">{w.name}</code></Table.Item>
              <Table.Item wrap className="muted">{w.description}</Table.Item>
            </tr>
          ))}
        </Table>
      )}

      <div className="tool-group">Marketplace{market.length ? ` (${market.length})` : ''}</div>
      {market.length === 0 && catalog && <p className="muted">No marketplace workflows are listed for this account.</p>}
      {shownMarket.length > 0 && (
        <Table density="compact" className="wf-catalog" wrapperClassName="rag-calls-wrap">
          <Table.Header />
          <Table.Header>Workflow</Table.Header>
          <Table.Header>Description</Table.Header>
          {shownMarket.map(w => (
            <tr key={w.name}>
              <Table.Item><Checkbox checked={picked.includes(w.name)} onCheckedChange={() => toggle(w.name)} aria-label={`Offer ${w.displayName}`} /></Table.Item>
              <Table.Item><div>{w.displayName}</div><code className="muted">{w.name}</code></Table.Item>
              <Table.Item wrap className="muted">{w.description}</Table.Item>
            </tr>
          ))}
        </Table>
      )}

      <div className="tool-group">From GitHub</div>
      <p className="muted">
        Workflows kept in a public GitHub repository, including components never published to the marketplace. Enter a
        repository, a directory, or a workflow file, as <code>github.com/owner/repo[/path][@ref]</code> or a GitHub link,
        and tick the workflows found there: each directory's workflow.yaml, or each of its yamls/ variants. They run as
        they are in the repository, without a copy in anyone's account.
      </p>
      <div className="query-actions">
        <Input aria-label="GitHub repository" placeholder="github.com/parallelworks/workflows/workflows@canary" value={gh} onChange={e => setGh(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') void findGithub() }} />
        <Button variant="secondary" loading={ghBusy} disabled={!gh.trim()} onClick={() => void findGithub()}>{ghBusy ? 'Looking…' : 'Find workflows'}</Button>
      </div>
      {ghNote && <p className="muted">{ghNote}</p>}
      {ghRows.length > 0 && (
        <Table density="compact" className="wf-catalog wf-gh" wrapperClassName="rag-calls-wrap">
          <Table.Header />
          <Table.Header>Workflow</Table.Header>
          <Table.Header>Description</Table.Header>
          {shownGh.map(w => (
            <tr key={w.entry}>
              <Table.Item><Checkbox checked={picked.includes(w.entry)} onCheckedChange={() => toggle(w.entry)} aria-label={`Offer ${w.title}`} /></Table.Item>
              <Table.Item><div>{w.title}</div><code className="muted" title={w.entry}>{relPath(w.entry)}</code></Table.Item>
              <Table.Item wrap className="muted"><span className="wf-clamp">{w.description}</span></Table.Item>
            </tr>
          ))}
        </Table>
      )}
    </>
  )
}
