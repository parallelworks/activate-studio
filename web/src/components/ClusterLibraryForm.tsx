import { useEffect, useState } from 'react'
import { Button, Callout, Checkbox, Field, Input, Select } from '@parallelworks/ui'
import { api, type UserLibrary } from '../api'

/**
 * Builds a GUFI index of a directory on one of the user's clusters, with
 * their own key, through the platform's index workflow (POST
 * /api/user-libraries). Offered only where the Studio reaches a platform.
 * The library is ready, for this user alone, when the run finishes.
 */
export function ClusterLibraryForm({ onStarted }: { onStarted?: (lib: UserLibrary) => void }) {
  const [sites, setSites] = useState<string[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [form, setForm] = useState({ site: '', sourceDir: '$WORKDIR', label: '', slurm: false, partition: '', account: '' })

  useEffect(() => {
    api.agentSites().then(r => {
      const names = r.sites.filter(s => s.kind === 'cluster').map(s => s.name)
      setSites(names)
      setForm(f => (f.site || !names.length ? f : { ...f, site: names[0] }))
    }).catch(() => setSites([]))
  }, [])

  const add = async () => {
    setBusy(true)
    setError(null)
    try {
      const lib = await api.addUserLibrary({
        site: form.site, sourceDir: form.sourceDir.trim() || '$WORKDIR', label: form.label.trim() || undefined,
        scheduler: form.slurm && form.partition.trim() ? { partition: form.partition.trim(), account: form.account.trim() || undefined } : null,
      })
      setForm(f => ({ ...f, label: '' }))
      onStarted?.(lib)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }

  return (
    <div className="cluster-library-form">
      <p className="muted view-sub">
        A directory on one of your clusters, indexed there with your own key and searched there as you. Only you see it.
      </p>
      <div className="settings-grid">
        <Field label="Cluster">
          <Select items={(sites ?? []).map(s => ({ value: s, label: s }))} value={form.site || null}
            placeholder={sites === null ? 'Loading…' : 'No active clusters'} disabled={!sites?.length}
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
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!form.site || (form.slurm && !form.partition.trim()) || busy} onClick={() => void add()}>
          {busy ? 'Starting…' : 'Build the index'}
        </Button>
      </div>
    </div>
  )
}
