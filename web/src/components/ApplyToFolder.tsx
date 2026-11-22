import { useEffect, useMemo, useState } from 'react'
import { Button, Callout, EmptyState, Field, Input, Modal, Select } from '@parallelworks/ui'
import { libraryNames } from '../libraryNames'
import { useAppConfig } from '../config'

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

const FOLDER_ICON = <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>

/** The folders of one library, one folder at a time (GET /api/kb/tree
 *  with the library), from its top. Opening a folder picks it. */
export function LibraryFolders({ library, libraryLabel, onPick }: {
  library: string
  libraryLabel: string
  onPick: (folder: string) => void
}) {
  const [at, setAt] = useState('')
  const [dirs, setDirs] = useState<{ name: string; path: string }[] | null>(null)
  const [error, setError] = useState('')
  useEffect(() => { setAt('') }, [library])
  useEffect(() => {
    let gone = false
    setDirs(null); setError('')
    fetch(`/api/kb/tree?path=${encodeURIComponent(at)}&library=${encodeURIComponent(library)}`)
      .then(async r => {
        const d = await r.json().catch(() => ({}))
        if (!r.ok) throw new Error(d.error ?? `${r.status}`)
        return (d.entries ?? []) as { name: string; path: string; type: string }[]
      })
      .then(es => { if (!gone) setDirs(es.filter(e => e.type === 'dir').sort((a, b) => a.name.localeCompare(b.name))) })
      .catch(e => { if (!gone) { setDirs([]); setError((e as Error).message) } })
    return () => { gone = true }
  }, [library, at])
  const open = (folder: string) => { setAt(folder); onPick(folder) }
  const parent = at.includes('/') ? at.slice(0, at.lastIndexOf('/')) : ''
  return (
    <div className="apply-folders" role="group" aria-label={`Folders in ${libraryLabel}`}>
      <div className="apply-folders-head">
        <span className="apply-folders-at" title={at || libraryLabel}>{at ? <code>{at}</code> : <>Top of {libraryLabel}</>}</span>
        <Button variant="ghost" size="sm" disabled={!at} onClick={() => open(parent)}>Up</Button>
      </div>
      {error && <Callout tone="error" size="sm">{error}</Callout>}
      <div className="apply-folders-list">
        {dirs === null ? <span className="muted">Reading…</span>
          : dirs.length ? dirs.map(d => (
            <Button key={d.path} variant="ghost" size="sm" className="apply-folders-item" icon={FOLDER_ICON} onClick={() => open(d.path)}>{d.name}</Button>
          )) : <EmptyState variant="bare" title="No folders inside" />}
      </div>
    </div>
  )
}

export function ApplyToFolder({ kind, name, onClose, onDone }: {
  kind: 'persona' | 'skill'
  name: string
  onClose: () => void
  onDone: (f: AgentFolder) => void
}) {
  const cfg = useAppConfig()
  const [libs, setLibs] = useState<LibraryChoice[] | null>(null)
  const [library, setLibrary] = useState('')
  const [folder, setFolder] = useState(`agents/${name}`)
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
        <Callout tone="warning" size="sm">No library here takes agent instructions: they go into {libraryNames(cfg).writable}, where you may write.</Callout>
      )}
      {libs && libs.length > 1 && (
        <Field label="Library">
          {control => <Select<string> {...control} aria-label="Library" items={items} value={library} onValueChange={v => v && setLibrary(v)} />}
        </Field>
      )}
      <Input label="Folder" value={folder} onChange={e => setFolder(e.target.value)}
        description="A folder in the library, from its top: open one below, or type it. It is made when it is not there; leave it empty for the top." />
      {chosen && <LibraryFolders library={chosen.id} libraryLabel={chosen.space ? `${chosen.label} (yours)` : chosen.label} onPick={setFolder} />}
      {error && <Callout tone="error" size="sm">{error}</Callout>}
    </Modal>
  )
}
