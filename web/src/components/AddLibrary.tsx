import { useEffect, useState } from 'react'
import { Button, Field, SegmentedControl } from '@parallelworks/ui'
import type { UserLibrary } from '../api'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { LibraryImport } from './LibraryImport'
import { LocalLibraryForm } from './LocalLibraryForm'
import { SshLibraryForm } from './SshLibraryForm'

/**
 * Add a library, as two choices: where the folder or index is (this
 * computer, or a remote system) and how (build an index of a folder, or
 * connect a GUFI index that exists). A remote system is reached over ssh
 * or over pw ssh, a choice shown side by side. Building through the platform's index workflow is a
 * third way only on a Studio the platform hosts (`workflow`); a Studio on
 * someone's own computer builds on a remote system over the session.
 */

export type Where = 'here' | 'remote'
export type How = 'build' | 'connect'
export type Via = 'ssh' | 'pw' | 'workflow'

export interface AddOptions {
  /** Libraries can be built from folders on this computer (sign-in off). */
  buildHere: boolean
  /** Remote systems over ssh (sign-in off). */
  ssh: boolean
  /** Over pw ssh: offered here, though the form may say it cannot be used
   *  yet (no pw CLI found, or not signed in). */
  pw: boolean
  /** The platform's index workflow: a hosted Studio that reaches a platform. */
  workflow: boolean
}

/** What can be chosen, given what this Studio offers. */
export function addChoices(o: AddOptions) {
  const where: Where[] = ['here', ...(o.ssh || o.pw || o.workflow ? ['remote' as const] : [])]
  const how = (w: Where): How[] => (w === 'here'
    ? [...(o.buildHere ? ['build' as const] : []), 'connect']
    : [...(o.ssh || o.pw || o.workflow ? ['build' as const] : []), ...(o.ssh || o.pw ? ['connect' as const] : [])])
  const via = (w: Where, h: How): Via[] => (w === 'here' ? [] : [
    ...(o.ssh ? ['ssh' as const] : []), ...(o.pw ? ['pw' as const] : []), ...(h === 'build' && o.workflow ? ['workflow' as const] : []),
  ])
  return { where, how, via }
}

const WHERE_LABEL: Record<Where, string> = { here: 'This computer', remote: 'A remote system' }
const HOW_LABEL: Record<How, string> = { build: 'Build an index of a folder', connect: 'Connect an existing index' }
const VIA_LABEL: Record<Via, string> = { ssh: 'SSH', pw: 'ACTIVATE (pw ssh)', workflow: 'Platform workflow' }

function describe(where: Where, how: How, via: Via | undefined): string {
  if (where === 'here') {
    return how === 'build'
      ? 'A folder on this computer, indexed with GUFI and kept in step with the folder as it changes. Read only in the Studio.'
      : 'A GUFI index already built on this computer. Read only: the Studio searches it and never writes to it.'
  }
  if (via === 'workflow') return 'A folder on one of your clusters, indexed there by the platform\'s index workflow with your own key, on the login node or as a Slurm job. Only you see it.'
  const over = via === 'pw' ? 'over pw ssh with this computer\'s pw sign-in' : 'over ssh with your own keys (no password prompts)'
  return how === 'build'
    ? `A folder on a remote system, indexed there with GUFI ${over}. The build runs there, reads the text of its files where it can, and leaves the folder as it is.`
    : `A GUFI index already on a remote system, read ${over}. Read only: nothing is written there.`
}

export function AddLibrary({ options, browse = false, onAdded, onStarted, onImported }: {
  options: AddOptions
  /** Folders on this computer can be picked from a list (a single-user install). */
  browse?: boolean
  /** A library was added, or its build started (`started`); `here` is a
   *  build on this computer, searchable by name while its text and meaning
   *  are read. */
  onAdded: (lib: { id: string; label: string }, started: boolean, here?: boolean) => void
  /** A build through the platform's index workflow started. */
  onStarted?: (lib: UserLibrary) => void
  /** Offers importing a library list in place of the form; called with
   *  how many were added once the import is done. */
  onImported?: (added: number) => void
}) {
  const [importing, setImporting] = useState(false)
  const choices = addChoices(options)
  const [where, setWhere] = useState<Where>('here')
  const hows = choices.how(where)
  // Until a How is picked it follows the first offered: building here is
  // offered once the libraries answer, after the form first shows.
  const [howPicked, setHow] = useState<How | null>(null)
  const how = howPicked && hows.includes(howPicked) ? howPicked : hows[0]
  const vias = choices.via(where, how)
  const [via, setVia] = useState<Via | undefined>(vias[0])
  useEffect(() => { if (vias.length ? !via || !vias.includes(via) : via) setVia(vias[0]) }, [vias.join(), via])
  const v = vias.includes(via as Via) ? via : vias[0]

  if (importing) {
    return (
      <div className="lib-add-flow">
        <div><Button variant="ghost" size="sm" onClick={() => setImporting(false)}>Back to adding one library</Button></div>
        <p className="muted lib-add-about">A library list exported from another Studio, such as one your administrator shares. Each library is checked and added as this form adds it.</p>
        <LibraryImport onDone={n => onImported?.(n)} />
      </div>
    )
  }

  return (
    <div className="lib-add-flow">
      {onImported && <div><Button variant="ghost" size="sm" onClick={() => setImporting(true)}>Import a library list from a file…</Button></div>}
      <div className="lib-add-choices">
        {choices.where.length > 1 && (
          <Field label="Where">
            <SegmentedControl label="Where" value={where} onValueChange={setWhere} options={choices.where.map(w => ({ value: w, label: WHERE_LABEL[w] }))} />
          </Field>
        )}
        {hows.length > 1 && (
          <Field label="How">
            <SegmentedControl label="How" value={how} onValueChange={setHow} options={hows.map(h => ({ value: h, label: HOW_LABEL[h] }))} />
          </Field>
        )}
        {where === 'remote' && vias.length > 1 && (
          <Field label="Connect with">
            <SegmentedControl label="Connect with" value={v!} onValueChange={setVia} options={vias.map(x => ({ value: x, label: VIA_LABEL[x] }))} />
          </Field>
        )}
      </div>
      <p className="muted lib-add-about">{describe(where, how, v)}</p>
      {where === 'here' && (how === 'build'
        ? <LocalLibraryForm key="here-build" mode="build" compact browse={browse} onAdded={lib => onAdded(lib, true, true)} />
        : <LocalLibraryForm key="here-connect" compact browse={browse} onAdded={lib => onAdded(lib, false)} />)}
      {where === 'remote' && v === 'workflow' && <ClusterLibraryForm compact onStarted={lib => onStarted?.(lib)} />}
      {where === 'remote' && (v === 'ssh' || v === 'pw') && (
        <SshLibraryForm key={`${v}-${how}`} compact transport={v} mode={how === 'build' ? 'build' : 'connect'}
          onAdded={lib => onAdded(lib, how === 'build')} />
      )}
    </div>
  )
}
