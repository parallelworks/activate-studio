import { useEffect, useState } from 'react'
import { Button, Callout, Checkbox, Modal, toast } from '@parallelworks/ui'
import type { SettingsLibrary, UserLibrary } from '../api'
import { exportLibraries } from '../libraryLists'
import { saveText } from '../tableExport'

const errText = (e: unknown) => String((e as Error)?.message ?? e)

/** A library built by the platform's workflow is the person's own and is
 *  not exported. */
const workflowBuilt = (l: UserLibrary) => l.transport !== 'ssh' && !l.built && !l.existing

/**
 * Export libraries as a library list, a file others import into their own
 * Studio: every library or the ones ticked, the main folder only when it is
 * ticked. Over ssh, the user name is left out unless that is turned off, so
 * each person connects as themselves. A library this Studio built goes as
 * its folder, for each person's Studio to index, unless its index is shared
 * as it is (an administrator's root-built index, searched by each person as
 * themselves). The file is saved as a download, the way an exported table is.
 */
export function LibraryExport({ open, onClose, libraries, userLibraries }: {
  open: boolean
  onClose: () => void
  libraries: SettingsLibrary[]
  userLibraries: UserLibrary[]
}) {
  const rows = [
    ...libraries.map(l => ({ id: l.id, label: l.label, primary: l.primary, note: l.primary ? 'Main folder, exported as a folder to index' : '', ssh: !!l.readThrough, built: !l.primary && !!l.build })),
    ...userLibraries.map(l => ({ id: l.id, label: l.label, primary: false, note: workflowBuilt(l) ? 'Built by the platform workflow for you alone, so it is not exported' : '', ssh: l.transport === 'ssh' || !!l.readThrough, built: !!l.built })),
  ]
  const [ticked, setTicked] = useState<Set<string>>(new Set())
  const [leaveOutUser, setLeaveOutUser] = useState(true)
  const [shareBuilt, setShareBuilt] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!open) return
    setTicked(new Set(rows.filter(r => !r.primary && !r.note).map(r => r.id)))
    setShareBuilt(false)
    setError('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])
  const toggle = (id: string, on: boolean) => setTicked(t => { const n = new Set(t); if (on) n.add(id); else n.delete(id); return n })
  const primary = rows.find(r => r.primary)
  const anySsh = rows.some(r => r.ssh && ticked.has(r.id))
  const anyBuilt = rows.some(r => r.built && ticked.has(r.id))

  const save = async () => {
    setBusy(true); setError('')
    try {
      const ids = [...ticked]
      const r = await exportLibraries({ ids, primary: !!primary && ticked.has(primary.id), keepUser: !leaveOutUser, shareBuilt: anyBuilt && shareBuilt })
      saveText(r.name, JSON.stringify(r.file, null, 2) + '\n', 'application/json')
      for (const s of r.skipped) toast.warning(`${s.label} is not in the file: ${s.why}`)
      onClose()
    } catch (e) { setError(errText(e)) } finally { setBusy(false) }
  }

  return (
    <Modal open={open} onClose={() => { if (!busy) onClose() }} size="lg" title="Export libraries"
      description="A library list: where each library is and how it is built, for others to import into their own Studio. It holds no keys, passwords, tokens or settings."
      footer={(
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!ticked.size} onClick={() => void save()}>Export</Button>
        </>
      )}>
      <div className="lib-export">
        <ul className="lib-import-list" aria-label="Libraries to export">
          {rows.map(r => (
            <li key={r.id} className="lib-import-item">
              <Checkbox aria-label={`Export ${r.label}`} checked={ticked.has(r.id)} disabled={!!r.note && !r.primary} onCheckedChange={v => toggle(r.id, !!v)} />
              <div className="lib-import-body">
                <div className="lib-name">{r.label}</div>
                {r.note && <div className="lib-id">{r.note}</div>}
              </div>
            </li>
          ))}
        </ul>
        {anySsh && (
          <label className="lib-export-user">
            <Checkbox checked={leaveOutUser} onCheckedChange={v => setLeaveOutUser(!!v)} />
            <span>
              Leave out the user name
              <span className="lib-id">Writes the host without your user name, so each person connects as themselves and their own permissions apply.</span>
            </span>
          </label>
        )}
        {anyBuilt && (
          <label className="lib-export-user">
            <Checkbox checked={shareBuilt} onCheckedChange={v => setShareBuilt(!!v)} />
            <span>
              Share the indexes built here
              <span className="lib-id">Each person connects to this index and searches it as themselves, without indexing the folder again. They need to reach the index folder, and an index built as root keeps the source's owners and modes, so each person sees what their own permissions allow. Otherwise each person's Studio indexes the folder.</span>
            </span>
          </label>
        )}
        {error && <Callout tone="error" live>{error}</Callout>}
      </div>
    </Modal>
  )
}
