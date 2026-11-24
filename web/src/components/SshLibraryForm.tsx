import { useState } from 'react'
import { Button, Callout, Input } from '@parallelworks/ui'
import { type PwSystems } from '../api'
import { sshOptionsProblem } from '../sshOptions'
import { openAccounts } from '../accounts'
import { NeedsActivate } from './NeedsActivate'
import { postJson, StepList, type CheckStep } from './LibraryFormParts'
import { PwSystemField } from './PwSystemField'

/**
 * A library on a remote system: one reached with the user's own ssh (keys,
 * agent and ~/.ssh/config, the host written user@host or user@host:port),
 * or with `transport="pw"` one of the systems the signed-in pw CLI reaches,
 * over pw ssh: picked from the person's systems on the platform, running
 * ones first, or typed when the list cannot be had. Where pw ssh cannot be
 * used yet (no pw CLI found, no sign-in), the form says why instead. Used
 * by Settings, Indexing, Libraries and by the setup wizard.
 *
 * The GUFI index is there already: the connection test runs first and
 * reports each step (the connection, GUFI, the index, the source
 * directory), and Add is offered once a test of the fields as they now
 * stand has passed. The server tests again before it adds. Read only. A
 * folder to index there is IndexFolderForm's.
 */
export interface SshLibraryAdded { id: string; label: string }

interface TestResult { ok: boolean; steps: CheckStep[]; gufiBin?: string; gufiVersion?: string; directories?: number }

const EMPTY = { label: '', destination: '', sshOptions: '', cluster: '', indexRoot: '', sourceRoot: '', gufiBin: '' }

export function SshLibraryForm({ onAdded, compact = false, transport = 'ssh', onOpenAccess, cluster, destination }: {
  onAdded?: (lib: SshLibraryAdded) => void
  /** The system, chosen by the form around this one (Add a library); the
   *  form then shows no System or Host field of its own. */
  cluster?: string
  destination?: string
  compact?: boolean
  transport?: 'ssh' | 'pw'
  /** Opens Settings, Accounts; by default, by its address. */
  onOpenAccess?: () => void
}) {
  const pw = transport === 'pw'
  const [form, setForm] = useState(EMPTY)
  const [result, setResult] = useState<TestResult | null>(null)
  // The fields the last test ran with; a change after it asks for another.
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'add' | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Over pw ssh: the person's systems on the platform (PwSystemField), or
  // why pw ssh cannot be used here.
  const [systems, setSystems] = useState<PwSystems | null>(null)

  const clusterOf = cluster ?? form.cluster
  const destinationOf = (destination ?? form.destination).trim()
  const paths = { indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined, gufiBin: form.gufiBin.trim() || undefined }
  const options = form.sshOptions.trim()
  const optionsProblem = pw ? null : sshOptionsProblem(options)
  const conn = pw ? { cluster: clusterOf, ...paths } : { destination: destinationOf, ...(options ? { sshOptions: options } : {}), ...paths }
  const passed = !!result?.ok && tested === JSON.stringify(conn)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }
  const host = pw ? clusterOf : destinationOf.replace(/^.*@/, '').replace(/:\d+$/, '')
  const name = paths.indexRoot.replace(/\/+$/, '').split('/').pop() || 'index'
  const route = `/api/user-libraries/${pw ? 'pw' : 'ssh'}`
  const required = paths.indexRoot

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
      const lib = await postJson<SshLibraryAdded>(route, { ...conn, label: form.label.trim() || undefined })
      setForm(f => ({ ...EMPTY, cluster: f.cluster })); setResult(null); setTested('')
      onAdded?.({ id: lib.id, label: lib.label })
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const help = (text: string) => (compact ? undefined : text)
  const where = pw ? 'system' : 'machine'
  if (pw && systems && !systems.usable) {
    return (
      <div className="ssh-library-form">
        <PwBlocked why={systems.why} onOpen={onOpenAccess} />
      </div>
    )
  }
  return (
    <div className="ssh-library-form">
      {!compact && (
        <p className="muted view-sub">
          {pw
            ? 'An existing GUFI index on one of your remote computers, read over pw ssh with this computer\'s pw sign-in. Read only: nothing is written there.'
            : 'An existing GUFI index on a remote computer, read over ssh with your own keys. The Studio cannot answer a password prompt, so key access is needed. Read only: nothing is written there.'}
        </p>
      )}
      {pw && systems?.error && (
        <Callout tone="warning">Your clusters could not be listed: {systems.error} Type the name of a running cluster instead.</Callout>
      )}
      <div className="settings-grid">
        {pw ? (
          <>
            {cluster === undefined && <PwSystemField value={form.cluster} onChange={c => set({ cluster: c })} help={help} onSystems={setSystems} />}
          </>
        ) : (
          <>
            {destination === undefined && (
              <Input label="Host" placeholder="user@host or user@host:port" autoComplete="off" spellCheck={false} value={form.destination}
                description={help('As you would type it after ssh, with :port for a port other than 22. A Host from your ~/.ssh/config works, including ProxyJump.')} onChange={e => set({ destination: e.target.value })} />
            )}
            <Input label="SSH options" hint="optional" mono placeholder="-J user@jumphost -o ConnectTimeout=20" autoComplete="off" spellCheck={false} value={form.sshOptions}
              error={optionsProblem ?? undefined}
              description={help('For a jump host or other settings: -J, -p, -i, -l, -F, and -o Keyword=value. Used on every ssh to this host.')} onChange={e => set({ sshOptions: e.target.value })} />
          </>
        )}
        <Input label="Index directory" mono placeholder="/path/to/index" spellCheck={false} value={form.indexRoot}
          description={help(`The top of the GUFI index on that ${where}: the directory with db.db.`)} onChange={e => set({ indexRoot: e.target.value })} />
        <Input label="Source directory" hint="optional" mono placeholder="/path/to/files" spellCheck={false} value={form.sourceRoot}
          description={help('The indexed files, for previews. Without it, names and metadata only.')} onChange={e => set({ sourceRoot: e.target.value })} />
        <Input label="GUFI bin directory" hint="optional" mono placeholder="found by the test" spellCheck={false} value={form.gufiBin}
          description={help('Where GUFI\'s tools are. The test looks on the PATH, in ~/.activate-studio/gufi, /usr/local/bin, ~/usr/local/bin, and /opt/gufi/bin.')} onChange={e => set({ gufiBin: e.target.value })} />
        <Input label="Label" hint="optional" placeholder={required && host ? name : 'Shown in the Library switcher'} value={form.label}
          onChange={e => setForm(f => ({ ...f, label: e.target.value }))} />
      </div>
      {result && <StepList steps={result.steps} label="Connection test" />}
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={!(pw ? clusterOf : destinationOf) || !required || !!optionsProblem || busy !== null} onClick={() => void test()}>
          Test connection
        </Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null}
          onClick={() => void add()}>
          Add library
        </Button>
        {result && !passed && !busy && <span className="muted">{result.ok ? 'Fields changed: test again to add.' : 'Fix the failing step and test again.'}</span>}
      </div>
    </div>
  )
}

/** Why pw ssh cannot be used yet: the pw CLI is not found, which is said as
 *  it is, or no ACTIVATE credential, which is the prompt every ACTIVATE
 *  feature shows. Both open Settings, Accounts, where either is set. */
export function PwBlocked({ why, onOpen }: { why?: string | null; onOpen?: () => void }) {
  if (why && /pw CLI/.test(why) && /not find|not there/.test(why)) return (
    <Callout tone="warning" title="pw ssh cannot be used yet"
      action={<Button variant="secondary" size="sm" onClick={() => (onOpen ? onOpen() : openAccounts('activate'))}>Open Accounts</Button>}>
      {why}
    </Callout>
  )
  return <NeedsActivate onOpen={onOpen}>{why}</NeedsActivate>
}
