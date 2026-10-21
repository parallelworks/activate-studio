import { useEffect, useState } from 'react'
import { Button, Callout, DescriptionList, DescriptionListItem, Field, Input, NumberInput, Select, StatusDot } from '@parallelworks/ui'
import { api } from '../api'

/**
 * Adds a read-only library whose GUFI index already exists elsewhere: on
 * another machine, reached with the user's own ssh (keys, agent and
 * ~/.ssh/config), or with `transport="pw"` on one of the user's running
 * clusters, reached with pw ssh. Used by Settings, Libraries and by the
 * setup wizard.
 *
 * The connection test runs first and reports each step (the connection,
 * GUFI, the index, the source directory); Add is offered once a test of
 * the fields as they now stand has passed. The server tests again before
 * adding.
 */
export interface SshLibraryAdded { id: string; label: string }

interface Step { name: 'ssh' | 'gufi' | 'index' | 'source'; ok: boolean; detail: string }
interface TestResult { ok: boolean; steps: Step[]; gufiBin?: string; gufiVersion?: string; directories?: number }

const STEP_LABEL: Record<Step['name'], string> = { ssh: 'Connection', gufi: 'GUFI', index: 'Index', source: 'Source directory' }
const EMPTY = { label: '', destination: '', cluster: '', port: null as number | null, indexRoot: '', sourceRoot: '', gufiBin: '' }

async function post<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = (await res.json().catch(() => ({}))) as { error?: string }
  if (!res.ok) throw new Error(data.error ?? `${url}: ${res.status}`)
  return data as T
}

export function SshLibraryForm({ onAdded, compact = false, transport = 'ssh' }: {
  onAdded?: (lib: SshLibraryAdded) => void
  compact?: boolean
  transport?: 'ssh' | 'pw'
}) {
  const pw = transport === 'pw'
  const [form, setForm] = useState(EMPTY)
  const [result, setResult] = useState<TestResult | null>(null)
  // The fields the last test ran with; a change after it asks for another.
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'add' | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Over pw ssh: the user's running clusters, as the cluster form lists them.
  const [clusters, setClusters] = useState<string[] | null>(null)
  useEffect(() => {
    if (!pw) return
    api.agentSites().then(r => {
      const names = r.sites.filter(s => s.kind === 'cluster').map(s => s.name)
      setClusters(names)
      setForm(f => (f.cluster || !names.length ? f : { ...f, cluster: names[0] }))
    }).catch(() => setClusters([]))
  }, [pw])

  const paths = { indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined, gufiBin: form.gufiBin.trim() || undefined }
  const conn = pw
    ? { cluster: form.cluster, ...paths }
    : { destination: form.destination.trim(), port: form.port ?? undefined, ...paths }
  const passed = !!result?.ok && tested === JSON.stringify(conn)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }
  const host = pw ? form.cluster : form.destination.trim().replace(/^.*@/, '')
  const indexName = paths.indexRoot.replace(/\/+$/, '').split('/').pop() || 'index'
  const route = pw ? '/api/user-libraries/pw' : '/api/user-libraries/ssh'

  const test = async () => {
    setBusy('test'); setError(null); setResult(null)
    try {
      const r = await post<TestResult>(`${route}/test`, conn)
      // Where the test found GUFI fills the empty field, and the result
      // stands for the fields with it.
      const found = !paths.gufiBin && r.gufiBin ? r.gufiBin : null
      if (found) setForm(f => ({ ...f, gufiBin: found }))
      setResult(r)
      setTested(JSON.stringify(found ? { ...conn, gufiBin: found } : conn))
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const add = async () => {
    setBusy('add'); setError(null)
    try {
      const lib = await post<SshLibraryAdded>(route, { ...conn, label: form.label.trim() || undefined })
      setForm(f => ({ ...EMPTY, cluster: f.cluster })); setResult(null); setTested('')
      onAdded?.({ id: lib.id, label: lib.label })
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const help = (text: string) => (compact ? undefined : text)
  return (
    <div className="ssh-library-form">
      {!compact && (
        <p className="muted view-sub">
          {pw
            ? 'An existing GUFI index on one of your running clusters, read over pw ssh with this computer\'s pw sign-in. Read only: nothing is written there.'
            : 'An existing GUFI index on another machine, read over ssh with your own keys. The Studio cannot answer a password prompt, so key access is needed. Read only: nothing is written there.'}
        </p>
      )}
      <div className="settings-grid">
        {pw ? (
          <Field label="Cluster" description={help('One of your running clusters on ACTIVATE.')}>
            <Select items={(clusters ?? []).map(c => ({ value: c, label: c }))} value={form.cluster || null}
              placeholder={clusters === null ? 'Loading…' : 'No running clusters'} disabled={!clusters?.length}
              onValueChange={v => v && set({ cluster: v })} />
          </Field>
        ) : (
          <>
            <Input label="Host" placeholder="user@host or an ssh alias" autoComplete="off" spellCheck={false} value={form.destination}
              description={help('As you would type it after ssh; your ~/.ssh/config applies.')} onChange={e => set({ destination: e.target.value })} />
            <Field label="Port" hint="optional" description={help('Leave empty for 22, or for the port your ~/.ssh/config sets.')}>
              <NumberInput min={1} max={65535} placeholder="22" value={form.port} onValueChange={v => set({ port: v })} />
            </Field>
          </>
        )}
        <Input label="Index directory" mono placeholder="/path/to/index" spellCheck={false} value={form.indexRoot}
          description={help(`The top of the GUFI index on that ${pw ? 'cluster' : 'machine'}: the directory with db.db.`)} onChange={e => set({ indexRoot: e.target.value })} />
        <Input label="Source directory" hint="optional" mono placeholder="/path/to/files" spellCheck={false} value={form.sourceRoot}
          description={help('The indexed files, for previews. Without it, names and metadata only.')} onChange={e => set({ sourceRoot: e.target.value })} />
        <Input label="GUFI bin directory" hint="optional" mono placeholder="found by the test" spellCheck={false} value={form.gufiBin}
          description={help('Where gufi_query is. The test looks on the PATH, in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, and /opt/gufi/bin.')} onChange={e => set({ gufiBin: e.target.value })} />
        <Input label="Label" hint="optional" placeholder={paths.indexRoot && host ? `${indexName} on ${host}` : 'Shown in the Library switcher'} value={form.label}
          onChange={e => setForm(f => ({ ...f, label: e.target.value }))} />
      </div>
      {result && (
        <DescriptionList aria-label="Connection test">
          {result.steps.map(s => (
            <DescriptionListItem key={s.name} label={STEP_LABEL[s.name]}>
              <StatusDot variant={s.ok ? 'success' : 'error'} /> {s.detail}
            </DescriptionListItem>
          ))}
        </DescriptionList>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={!(pw ? form.cluster : form.destination.trim()) || !paths.indexRoot || busy !== null} onClick={() => void test()}>
          Test connection
        </Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null} onClick={() => void add()}>Add library</Button>
        {result && !passed && !busy && <span className="muted">{result.ok ? 'Fields changed: test again to add.' : 'Fix the failing step and test again.'}</span>}
      </div>
    </div>
  )
}
