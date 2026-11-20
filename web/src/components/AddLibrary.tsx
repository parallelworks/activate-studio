import { useEffect, useState } from 'react'
import { Button, Field, SegmentedControl } from '@parallelworks/ui'
import type { UserLibrary } from '../api'
import { BucketLibraryForm } from './BucketLibraryForm'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { CopyLibraryForm } from './CopyLibraryForm'
import { LibraryImport } from './LibraryImport'
import { LocalLibraryForm } from './LocalLibraryForm'
import { SshLibraryForm } from './SshLibraryForm'
import { SharedWithMe } from './SharedWithMe'
import { SpaceForm } from './SpaceForm'

/**
 * Add a library, as two choices: where the folder or index is (this
 * computer, or a remote system) and how (build an index of a folder, or
 * connect a GUFI index that exists). A remote system is reached over ssh
 * or over pw ssh, a choice shown side by side. Building through the platform's index workflow is a
 * third way only on a Studio the platform hosts (`workflow`); a Studio on
 * someone's own computer builds on a remote system over the session. A
 * folder on a remote system can also be copied here and indexed (`copy`,
 * sign-in off): from a Transfers location, which reaches hosts that take
 * SFTP only and rclone remotes, where nothing can be built. Beside the
 * cluster it runs on, a Studio also offers a space of the viewer's own
 * (`space`) and the folders others shared with them (`shared`), both read
 * and written as them. A bucket of an rclone remote (`bucket`, sign-in
 * off) is indexed from its listing alone, its objects read when opened.
 */

export type Where = 'here' | 'remote' | 'bucket' | 'space' | 'shared'
export type How = 'build' | 'connect' | 'copy'
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
  /** A copy of a folder in a Transfers location, indexed here (sign-in off). */
  copy?: boolean
  /** A bucket of an rclone remote, indexed here from its listing (sign-in off). */
  bucket?: boolean
  /** A space of the viewer's own on the cluster the Studio runs beside, and
   *  folders shared with them there: offered wherever the Studio knows that
   *  cluster, the form saying why when one cannot be made yet. */
  space?: boolean
}

/** What can be chosen, given what this Studio offers. */
export function addChoices(o: AddOptions) {
  const where: Where[] = ['here', ...(o.ssh || o.pw || o.workflow || o.copy ? ['remote' as const] : []), ...(o.bucket ? ['bucket' as const] : []), ...(o.space ? ['space' as const, 'shared' as const] : [])]
  const how = (w: Where): How[] => (w === 'space' || w === 'shared' || w === 'bucket' ? [] : w === 'here'
    ? [...(o.buildHere ? ['build' as const] : []), 'connect']
    : [...(o.ssh || o.pw || o.workflow ? ['build' as const] : []), ...(o.ssh || o.pw ? ['connect' as const] : []), ...(o.copy ? ['copy' as const] : [])])
  const via = (w: Where, h: How): Via[] => (w !== 'remote' || h === 'copy' ? [] : [
    ...(o.ssh ? ['ssh' as const] : []), ...(o.pw ? ['pw' as const] : []), ...(h === 'build' && o.workflow ? ['workflow' as const] : []),
  ])
  return { where, how, via }
}

const WHERE_LABEL: Record<Where, string> = { here: 'This computer', remote: 'A remote system', bucket: 'A bucket', space: 'My library', shared: 'Shared with me' }
const HOW_LABEL: Record<How, string> = { build: 'Build an index of a folder', connect: 'Connect an existing index', copy: 'Copy here and index it' }
const VIA_LABEL: Record<Via, string> = { ssh: 'SSH', pw: 'ACTIVATE (pw ssh)', workflow: 'Platform workflow' }

function describe(where: Where, how: How | undefined, via: Via | undefined): string {
  if (where === 'bucket') return 'A bucket, or a folder in one, of an rclone remote: any S3 compatible store, AWS or on premises (MinIO, Ceph and the like), or another store rclone lists. Studio indexes its listing for finding and searching by name, size and time, and reads an object only when you open it. Read only in the Studio.'
  if (where === 'space') return 'A folder of your own on the cluster this Studio runs beside, reached with your ACTIVATE sign-in as your own account, closed to other accounts, and indexed there as you. You can add, move and delete files in it from the Library, and share folders in it with other accounts.'
  if (where === 'shared') return 'Folders other accounts on this Studio\'s cluster shared with you. Each is indexed there as you, so it holds what your account can read, and it can be written where the share allows.'
  if (where === 'here') {
    return how === 'build'
      ? 'A folder on this computer, indexed with GUFI and kept in step with the folder as it changes. Read only in the Studio.'
      : 'A GUFI index already built on this computer. Read only: the Studio searches it and never writes to it.'
  }
  if (how === 'copy') return 'A folder on an SFTP or ssh host, a cluster or an rclone remote, as Transfers reaches it: Studio copies it to this computer, indexes the copy, and looks for changes on an interval. The only way for a host that takes SFTP only. Read only in the Studio.'
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
      {where === 'space' && <SpaceForm onAdded={lib => onAdded(lib, true)} />}
      {where === 'shared' && <SharedWithMe onAdded={lib => onAdded(lib, true)} />}
      {where === 'bucket' && <BucketLibraryForm onAdded={lib => onAdded(lib, true, true)} />}
      {where === 'here' && (how === 'build'
        ? <LocalLibraryForm key="here-build" mode="build" compact browse={browse} onAdded={lib => onAdded(lib, true, true)} />
        : <LocalLibraryForm key="here-connect" compact browse={browse} onAdded={lib => onAdded(lib, false)} />)}
      {where === 'remote' && how === 'copy' && <CopyLibraryForm onAdded={lib => onAdded(lib, true, true)} />}
      {where === 'remote' && how !== 'copy' && v === 'workflow' && <ClusterLibraryForm compact onStarted={lib => onStarted?.(lib)} />}
      {where === 'remote' && how !== 'copy' && (v === 'ssh' || v === 'pw') && (
        <SshLibraryForm key={`${v}-${how}`} compact transport={v} mode={how === 'build' ? 'build' : 'connect'}
          onAdded={lib => onAdded(lib, how === 'build')} />
      )}
    </div>
  )
}
