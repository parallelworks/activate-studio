import { useEffect, useMemo, useState } from 'react'
import { Button, Callout, Field, Input, Modal, Select } from '@parallelworks/ui'

/**
 * Agent instructions and skills, applied to a folder: the Studio writes
 * them into the folder's AGENTS.md (and .agents/skills), which pw code
 * reads in every session that works there (POST /api/agents-md/apply).
 */

/** A folder with what the Studio has put there (GET /api/agents-md/folders). */
export interface AgentFolder {
  library: string
  label: string
  folder: string
  path: string
  space: boolean
  exists: boolean
  instructions: string[]
  skills: string[]
  problem?: string
}

interface LibraryChoice { id: string; label: string; primary: boolean; writable: boolean; space?: boolean; sharedFrom?: string; site?: string | null }

/** The libraries instructions can go into: the main library when it is
 *  writable here, and the person's own library. */
export function instructionLibraries(libs: LibraryChoice[]): LibraryChoice[] {
  return libs.filter(l => (l.primary && l.writable && !l.site) || (l.space && !l.sharedFrom))
}

async function postJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const d = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error(d.error ?? d.message ?? `${res.status}`)
  return d as T
}

/** Applies (or, with remove, takes off) instructions and skills in a folder. */
export function changeFolder(remove: boolean, library: string, folder: string, what: { instructions?: string; skills?: string[] }) {
  return postJson<AgentFolder>(`/api/agents-md/${remove ? 'remove' : 'apply'}`, { library, folder, ...what })
}

export function ApplyToFolder({ kind, name, onClose, onDone }: {
  kind: 'persona' | 'skill'
  name: string
  onClose: () => void
  onDone: (f: AgentFolder) => void
}) {
  const [libs, setLibs] = useState<LibraryChoice[] | null>(null)
  const [library, setLibrary] = useState('')
  const [folder, setFolder] = useState(`agents/${name}`)
  const [dirs, setDirs] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    fetch('/api/libraries').then(r => r.json()).then(d => {
      const choices = instructionLibraries(d.libraries ?? [])
      setLibs(choices)
      const main = choices.find(l => l.primary)
      setLibrary((main ?? choices[0])?.id ?? '')
    }).catch(() => setLibs([]))
  }, [])
  const chosen = libs?.find(l => l.id === library)
  useEffect(() => {
    // The main library's folders are suggested as the person types.
    if (!chosen?.primary) { setDirs([]); return }
    fetch('/api/kb/dirs').then(r => r.json()).then(d => setDirs(d.dirs ?? [])).catch(() => setDirs([]))
  }, [chosen?.primary])
  const items = useMemo(() => (libs ?? []).map(l => ({ value: l.id, label: l.space ? `${l.label} (yours)` : l.label })), [libs])
  const apply = async () => {
    setBusy(true); setError('')
    try {
      const f = await changeFolder(false, chosen?.primary ? '' : library, folder,
        kind === 'persona' ? { instructions: name } : { skills: [name] })
      onDone(f)
    } catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  const what = kind === 'persona' ? `the instructions ${name}` : `the skill ${name}`
  return (
    <Modal open onClose={onClose} size="md" title={`Apply ${name} to a folder`}
      description={kind === 'persona'
        ? <>Studio writes {what} into the folder's <code>AGENTS.md</code>, in a section of their own; pw code reads that file in every session working there. Your own text in the file stays as it is.</>
        : <>Studio writes {what} to <code>.agents/skills/{name}/SKILL.md</code> in the folder, where pw code finds a workspace's skills once you have opened the folder in pw code and approved it.</>}
      onSubmit={() => void apply()}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" type="submit" loading={busy} disabled={!libs?.length}>Apply</Button>
      </>}>
      {libs && libs.length === 0 && (
        <Callout tone="warning" size="sm">No library here takes agent instructions: they go into the main library when it can be written, or into My library.</Callout>
      )}
      {libs && libs.length > 1 && (
        <Field label="Library">
          {control => <Select<string> {...control} aria-label="Library" items={items} value={library} onValueChange={v => v && setLibrary(v)} />}
        </Field>
      )}
      <Input label="Folder" value={folder} onChange={e => setFolder(e.target.value)} list="agents-md-dirs"
        description="A folder in the library, from its top. It is made when it is not there; leave it empty for the top." />
      <datalist id="agents-md-dirs">{dirs.slice(0, 500).map(d => <option key={d} value={d} />)}</datalist>
      {error && <Callout tone="error" size="sm">{error}</Callout>}
    </Modal>
  )
}
