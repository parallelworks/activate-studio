import { useState } from 'react'
import { Button, Callout, Input, Modal } from '@parallelworks/ui'
import { api, type ReadThrough } from '../api'
import { sshOptionsProblem } from '../sshOptions'
import { StepList, type CheckStep } from './LibraryFormParts'
import { errText } from './PrimaryFolder'

/** The host as one line: user@host[:port], and the folder there. */
export function readThroughText(rt: ReadThrough): string {
  return `${rt.destination}${rt.port ? `:${rt.port}` : ''}${rt.root ? `:${rt.root}` : ''}`
}

const EMPTY = { destination: '', sshOptions: '', root: '' }

/**
 * The row's "Originals also read through" setting: another host over ssh
 * that has the library's files, such as a file transfer host that mounts the
 * file systems the index host does not. The assistant's read_original and the
 * viewer try the library's own location first and then this host, over ssh
 * with this account's keys. A host is saved only after its connection test
 * passes with the same fields.
 */
export function ReadThroughControl({ id, label, current, canEdit, onChanged }: {
  id: string
  label: string
  current: ReadThrough | null | undefined
  /** False where the Studio signs people in (it lends no ssh keys) or the library is set by the deployment. */
  canEdit: boolean
  onChanged: () => Promise<unknown> | void
}) {
  const [open, setOpen] = useState(false)
  const [form, setForm] = useState(EMPTY)
  const [steps, setSteps] = useState<CheckStep[] | null>(null)
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'save' | 'clear' | null>(null)
  const [error, setError] = useState<string | null>(null)

  const begin = () => {
    setForm(current ? { destination: `${current.destination}${current.port ? `:${current.port}` : ''}`, sshOptions: current.sshOptions ?? '', root: current.root ?? '' } : EMPTY)
    setSteps(null); setTested(''); setError(null); setOpen(true)
  }
  const body = { destination: form.destination.trim(), ...(form.sshOptions.trim() ? { sshOptions: form.sshOptions.trim() } : {}), ...(form.root.trim() ? { root: form.root.trim() } : {}) }
  const key = JSON.stringify(body)
  const problem = sshOptionsProblem(form.sshOptions)
  const passed = !!steps && steps.every(s => s.ok) && tested === key
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }

  const test = async () => {
    setBusy('test'); setError(null); setSteps(null)
    try {
      const r = await api.testReadThrough(id, body)
      setSteps(r.steps); setTested(key)
    } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  const save = async () => {
    setBusy('save'); setError(null)
    try { await api.setReadThrough(id, body); setOpen(false); await onChanged() } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  const clear = async () => {
    setBusy('clear'); setError(null)
    try { await api.setReadThrough(id, { destination: '' }); setOpen(false); await onChanged() } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }

  return (
    <>
      <span className="lib-readthrough">
        {current ? <code title={readThroughText(current)}>{readThroughText(current)}</code> : <span className="muted">none</span>}
        {canEdit && (
          <Button variant="secondary" size="sm" aria-label={`${current ? 'Change' : 'Set'} the host originals are read through for ${label}`} onClick={begin}>
            {current ? 'Change' : 'Set'}
          </Button>
        )}
      </span>
      <Modal open={open} onClose={() => { if (!busy) setOpen(false) }} dismissible={!busy} size="lg"
        title="Read originals through another host"
        description={`${label}: when a file cannot be opened where the library is, the assistant and the viewer try this host over ssh, as you, with your own keys. Use it where the index host does not have the files and another host, such as a file transfer host, mounts them.`}
        footer={(
          <>
            {current && <Button variant="danger-outline" loading={busy === 'clear'} disabled={busy !== null} onClick={() => void clear()}>Remove</Button>}
            <Button variant="secondary" disabled={busy !== null} onClick={() => setOpen(false)}>Cancel</Button>
            <Button variant="secondary" loading={busy === 'test'} disabled={!body.destination || !!problem || busy !== null} onClick={() => void test()}>Test connection</Button>
            <Button variant="primary" loading={busy === 'save'} disabled={!passed || busy !== null} onClick={() => void save()}>Save</Button>
          </>
        )}>
        <div className="settings-grid">
          <Input label="Host" placeholder="user@host or user@host:port" autoComplete="off" spellCheck={false} value={form.destination}
            description="As you would type it after ssh, or a Host from your ~/.ssh/config." onChange={e => set({ destination: e.target.value })} />
          <Input label="Folder on that host" hint="optional" mono placeholder="same path as the library's own" spellCheck={false} value={form.root}
            description="The library's folder there, when its path differs from the one the library has. An absolute path." onChange={e => set({ root: e.target.value })} />
          <Input label="SSH options" hint="optional" mono placeholder="-J user@jumphost -o ConnectTimeout=20" autoComplete="off" spellCheck={false} value={form.sshOptions}
            error={problem ?? undefined} description="For a jump host or a key: -J, -p, -i, -l, -F, and -o Keyword=value." onChange={e => set({ sshOptions: e.target.value })} />
        </div>
        {steps && <StepList steps={steps} label="Connection test" />}
        {error && <Callout tone="error" live>{error}</Callout>}
        {steps && !passed && !busy && <p className="muted">{steps.every(s => s.ok) ? 'Fields changed: test again to save.' : 'Fix the failing step and test again.'}</p>}
      </Modal>
    </>
  )
}
