import { useEffect, useState } from 'react'
import { Field, SegmentedControl } from '@parallelworks/ui'
import type { UserLibrary } from '../api'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { LocalLibraryForm } from './LocalLibraryForm'
import { SshLibraryForm } from './SshLibraryForm'

/**
 * Add a library, as two choices: where the folder or index is (this
 * computer, or a remote system) and how (build an index of a folder, or
 * connect a GUFI index that exists). A remote system is reached over ssh
 * or over pw ssh. Building through the platform's index workflow is a
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
  /** Over pw ssh: the pw CLI is here, signed in. */
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
const VIA_LABEL: Record<Via, string> = { ssh: 'ssh', pw: 'pw ssh', workflow: 'Platform workflow' }

function describe(where: Where, how: How, via: Via | undefined): string {
  if (where === 'here') {
    return how === 'build'
      ? 'A folder on this computer, indexed with GUFI and kept in step with the folder as it changes. Read only in the Studio.'
      : 'A GUFI index already built on this computer. Read only: the Studio searches it and never writes to it.'
  }
  if (via === 'workflow') return 'A folder on one of your clusters, indexed there by the platform\'s index workflow with your own key, on the login node or as a Slurm job. Only you see it.'
  const over = via === 'pw' ? 'over pw ssh with this computer\'s pw sign-in' : 'over ssh with your own keys (no password prompts)'
  return how === 'build'
    ? `A folder on a remote system, indexed there with GUFI ${over}. The build runs there, with text extraction where Python is installed, and leaves the folder as it is.`
    : `A GUFI index already on a remote system, read ${over}. Read only: nothing is written there.`
}

export function AddLibrary({ options, browse = false, onAdded, onStarted }: {
  options: AddOptions
  /** Folders on this computer can be picked from a list (a single-user install). */
  browse?: boolean
  /** A library was added, or its build started. */
  onAdded: (lib: { id: string; label: string }, started: boolean) => void
  /** A build through the platform's index workflow started. */
  onStarted?: (lib: UserLibrary) => void
}) {
  const choices = addChoices(options)
  const [where, setWhere] = useState<Where>('here')
  const hows = choices.how(where)
  const [how, setHow] = useState<How>(hows[0])
  const vias = choices.via(where, how)
  const [via, setVia] = useState<Via | undefined>(vias[0])
  // A choice that is no longer offered falls back to the first that is.
  useEffect(() => { if (!hows.includes(how)) setHow(hows[0]) }, [hows.join(), how])
  useEffect(() => { if (vias.length ? !via || !vias.includes(via) : via) setVia(vias[0]) }, [vias.join(), via])
  const v = vias.includes(via as Via) ? via : vias[0]

  return (
    <div className="lib-add-flow">
      <div className="lib-add-choices">
        {choices.where.length > 1 && (
          <Field label="Where">
            <SegmentedControl label="Where" value={where} onValueChange={setWhere} options={choices.where.map(w => ({ value: w, label: WHERE_LABEL[w] }))} />
          </Field>
        )}
        {hows.length > 1 && (
          <Field label="How">
            <SegmentedControl label="How" value={hows.includes(how) ? how : hows[0]} onValueChange={setHow} options={hows.map(h => ({ value: h, label: HOW_LABEL[h] }))} />
          </Field>
        )}
        {where === 'remote' && vias.length > 1 && (
          <Field label="Connect with">
            <SegmentedControl label="Connect with" value={v!} onValueChange={setVia} options={vias.map(x => ({ value: x, label: VIA_LABEL[x] }))} />
          </Field>
        )}
      </div>
      <p className="muted lib-add-about">{describe(where, hows.includes(how) ? how : hows[0], v)}</p>
      {where === 'here' && (how === 'build' && hows.includes('build')
        ? <LocalLibraryForm key="here-build" mode="build" compact browse={browse} onAdded={lib => onAdded(lib, true)} />
        : <LocalLibraryForm key="here-connect" compact browse={browse} onAdded={lib => onAdded(lib, false)} />)}
      {where === 'remote' && v === 'workflow' && <ClusterLibraryForm compact onStarted={lib => onStarted?.(lib)} />}
      {where === 'remote' && (v === 'ssh' || v === 'pw') && (
        <SshLibraryForm key={`${v}-${how}`} compact transport={v} mode={how === 'build' && hows.includes('build') ? 'build' : 'connect'}
          onAdded={lib => onAdded(lib, how === 'build')} />
      )}
    </div>
  )
}
