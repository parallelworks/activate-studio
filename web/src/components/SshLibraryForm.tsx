import { useEffect, useState } from 'react'
import { Button, Callout, Field, Input, Select } from '@parallelworks/ui'
import { api, type IndexDepth, type PwSystems } from '../api'
import { DEFAULT_LIBRARY_DEPTH, IndexDepthControl } from './IndexDepth'
import { CreateConfirm, postJson, StepList, type CheckStep } from './LibraryFormParts'

/**
 * A library on a remote system: one reached with the user's own ssh (keys,
 * agent and ~/.ssh/config, the host written user@host or user@host:port),
 * or with `transport="pw"` one of the systems the signed-in pw CLI reaches,
 * over pw ssh: picked from the person's systems on the platform, running
 * ones first, or typed when the list cannot be had. Where pw ssh cannot be
 * used yet (no pw CLI found, no sign-in), the form says why instead. Used
 * by Settings, Libraries and by the setup wizard.
 *
 * By default (`mode` connect) the GUFI index is there already: the
 * connection test runs first and reports each step (the connection, GUFI,
 * the index, the source directory), and Add is offered once a test of the
 * fields as they now stand has passed. Read only.
 *
 * With `mode` build, the Studio builds the index there: the test checks
 * the connection, that GUFI is installed, the source folder and the index
 * folder (made if missing, after asking), and whether text extraction can
 * run; Build the index starts gufi_dir2index there, at the index depth
 * chosen (OCR where tesseract is installed there, search by meaning where
 * the Studio's model is), and the library shows its progress in the list.
 * The server tests again before either.
 */
export interface SshLibraryAdded { id: string; label: string }

interface TestResult { ok: boolean; steps: CheckStep[]; gufiBin?: string; gufiVersion?: string; directories?: number; indexDir?: string; create?: string[] }

const EMPTY = { label: '', destination: '', cluster: '', indexRoot: '', sourceRoot: '', gufiBin: '' }

/** The list's choice for a system typed by name. */
const ANOTHER = '\u0000another'

export function SshLibraryForm({ onAdded, compact = false, transport = 'ssh', mode = 'connect', onOpenAccess }: {
  onAdded?: (lib: SshLibraryAdded) => void
  compact?: boolean
  transport?: 'ssh' | 'pw'
  mode?: 'connect' | 'build'
  /** Opens Settings, Model access; by default, by its address. */
  onOpenAccess?: () => void
}) {
  const pw = transport === 'pw'
  const build = mode === 'build'
  const [form, setForm] = useState(EMPTY)
  const [result, setResult] = useState<TestResult | null>(null)
  // The fields the last test ran with; a change after it asks for another.
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'add' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [depth, setDepth] = useState<IndexDepth>(DEFAULT_LIBRARY_DEPTH)
  // Over pw ssh: the person's systems on the platform, running first, the
  // first running one chosen; or why pw ssh cannot be used here.
  const [systems, setSystems] = useState<PwSystems | null>(null)
  const [typing, setTyping] = useState(false)
  useEffect(() => {
    if (!pw) return
    api.pwSystems().then(r => {
      setSystems(r)
      const first = r.systems.find(x => x.running)
      setForm(f => (f.cluster || !first ? f : { ...f, cluster: first.name }))
    }).catch(e => setSystems({ usable: true, why: null, systems: [], error: String((e as Error).message ?? e) }))
  }, [pw])
  // Without a list, the system is typed by name.
  const listed = !!systems?.systems.length
  const typed = pw && systems !== null && (typing || !listed)

  const paths = build
    ? { sourceRoot: form.sourceRoot.trim(), indexRoot: form.indexRoot.trim() || undefined, gufiBin: form.gufiBin.trim() || undefined }
    : { indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined, gufiBin: form.gufiBin.trim() || undefined }
  const conn = pw ? { cluster: form.cluster, ...paths } : { destination: form.destination.trim(), ...paths }
  const passed = !!result?.ok && tested === JSON.stringify(conn)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }
  const host = pw ? form.cluster : form.destination.trim().replace(/^.*@/, '').replace(/:\d+$/, '')
  const name = String((build ? paths.sourceRoot : paths.indexRoot) ?? '').replace(/\/+$/, '').split('/').pop() || (build ? 'folder' : 'index')
  const route = `/api/user-libraries/${pw ? 'pw' : 'ssh'}${build ? '/build' : ''}`
  const required = build ? paths.sourceRoot : paths.indexRoot

  const test = async () => {
    setBusy('test'); setError(null); setResult(null)
    try {
      const r = await postJson<TestResult>(`${route}/test`, conn)
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
      const lib = await postJson<SshLibraryAdded>(route, { ...conn, label: form.label.trim() || undefined, ...(build ? { depth } : {}), ...(build && result?.create?.length ? { create: true } : {}) })
      setForm(f => ({ ...EMPTY, cluster: f.cluster })); setResult(null); setTested('')
      onAdded?.({ id: lib.id, label: lib.label })
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const help = (text: string) => (compact ? undefined : text)
  const where = pw ? 'system' : 'machine'
  if (pw && systems && !systems.usable) {
    return (
      <div className="ssh-library-form">
        <Callout tone="warning" title="pw ssh cannot be used yet"
          action={<Button variant="secondary" size="sm" onClick={() => (onOpenAccess ? onOpenAccess() : void (location.hash = '#view=settings:access'))}>Open Model access</Button>}>
          {systems.why}
        </Callout>
      </div>
    )
  }
  return (
    <div className="ssh-library-form">
      {!compact && (
        <p className="muted view-sub">
          {build
            ? (pw
              ? 'A folder on one of your systems, indexed there with GUFI over pw ssh with this computer\'s pw sign-in. The build runs there and leaves the folder as it is.'
              : 'A folder on another machine, indexed there with GUFI over ssh with your own keys. The build runs there and leaves the folder as it is. The Studio cannot answer a password prompt, so key access is needed.')
            : (pw
              ? 'An existing GUFI index on one of your systems, read over pw ssh with this computer\'s pw sign-in. Read only: nothing is written there.'
              : 'An existing GUFI index on another machine, read over ssh with your own keys. The Studio cannot answer a password prompt, so key access is needed. Read only: nothing is written there.')}
        </p>
      )}
      {pw && systems?.error && (
        <Callout tone="warning">Your systems could not be listed: {systems.error} Type the name of a running system instead.</Callout>
      )}
      <div className="settings-grid">
        {pw ? (
          <>
            {(systems === null || listed) && (
              <Field label="System" description={help('Your systems on the platform, running ones first. One that is off has to be started on ACTIVATE before it can be reached.')}>
                <Select value={typing ? ANOTHER : form.cluster || null} placeholder={systems === null ? 'Loading…' : 'Choose a system'} disabled={systems === null}
                  items={[
                    ...(systems?.systems ?? []).map(x => ({ value: x.name, label: x.label, description: x.label !== x.name ? x.name : undefined, hint: x.running ? 'running' : x.status })),
                    { value: ANOTHER, label: 'Another system…', description: 'Type its name' },
                  ]}
                  onValueChange={v => {
                    if (!v) return
                    setTyping(v === ANOTHER)
                    set({ cluster: v === ANOTHER ? '' : v })
                  }} />
              </Field>
            )}
            {typed && (
              <Input label={listed ? 'System name' : 'System'} placeholder="my-cluster" mono autoComplete="off" spellCheck={false} value={form.cluster}
                description={help('The name of a running system, as pw ssh takes it.')} onChange={e => set({ cluster: e.target.value.trim() })} />
            )}
          </>
        ) : (
          <Input label="Host" placeholder="user@host or user@host:port" autoComplete="off" spellCheck={false} value={form.destination}
            description={help('As you would type it after ssh, with :port for a port other than 22; your ~/.ssh/config applies.')} onChange={e => set({ destination: e.target.value })} />
        )}
        {build ? (
          <>
            <Input label="Source folder" mono placeholder="~/data" spellCheck={false} value={form.sourceRoot}
              description={help(`The folder to index on that ${where}. Created, empty, if it does not exist.`)} onChange={e => set({ sourceRoot: e.target.value })} />
            <Input label="Index folder" hint="optional" mono placeholder="~/.activate-studio/indexes/…" spellCheck={false} value={form.indexRoot}
              description={help('Where the index is written there: an empty or new folder, outside the source folder.')} onChange={e => set({ indexRoot: e.target.value })} />
          </>
        ) : (
          <>
            <Input label="Index directory" mono placeholder="/path/to/index" spellCheck={false} value={form.indexRoot}
              description={help(`The top of the GUFI index on that ${where}: the directory with db.db.`)} onChange={e => set({ indexRoot: e.target.value })} />
            <Input label="Source directory" hint="optional" mono placeholder="/path/to/files" spellCheck={false} value={form.sourceRoot}
              description={help('The indexed files, for previews. Without it, names and metadata only.')} onChange={e => set({ sourceRoot: e.target.value })} />
          </>
        )}
        <Input label="GUFI bin directory" hint="optional" mono placeholder="found by the test" spellCheck={false} value={form.gufiBin}
          description={help('Where GUFI\'s tools are. The test looks on the PATH, in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, and /opt/gufi/bin.')} onChange={e => set({ gufiBin: e.target.value })} />
        <Input label="Label" hint="optional" placeholder={required && host ? name : 'Shown in the Library switcher'} value={form.label}
          onChange={e => setForm(f => ({ ...f, label: e.target.value }))} />
      </div>
      {build && <IndexDepthControl value={depth} onChange={setDepth} disabled={busy !== null} />}
      {result && <StepList steps={result.steps} label="Connection test" />}
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={!(pw ? form.cluster : form.destination.trim()) || !required || busy !== null} onClick={() => void test()}>
          Test connection
        </Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null}
          onClick={() => (build && result?.create?.length ? setConfirming(true) : void add())}>
          {build ? 'Build the index' : 'Add library'}
        </Button>
        {result && !passed && !busy && <span className="muted">{result.ok ? 'Fields changed: test again to add.' : 'Fix the failing step and test again.'}</span>}
      </div>
      {build && (
        <CreateConfirm paths={result?.create ?? []} where={host ? `on ${host}` : undefined} open={confirming} onClose={() => setConfirming(false)}
          onConfirm={async () => { setConfirming(false); await add() }} />
      )}
    </div>
  )
}
