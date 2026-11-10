import { useState } from 'react'
import { Button, Callout, Checkbox, FileButton, Input, StatusLabel, Textarea } from '@parallelworks/ui'
import { type CheckedEntry, type ImportResult, type ListCheck, checkList, destinationWith, importEntry, kindLabel } from '../libraryLists'

const errText = (e: unknown) => String((e as Error)?.message ?? e)

const STATE_TEXT: Record<CheckedEntry['state'], string> = {
  new: 'Not added yet', present: 'Already added', invalid: 'Cannot be read', unavailable: 'Cannot be added here',
}
const STATE_TONE = { new: 'info', present: 'success', invalid: 'error', unavailable: 'warning' } as const
const RESULT_TONE = { added: 'success', failed: 'error', skipped: 'info' } as const
const RESULT_TEXT = { added: 'Added', failed: 'Failed', skipped: 'Skipped' } as const

/**
 * Import a library list: a file an administrator exported, chosen or
 * pasted. Each entry is shown with its kind, label and where it points, and
 * a tick; one that is already a library here, cannot be read, or cannot be
 * added on this Studio is shown with the reason and no tick. An entry over
 * ssh has a user name to connect as, the login name here when the file
 * names none. Import adds each ticked entry in turn through the Add a
 * library routes, and says for each one whether it was added, failed and
 * why, or was skipped.
 */
export function LibraryImport({ onAdded, onDone }: {
  /** One library was added. */
  onAdded?: (lib: { id: string; label: string }) => void
  /** The import finished; `added` is how many were added. */
  onDone?: (added: number) => void
}) {
  const [pasting, setPasting] = useState(false)
  const [text, setText] = useState('')
  const [checking, setChecking] = useState(false)
  const [error, setError] = useState('')
  const [check, setCheck] = useState<ListCheck | null>(null)
  const [ticked, setTicked] = useState<Set<number>>(new Set())
  const [users, setUsers] = useState<Record<number, string>>({})
  const [running, setRunning] = useState(false)
  const [results, setResults] = useState<Record<number, ImportResult>>({})

  const read = async (t: string) => {
    setChecking(true); setError(''); setCheck(null); setResults({})
    try {
      const c = await checkList(t)
      setCheck(c)
      setTicked(new Set(c.entries.filter(e => e.state === 'new').map(e => e.index)))
      const u: Record<number, string> = {}
      for (const e of c.entries) if (e.entry?.kind === 'ssh') u[e.index] = e.user || c.localUser || ''
      setUsers(u)
    } catch (e) { setError(errText(e)) } finally { setChecking(false) }
  }
  const onFiles = (files: File[]) => { void files[0]?.text().then(read, e => setError(errText(e))) }

  const toggle = (i: number, on: boolean) => setTicked(t => { const n = new Set(t); if (on) n.add(i); else n.delete(i); return n })
  const run = async () => {
    if (!check) return
    setRunning(true)
    let added = 0
    for (const c of check.entries) {
      if (!ticked.has(c.index) || !c.entry) {
        if (c.state === 'new') setResults(r => ({ ...r, [c.index]: { state: 'skipped', text: 'Not ticked.' } }))
        continue
      }
      const r = await importEntry(c.entry, c.entry.kind === 'ssh' ? users[c.index] ?? '' : undefined)
      setResults(rs => ({ ...rs, [c.index]: r }))
      if (r.state === 'added' && r.id) { added++; onAdded?.({ id: r.id, label: r.label ?? c.entry.label ?? r.id }) }
    }
    setTicked(new Set())
    setRunning(false)
    onDone?.(added)
  }

  const done = Object.keys(results).length > 0
  const count = ticked.size
  return (
    <div className="lib-import">
      <div className="lib-import-source">
        <FileButton variant="secondary" size="sm" accept=".json,application/json" loading={checking} disabled={running} onFiles={onFiles}>Choose a file…</FileButton>
        <Button variant="ghost" size="sm" aria-expanded={pasting} disabled={running} onClick={() => setPasting(p => !p)}>{pasting ? 'Hide the text box' : 'Paste the list instead'}</Button>
      </div>
      {pasting && (
        <div className="lib-import-paste">
          <Textarea label="Library list" mono rows={6} spellCheck={false} value={text} onChange={e => setText(e.target.value)}
            description="The contents of a .studio-libraries.json file." />
          <div><Button variant="secondary" size="sm" disabled={!text.trim() || checking || running} loading={checking} onClick={() => void read(text)}>Read the list</Button></div>
        </div>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
      {check && (
        <>
          <p className="muted" style={{ margin: 0 }}>
            {check.entries.length === 1 ? 'One library' : `${check.entries.length} libraries`} in this list
            {check.exportedAt ? `, exported ${new Date(check.exportedAt).toLocaleString()}` : ''}
            {check.studioVersion ? ` from Studio ${check.studioVersion}` : ''}.
          </p>
          <ul className="lib-import-list" aria-label="Libraries in the list">
            {check.entries.map(c => {
              const e = c.entry
              const res = results[c.index]
              const can = c.state === 'new' && !res
              const name = e?.label || `Entry ${c.index + 1}`
              return (
                <li key={c.index} className="lib-import-item">
                  <Checkbox aria-label={`Import ${name}`} checked={ticked.has(c.index)} disabled={!can || running} onCheckedChange={v => toggle(c.index, !!v)} />
                  <div className="lib-import-body">
                    <div className="lib-name">{name}</div>
                    <div className="lib-id">{kindLabel(e)}</div>
                    {c.where && <code className="lib-import-where">{c.where}</code>}
                    {e?.kind === 'ssh' && can && (
                      <Input label="Connect as" size="sm" mono spellCheck={false} value={users[c.index] ?? ''} disabled={running}
                        placeholder="user name"
                        description={`Connects as ${destinationWith(users[c.index] ?? '', c.host ?? '')}. Your own login, so your permissions apply there.`}
                        onChange={ev => setUsers(u => ({ ...u, [c.index]: ev.target.value }))} />
                    )}
                    {res
                      ? <div><StatusLabel variant={RESULT_TONE[res.state]}>{RESULT_TEXT[res.state]}</StatusLabel> <span className="lib-id">{res.text}</span></div>
                      : (
                        <div>
                          <StatusLabel variant={STATE_TONE[c.state]}>{STATE_TEXT[c.state]}</StatusLabel>
                          {c.why && <span className="lib-id"> {c.state === 'present' ? `as ${c.why}` : c.why}</span>}
                        </div>
                      )}
                  </div>
                </li>
              )
            })}
          </ul>
          {!done || count > 0
            ? (
              <div>
                <Button variant="primary" disabled={!count || running} loading={running} onClick={() => void run()}>
                  {count === 1 ? 'Add 1 library' : `Add ${count} libraries`}
                </Button>
              </div>
            )
            : <p className="muted" style={{ margin: 0 }}>Done. Libraries being built show their progress in Settings, Indexing, Libraries.</p>}
        </>
      )}
    </div>
  )
}
