import { useState } from 'react'
import { Button, Callout, Disclosure, Input, Loader, Modal } from '@parallelworks/ui'
import { TransferError, bytesText, fieldError, transferApi, type SavedLocation, type SettingTest } from '../transferApi'

/** Why a saved location's fields cannot be used, field by field, checked
 *  before anything is sent. Empty when they can. */
export function locationProblems(d: { host: string; root: string }): { host?: string; root?: string } {
  const out: { host?: string; root?: string } = {}
  const host = d.host.trim()
  if (!host) out.host = 'Enter the host.'
  else if (/\s/.test(host)) out.host = 'A host has no spaces.'
  else if (host.startsWith('-')) out.host = 'A host cannot start with a dash.'
  const root = d.root.trim()
  if (!root) out.root = 'Enter the folder.'
  else if (!root.startsWith('/')) out.root = 'Start the folder with /, as /srv/archive.'
  else if (root.split('/').includes('..')) out.root = 'The folder cannot contain "..".'
  return out
}

type Field = 'host' | 'root' | 'port' | 'sshOptions' | 'label'

/** The field a server 400 is about, from its leading name ("root: ...",
 *  "savedLocations[2].host: ..."), with the rest of the message. */
function serverField(message: string): { field: Field; text: string } | null {
  const f = fieldError(message)
  if (!f) return null
  const last = f.field.split('.').pop() as string
  return (['host', 'root', 'port', 'sshOptions', 'label'] as string[]).includes(last) ? { field: last as Field, text: f.text } : null
}

/** What a new location's fields start with, such as what Studio knows of
 *  where a library's files are. A field left out starts empty. */
export type LocationPrefill = Partial<Pick<SavedLocation, 'label' | 'host' | 'root' | 'port' | 'sshOptions'>>

/** The dialog that adds or edits a saved location, from Settings, Transfers
 *  and from the Transfers page. Save tests it first and keeps it only when
 *  the test passes, or when asked to keep it anyway. `prefill` fills a new
 *  one, and `note` says where those values came from. */
export function LocationDialog({ first, prefill, note, onClose, onSave }: {
  first: SavedLocation | null
  prefill?: LocationPrefill
  note?: string
  onClose: () => void
  onSave: (l: SavedLocation, result: SettingTest | null) => Promise<void>
}) {
  const start = first ?? prefill
  const [d, setD] = useState({
    label: start?.label ?? '', host: start?.host ?? '', root: start?.root ?? '',
    port: start?.port ? String(start.port) : '', sshOptions: start?.sshOptions ?? '',
  })
  const [touched, setTouched] = useState({ host: !!first, root: !!first })
  const [busy, setBusy] = useState<'test' | 'save' | null>(null)
  const [result, setResult] = useState<SettingTest | null>(null)
  const [server, setServer] = useState<{ field: Field; text: string } | null>(null)
  const [error, setError] = useState('')
  const problems: Partial<Record<Field, string>> = locationProblems(d)
  const port = d.port.trim()
  if (port && !(/^\d+$/.test(port) && +port >= 1 && +port <= 65535)) problems.port = 'A port from 1 to 65535.'
  if (server && !problems[server.field]) problems[server.field] = server.text
  const ok = !problems.host && !problems.root && !problems.port
  const row = (): SavedLocation => {
    const out: SavedLocation = { ...(first ?? { id: '' }), label: d.label.trim() || d.host.trim(), host: d.host.trim(), root: d.root.trim() }
    delete out.port; delete out.sshOptions
    if (port) out.port = +port
    if (d.sshOptions.trim()) out.sshOptions = d.sshOptions.trim()
    return out
  }
  const fail = (e: unknown) => {
    const msg = (e as Error).message
    const f = serverField(msg)
    if (f) { setServer(f); setTouched({ host: true, root: true }) } else setError(msg)
  }

  const keep = async (r: SettingTest | null = result) => {
    setBusy('save'); setError('')
    try { await onSave(row(), r) } catch (e) { fail(e) } finally { setBusy(null) }
  }
  const testThenSave = async () => {
    setTouched({ host: true, root: true })
    if (!ok) return
    setBusy('test'); setError(''); setResult(null)
    const l = row()
    let r: SettingTest
    try {
      r = await transferApi.testSetting({ host: l.host, root: l.root, rsyncPath: l.rsyncPath, port: l.port, sshOptions: l.sshOptions })
    } catch (e) {
      // A 400 names the field it is about; that shows under the field.
      setBusy(null)
      if (e instanceof TransferError && e.status === 400 && serverField(e.message)) { fail(e); return }
      r = { ok: false, why: (e as Error).message }
    }
    setResult(r); setBusy(null)
    if (r.ok) await keep(r)
  }
  const edit = (patch: Partial<typeof d>) => { setD({ ...d, ...patch }); setResult(null); setServer(null) }
  const shown = (f: 'host' | 'root') => (touched[f] ? problems[f] : undefined)

  return (
    <Modal open onClose={onClose} size="md" title={first ? 'Edit saved location' : 'Add a saved location'}
      description="A folder on another computer that Studio reaches over ssh."
      onSubmit={() => void testThenSave()}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          {result && !result.ok && <Button variant="secondary" onClick={() => void keep()} loading={busy === 'save'}>Save anyway</Button>}
          <Button variant="primary" type="submit" loading={busy !== null} disabled={busy !== null}>{busy === 'save' ? 'Saving…' : result && !result.ok ? 'Test again' : 'Test and save'}</Button>
        </>
      }>
      <div className="xfer-dialog-fields">
        {note && <Callout tone="info" size="sm">{note}</Callout>}
        <Input label="Label" value={d.label} placeholder="Archive" onChange={e => edit({ label: e.target.value })}
          description="How it is named in the location picker. The host is used when this is empty." />
        <Input label="Host" mono value={d.host} placeholder="user@archive-host" autoComplete="off" spellCheck={false}
          description="An ssh destination: user@host, or a Host name from your ssh config."
          error={shown('host')}
          onBlur={() => setTouched(t => ({ ...t, host: true }))} onChange={e => edit({ host: e.target.value })} />
        <Input label="Folder" mono value={d.root} placeholder="/srv/archive" autoComplete="off" spellCheck={false}
          description="Transfers stay inside this folder."
          error={shown('root')}
          onBlur={() => setTouched(t => ({ ...t, root: true }))} onChange={e => edit({ root: e.target.value })} />
        <Disclosure summary="Advanced" defaultOpen={!!(start?.port || start?.sshOptions)}>
          <div className="xfer-dialog-fields">
            <Input label="Port" mono value={d.port} placeholder="22" inputMode="numeric" autoComplete="off"
              description="Only when the ssh config does not give it." error={problems.port}
              onChange={e => edit({ port: e.target.value })} />
            <Input label="ssh options" mono value={d.sshOptions} placeholder="-o IdentityFile=~/.ssh/archive_key" autoComplete="off" spellCheck={false}
              description="Passed to ssh for this location." error={problems.sshOptions}
              onChange={e => edit({ sshOptions: e.target.value })} />
          </div>
        </Disclosure>
        {busy === 'test' && <p className="muted"><Loader inline size={14} label="Testing the connection…" /></p>}
        {result && <TestResult r={result} />}
        {error && <Callout tone="error" size="sm" live>{error}</Callout>}
      </div>
    </Modal>
  )
}

/** A test's checks, each on a line, or the reason it failed. */
export function TestResult({ r }: { r: SettingTest }) {
  if (!r.ok) {
    return (
      <Callout tone="error" size="sm" live title="Test failed">
        {r.why ?? 'No reason was given.'} You can fix the fields and test again, or save it anyway.
      </Callout>
    )
  }
  const checks: [string, boolean | undefined][] = [
    [r.sftpOnly ? 'Reached over SFTP' : 'Reached over ssh', true],
    ['Folder exists', true],
    ['Writable', r.writable],
    [r.rsync && r.rsyncVersion ? `rsync ${r.rsyncVersion} there` : 'rsync there', r.sftpOnly ? undefined : r.rsync],
  ]
  return (
    <Callout tone={r.why ? 'warning' : 'success'} size="sm" live title="Connection works">
      <ul className="xfer-checks">
        {checks.filter(([, v]) => v !== undefined).map(([k, v]) => <li key={k} className={v ? 'yes' : 'no'}>{v ? '✓' : '✕'} {k}{!v && k.startsWith('rsync') ? ', Studio copy is used' : ''}</li>)}
        {r.freeBytes !== undefined && <li>{bytesText(r.freeBytes)} free</li>}
        {r.roundTripMs !== undefined && <li>{Math.round(r.roundTripMs)} ms round trip</li>}
        {r.rclone !== undefined && <li>{r.rclone ? `rclone${r.rcloneVersion ? ` ${r.rcloneVersion}` : ''} there` : 'No rclone there'} (for information; transfers do not run rclone on hosts)</li>}
      </ul>
      {r.sftpOnly && <p className="xfer-checks-note">This account takes SFTP only. Copies use Studio copy over SFTP; rsync is not available there.</p>}
      {r.rsync && r.rsyncText && <p className="xfer-checks-note">{r.rsyncText}.</p>}
      {r.why && <p className="xfer-checks-note">{r.why.charAt(0).toUpperCase() + r.why.slice(1)}.</p>}
    </Callout>
  )
}
