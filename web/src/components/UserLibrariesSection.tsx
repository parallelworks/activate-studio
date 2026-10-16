import { useEffect, useRef, useState } from 'react'
import { Button, Checkbox, DescriptionList, DescriptionListItem, Field, Input, Select, StatusDot, Table, toast } from '@parallelworks/ui'
import { api, type UserLibrary } from '../api'
import { reloadAppConfig } from '../config'

/**
 * Settings > Libraries > Your libraries: indexes of directories on the
 * user's own clusters. Adding one runs the index workflow there with the
 * user's key; when it finishes, the library appears in the Library switcher
 * for this user alone, read only, browsed and searched on the cluster.
 */
export function UserLibrariesSection() {
  const [libs, setLibs] = useState<UserLibrary[]>([])
  const [available, setAvailable] = useState(true)
  const [sites, setSites] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ site: '', sourceDir: '$WORKDIR', label: '', slurm: false, partition: '', account: '' })
  const ready = useRef(new Set<string>())

  // The list reloads every 15s during a build; one toast stands for a
  // failure that repeats rather than stacking another each time.
  const load = () => api.userLibraries().then(r => {
    setAvailable(r.available); setLibs(r.libraries)
    // A library that just finished joins the switcher.
    const now = new Set(r.libraries.filter(l => l.state === 'ready').map(l => l.id))
    if ([...now].some(id => !ready.current.has(id)) && ready.current.size + now.size > 0) reloadAppConfig()
    ready.current = now
  }).catch(e => toast.error(String((e as Error).message ?? e), { id: 'user-libraries-load' }))

  useEffect(() => {
    void load()
    api.agentSites().then(r => {
      const names = r.sites.filter(s => s.kind === 'cluster').map(s => s.name)
      setSites(names); setForm(f => (f.site || !names.length ? f : { ...f, site: names[0] }))
    }).catch(() => {})
  }, [])
  // Follow running builds.
  const running = libs.some(l => l.state === 'building' || l.state === 'updating')
  useEffect(() => {
    if (!running) return
    const t = window.setInterval(() => { void load() }, 15_000)
    return () => window.clearInterval(t)
  }, [running])

  const act = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true)
    try { await fn(); if (done) toast.success(done); await load() } catch (e) { toast.error(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  const add = () => act(async () => {
    await api.addUserLibrary({
      site: form.site, sourceDir: form.sourceDir.trim() || '$WORKDIR', label: form.label.trim() || undefined,
      scheduler: form.slurm && form.partition.trim() ? { partition: form.partition.trim(), account: form.account.trim() || undefined } : null,
    })
    setForm(f => ({ ...f, label: '' }))
  }, 'Building the index on the cluster. It appears in the Library switcher when it is ready.')

  if (!available) return null
  const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')
  return (
    <>
      <div className="tool-group">Your libraries</div>
      <p className="muted view-sub">
        Directories on your clusters, indexed there with your own key and searched there as you. Only you see them.
      </p>
      {libs.length > 0 && (
        <Table density="compact" className="libraries-table" wrapperClassName="rag-calls-wrap libraries-wrap">
          <Table.Header>Library</Table.Header>
          <Table.Header>State</Table.Header>
          <Table.Header>Contents</Table.Header>
          <Table.Header />
          {libs.map(l => {
            const building = l.state === 'building' || l.state === 'updating'
            return (
              <tr key={l.id}>
                <Table.Item wrap>
                  <div className="lib-name">{l.label}</div>
                  <div className="lib-id">{l.id}, on {l.site}, read only</div>
                  <DescriptionList className="lib-paths">
                    <DescriptionListItem label="Index" mono={!!l.indexRoot} copy={!!l.indexRoot}>
                      {l.indexRoot ? `${l.site}:${l.indexRoot}` : <span className="muted">being built</span>}
                    </DescriptionListItem>
                    <DescriptionListItem label="Source" mono copy>{`${l.site}:${l.sourceRoot ?? l.sourceDir}`}</DescriptionListItem>
                  </DescriptionList>
                </Table.Item>
                <Table.Item wrap>
                  <StatusDot variant={l.state === 'failed' ? 'warning' : 'success'} pulse={building} />{' '}
                  {l.state === 'building' ? 'Building' : l.state === 'updating' ? 'Updating' : l.state === 'failed' ? 'Failed' : 'Ready'}
                  {l.run && building && <div className="lib-id">run {l.run.slug}</div>}
                  {l.error && <div className="lib-id">{l.error}</div>}
                </Table.Item>
                <Table.Item>
                  {l.directories != null && <div>{l.directories.toLocaleString()} folders, {(l.files ?? 0).toLocaleString()} files</div>}
                  {l.builtAt && <div className="lib-id">indexed {when(l.builtAt)}</div>}
                </Table.Item>
                <Table.Item className="lib-actions">
                  <Button variant="secondary" size="sm" disabled={busy || building} onClick={() => void act(() => api.refreshUserLibrary(l.id), 'Updating the index on the cluster.')}>Refresh</Button>
                  <Button variant="secondary" size="sm" disabled={busy} onClick={() => void act(async () => { await api.removeUserLibrary(l.id); reloadAppConfig() })}>Remove</Button>
                </Table.Item>
              </tr>
            )
          })}
        </Table>
      )}
      <div className="settings-grid">
        <Field label="Cluster">
          <Select items={sites.map(s => ({ value: s, label: s }))} value={form.site || null}
            placeholder="No active clusters" disabled={!sites.length}
            onValueChange={v => v && setForm({ ...form, site: v })} />
        </Field>
        <Input label="Directory" placeholder="$WORKDIR" value={form.sourceDir} onChange={e => setForm({ ...form, sourceDir: e.target.value })} />
        <Input label="Label" placeholder={`${form.site || 'cluster'} ${form.sourceDir || '$WORKDIR'}`} value={form.label} onChange={e => setForm({ ...form, label: e.target.value })} />
        <div>
          <Field controlFirst label="Build as a Slurm job (otherwise on the login node)">
            <Checkbox checked={form.slurm} onCheckedChange={v => setForm({ ...form, slurm: v })} />
          </Field>
          {form.slurm && (
            <div className="user-lib-slurm">
              <Input aria-label="Partition" placeholder="Partition" value={form.partition} onChange={e => setForm({ ...form, partition: e.target.value })} />
              <Input aria-label="Account" placeholder="Account, if the site requires one" value={form.account} onChange={e => setForm({ ...form, account: e.target.value })} />
            </div>
          )}
        </div>
      </div>
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!form.site || (form.slurm && !form.partition.trim())} onClick={() => void add()}>{busy ? 'Starting…' : 'Add from cluster'}</Button>
      </div>
    </>
  )
}
