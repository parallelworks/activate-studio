import { useEffect, useState } from 'react'
import { Button, Callout, Field, Input, SegmentedControl } from '@parallelworks/ui'
import type { PwSystems, UserLibrary } from '../api'
import { BucketLibraryForm } from './BucketLibraryForm'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { LibraryImport } from './LibraryImport'
import { LocalLibraryForm } from './LocalLibraryForm'
import { PwSystemField } from './PwSystemField'
import { SshLibraryForm } from './SshLibraryForm'
import { SharedWithMe } from './SharedWithMe'
import { SpaceForm } from './SpaceForm'
import { accessLabel, knownAppConfig } from '../config'

/**
 * Add a library, in one model: where it is, whose account reads it, and
 * how it is indexed (docs/LIBRARIES.md, What a library carries, access).
 *
 * - Where: this computer (sign-in off: with sign-in on, the libraries here
 *   are the deployment's), a system (a cluster reached over pw ssh, a host
 *   over plain ssh on a Studio one person uses, or a cluster through the
 *   platform's index workflow), or a bucket of an rclone remote.
 * - Read as: on a system, always the viewer, with their own sign-in or ssh
 *   keys, so it holds what their account can read there; here and in a
 *   bucket, the account the Studio runs as. The form says which.
 * - What: on a system, a folder of the viewer's own (My library, read and
 *   written as them, closed to others), an index the Studio builds of a
 *   folder there (written as them where their account may write), or an
 *   index that is there already, connected as it is and read only. Here,
 *   a folder to build or an index to connect, read only.
 *
 * The system is chosen once, at the top, and the forms below take it. On a
 * cluster the viewer reaches as themselves, the folders others shared with
 * them there are listed too. A copy of a folder in a Transfers location is
 * made from Transfers ("Keep a local copy and index it"), where those
 * locations are browsed; the form points there where copies are offered.
 */

export type Where = 'here' | 'system' | 'bucket'
export type How = 'own' | 'build' | 'connect'
export type Via = 'pw' | 'ssh' | 'workflow'

export interface AddOptions {
  /** Libraries can be built from folders on this computer (sign-in off). */
  buildHere: boolean
  /** Hosts over plain ssh (sign-in off). */
  ssh: boolean
  /** Over pw ssh: offered here, though the form may say it cannot be used
   *  yet (no pw CLI found, or not signed in). */
  pw: boolean
  /** The platform's index workflow: a hosted Studio that reaches a platform. */
  workflow: boolean
  /** A copy of a folder in a Transfers location (sign-in off), made from
   *  Transfers; the form points there. */
  copy?: boolean
  /** A bucket of an rclone remote, indexed here from its listing (sign-in off). */
  bucket?: boolean
  /** A folder of the viewer's own on one of their systems (My library), and
   *  the folders shared with them there. */
  space?: boolean
  /** The Studio's own cluster, offered first among the viewer's systems. */
  home?: string
}

/** What can be chosen, given what this Studio offers. `how` takes whether
 *  the system chosen is a host over plain ssh. */
export function addChoices(o: AddOptions) {
  const onSystem = o.ssh || o.pw || o.workflow || !!o.space
  const where: Where[] = [...(o.buildHere ? ['here' as const] : []), ...(onSystem ? ['system' as const] : []), ...(o.bucket ? ['bucket' as const] : [])]
  const how = (w: Where, overSSH = false): How[] => {
    if (w === 'bucket') return []
    if (w === 'here') return [...(o.buildHere ? ['build' as const] : []), 'connect']
    if (overSSH) return [...(o.space ? ['own' as const] : []), 'build', 'connect']
    return [...(o.space ? ['own' as const] : []), ...(o.pw || o.workflow ? ['build' as const] : []), ...(o.pw ? ['connect' as const] : [])]
  }
  /** How a folder on a cluster is built: over the pw ssh session, or as a
   *  job of the platform's index workflow. */
  const builders = (): Via[] => [...(o.pw ? ['pw' as const] : []), ...(o.workflow ? ['workflow' as const] : [])]
  return { where, how, builders }
}

const WHERE_LABEL: Record<Where, string> = { here: 'This computer', system: 'A system', bucket: 'A bucket' }
const HOW_LABEL: Record<How, string> = { own: 'My own folder', build: 'Index a folder', connect: 'Connect an index' }
const BUILDER_LABEL: Record<'pw' | 'workflow', string> = { pw: 'pw ssh', workflow: 'Workflow job' }

/** The choice in the System list for a host over plain ssh. */
const SSH_HOST = '\u0000ssh'

function describe(where: Where, how: How | undefined, overSSH: boolean, builder: Via | undefined): string {
  if (where === 'bucket') return 'A bucket, or a folder in one, of an rclone remote: any S3 compatible store, AWS or on premises (MinIO, Ceph and the like), or another store rclone lists. Read with the Studio\'s account, from its listing, for finding and searching by name, size and time; an object is read only when you open it. Read only.'
  if (where === 'here') {
    return how === 'build'
      ? 'A folder on this computer, read with the Studio\'s account, indexed with GUFI and kept in step with the folder as it changes. Read only.'
      : 'A GUFI index already built on this computer, read with the Studio\'s account. Read only: the Studio searches it and never writes to it.'
  }
  if (how === 'own') return 'A new folder in your home on that system, made as your own account and closed to other accounts, indexed there as you. Read and written as you: you can add, move and delete files in it from the Library, and share folders in it with other accounts.'
  if (how === 'connect') return 'A GUFI index already on that system, read as you. Read only: nothing is written there.'
  if (builder === 'workflow') return 'A folder on that cluster, indexed there by the platform\'s index workflow with your own key, on the login node or as a Slurm job. Read as you, read only.'
  return `A folder on that system, indexed there with GUFI ${overSSH ? 'over ssh' : 'over pw ssh'} as you. The build reads the text of its files where it can and leaves the folder as it is. Read and written as you where your account may write there.`
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
  const [wherePicked, setWhere] = useState<Where | null>(null)
  const where: Where = wherePicked && choices.where.includes(wherePicked) ? wherePicked : (choices.where[0] ?? 'here')

  // The system: one of the viewer's clusters, or a host over plain ssh.
  const [cluster, setCluster] = useState('')
  const [host, setHost] = useState('')
  const pickSystems = options.pw || !!options.space
  const [overSSHPicked, setOverSSH] = useState(false)
  const overSSH = options.ssh && (overSSHPicked || !pickSystems)
  const [systems, setSystems] = useState<PwSystems | null>(null)
  const system = overSSH ? host.trim() : cluster

  const hows = choices.how(where, overSSH)
  // Until a How is picked it follows the first offered: building here is
  // offered once the libraries answer, after the form first shows.
  const [howPicked, setHow] = useState<How | null>(null)
  const how = howPicked && hows.includes(howPicked) ? howPicked : hows[0]
  const builders = overSSH || how !== 'build' ? [] : choices.builders()
  const [builderPicked, setBuilder] = useState<Via | null>(null)
  const builder = overSSH ? 'ssh' : builderPicked && builders.includes(builderPicked) ? builderPicked : builders[0]
  useEffect(() => { if (options.home && !cluster) setCluster(options.home) }, [options.home, cluster])

  if (importing) {
    return (
      <div className="lib-add-flow">
        <div><Button variant="ghost" size="sm" onClick={() => setImporting(false)}>Back to adding one library</Button></div>
        <p className="muted lib-add-about">A library list exported from another Studio, such as one your administrator shares. Each library is checked and added as this form adds it.</p>
        <LibraryImport onDone={n => onImported?.(n)} />
      </div>
    )
  }

  // The workflow alone reaches the clusters, and lists them itself.
  const workflowOnly = where === 'system' && !pickSystems && !options.ssh
  const pwBlocked = !overSSH && options.pw && systems && !systems.usable
  const asYou = overSSH ? 'your own ssh keys, agent and ~/.ssh/config' : 'your ACTIVATE sign-in'
  const systemChoice = where === 'system' && !workflowOnly && (
    <div className="settings-grid lib-add-system">
      {pickSystems && (
        <PwSystemField value={cluster} preferred={options.home} onChange={c => { setCluster(c); setOverSSH(false) }} onSystems={setSystems}
          extra={options.ssh ? [{ value: SSH_HOST, label: 'A host over ssh…', description: 'user@host, with your own keys' }] : []}
          extraValue={overSSH ? SSH_HOST : null} onExtra={() => setOverSSH(true)} />
      )}
      {!overSSH && systems?.error && (
        <Callout tone="warning">Your systems could not be listed: {systems.error.replace(/[.\s]*$/, '.')} Type the name of a running system instead.</Callout>
      )}
      {overSSH && (
        <Input label="Host" placeholder="user@host or user@host:port" autoComplete="off" spellCheck={false} value={host}
          description="As you would type it after ssh, with :port for a port other than 22. A Host from your ~/.ssh/config works, including ProxyJump."
          onChange={e => setHost(e.target.value)} />
      )}
    </div>
  )

  return (
    <div className="lib-add-flow">
      {onImported && <div><Button variant="ghost" size="sm" onClick={() => setImporting(true)}>Import a library list from a file…</Button></div>}
      <div className="lib-add-choices">
        {choices.where.length > 1 && (
          <Field label="Where">
            <SegmentedControl label="Where" value={where} onValueChange={v => setWhere(v)} options={choices.where.map(w => ({ value: w, label: WHERE_LABEL[w] }))} />
          </Field>
        )}
      </div>
      {systemChoice}
      {where === 'system' && !workflowOnly && (
        <p className="muted lib-add-as" data-testid="read-as">
          Read as you{system ? ` on ${system}` : ''}, with {asYou}, never the Studio&apos;s own account.
        </p>
      )}
      {pwBlocked ? (
        <Callout tone="warning" title="pw ssh cannot be used yet"
          action={<Button variant="secondary" size="sm" onClick={() => void (location.hash = '#view=settings:access')}>Open {accessLabel(knownAppConfig())}</Button>}>
          {systems?.why}
        </Callout>
      ) : (
        <>
          <div className="lib-add-choices">
            {hows.length > 1 && (
              <Field label="What">
                <SegmentedControl label="What" value={how} onValueChange={v => setHow(v)} options={hows.map(h => ({ value: h, label: HOW_LABEL[h] }))} />
              </Field>
            )}
            {builders.length > 1 && (
              <Field label="Build with">
                <SegmentedControl label="Build with" value={builder} onValueChange={v => setBuilder(v)}
                  options={builders.map(b => ({ value: b, label: BUILDER_LABEL[b as 'pw' | 'workflow'] }))} />
              </Field>
            )}
          </div>
          <p className="muted lib-add-about">{describe(where, how, overSSH, builder)}</p>
          {where === 'bucket' && <BucketLibraryForm onAdded={lib => onAdded(lib, true, true)} />}
          {where === 'here' && (how === 'build'
            ? <LocalLibraryForm key="here-build" mode="build" compact browse={browse} onAdded={lib => onAdded(lib, true, true)} />
            : <LocalLibraryForm key="here-connect" compact browse={browse} onAdded={lib => onAdded(lib, false)} />)}
          {where === 'system' && how === 'own' && (
            <SpaceForm key={`own-${overSSH ? 'ssh' : 'pw'}`} {...(overSSH ? { destination: system } : { cluster: system })} onAdded={lib => onAdded(lib, true)} />
          )}
          {where === 'system' && how === 'build' && builder === 'workflow' && (
            <ClusterLibraryForm compact {...(workflowOnly ? {} : { site: system })} onStarted={lib => onStarted?.(lib)} />
          )}
          {where === 'system' && (how === 'build' || how === 'connect') && builder !== 'workflow' && (
            <SshLibraryForm key={`${overSSH ? 'ssh' : 'pw'}-${how}`} compact transport={overSSH ? 'ssh' : 'pw'} mode={how === 'build' ? 'build' : 'connect'}
              {...(overSSH ? { destination: system } : { cluster: system })}
              onAdded={lib => onAdded(lib, how === 'build')} />
          )}
          {where === 'system' && !overSSH && options.space && system && (
            <section className="lib-add-shared" aria-label={`Shared with you on ${system}`}>
              <SharedWithMe compact system={system} title={`Shared with you on ${system}`} onAdded={lib => onAdded(lib, true)} />
            </section>
          )}
          {where === 'system' && options.copy && (
            <p className="muted lib-add-about">
              A host that takes SFTP only, or a cloud drive: <a href="#view=transfers">Transfers</a> keeps a local copy of a folder there and indexes it.
            </p>
          )}
        </>
      )}
    </div>
  )
}
