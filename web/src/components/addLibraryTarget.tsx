import type { ReactNode } from 'react'
import { menuDivider, type MenuEntry } from '@parallelworks/ui'
import { BucketIcon, ClusterIcon } from '@parallelworks/ui/icons'
import type { IconMachine } from '../code/machineIcon'
import { MachineIcon } from '../code/parts'
import type { PwSystem, PwSystems } from '../api'

/** Where a library is added from: this computer, one of the person's
 *  clusters over pw ssh, a host over plain ssh, or a bucket. */
export type Target =
  | { kind: 'here' }
  | { kind: 'cluster'; name: string }
  | { kind: 'ssh'; destination: string }
  | { kind: 'bucket' }

export interface AddOptions {
  /** Libraries can be built from folders on this computer (sign-in off). */
  buildHere: boolean
  /** Hosts over plain ssh (sign-in off). */
  ssh: boolean
  /** Over pw ssh: offered here, though it may not be usable yet (no pw CLI
   *  found, or not signed in). */
  pw: boolean
  /** The platform's index workflow: a hosted Studio that reaches a platform. */
  workflow: boolean
  /** A copy of a folder in a Transfers location (sign-in off), made from
   *  Transfers; the form's help points there. */
  copy?: boolean
  /** A bucket of an rclone remote, indexed here from its listing (sign-in off). */
  bucket?: boolean
  /** A folder on one of the viewer's clusters, indexed as them, can be
   *  added, shared, and be their My library where people sign in, and the
   *  folders shared with them there are listed. */
  space?: boolean
  /** The cluster this Studio runs on, where a cluster hosts it: listed
   *  first among the viewer's clusters, read as the viewer there, and never
   *  chosen for them. */
  home?: string
}

/** What the form offers, given what this Studio offers the viewer and, once
 *  it is known, whether they reach clusters over pw ssh. */
export function addChoices(o: AddOptions, systems?: PwSystems | null) {
  const asked = o.pw || !!o.space
  return {
    here: o.buildHere,
    /** Their clusters. On a Studio of one with nothing to reach them with,
     *  there are none, and nothing is said of them. */
    clusters: asked && !!systems?.usable,
    /** Still to be known. */
    finding: asked && !systems,
    /** People sign in here and cannot reach their clusters yet: the form
     *  says what is missing. */
    blocked: asked && !!systems && !systems.usable && !o.ssh,
    ssh: o.ssh,
    bucket: !!o.bucket,
    /** The platform's workflow is the only way to build: shown then alone. */
    workflowOnly: o.workflow && !asked && !o.ssh && !o.buildHere,
    /** An index that is there already can be connected, on a target. */
    connect: (t: Target | null) => t?.kind === 'here' ? o.buildHere : t?.kind === 'ssh' ? o.ssh : t?.kind === 'cluster' ? o.pw : false,
  }
}

/** The target to begin with: this computer, else the first running cluster,
 *  except on a Studio a cluster hosts, where nothing is chosen for them. */
export function firstTarget(o: AddOptions, systems: PwSystems | null): Target | null {
  if (o.buildHere) return { kind: 'here' }
  const up = o.home ? undefined : systems?.systems.find(s => s.running)
  return up ? { kind: 'cluster', name: up.name } : null
}

export const targetKey = (t: Target | null): string => (!t ? '' : t.kind === 'cluster' ? `cluster:${t.name}` : t.kind === 'ssh' ? `ssh:${t.destination}` : t.kind)

/** A target's name in a sentence. */
export const targetName = (t: Target | null, systems?: PwSystems | null): string => {
  switch (t?.kind) {
    case 'here': return 'This computer'
    case 'cluster': return systems?.systems.find(s => s.name === t.name)?.label ?? t.name
    case 'ssh': return t.destination
    case 'bucket': return 'A bucket'
  }
  return ''
}

/** A cluster as its icon is picked: a resource the person added, or one of
 *  a cloud's, by what the platform says of it. */
const clusterLooks = (s?: PwSystem): IconMachine => ({
  kind: s?.type === 'existing' ? 'existing' : 'cloud',
  ...(s?.imageUrl ? { imageUrl: s.imageUrl } : {}),
  ...(s?.csp ?? s?.type ? { csp: s?.csp ?? s?.type ?? undefined } : {}),
  ...(s?.schedulerType ? { schedulerType: s.schedulerType } : {}),
  ...(s?.os ? { os: s.os } : {}),
})

export function TargetIcon({ target, systems }: { target: Target; systems?: PwSystems | null }): ReactNode {
  switch (target.kind) {
    case 'here': return <MachineIcon machine={{ kind: 'local' }} />
    case 'bucket': return <BucketIcon aria-hidden className="code-machine-icon" />
    case 'ssh': return <MachineIcon machine={{ kind: 'existing' }} />
    case 'cluster': return <MachineIcon machine={clusterLooks(systems?.systems.find(s => s.name === target.name))} />
  }
}

/** The menu of the computers a library can be added from. */
export function targetMenu({ options, systems, hosts, target, onPick, onName }: {
  options: AddOptions
  systems: PwSystems | null
  /** The hosts the ssh config names. */
  hosts: string[]
  target: Target | null
  onPick: (t: Target) => void
  /** A host or a cluster to be named. */
  onName: (kind: 'ssh' | 'cluster') => void
}): MenuEntry[] {
  const offered = addChoices(options, systems)
  const now = targetKey(target)
  const row = (t: Target, more: { description?: string; hint?: string; disabled?: boolean } = {}): MenuEntry => ({
    label: targetName(t, systems), icon: <TargetIcon target={t} systems={systems} />, checked: targetKey(t) === now, onClick: () => onPick(t), ...more,
  })
  const named = target?.kind === 'ssh' && !hosts.includes(target.destination) ? [target.destination] : []
  const listed = [...(systems?.systems ?? [])].sort((a, b) => Number(b.name === options.home) - Number(a.name === options.home))
  const other = target?.kind === 'cluster' && !listed.some(s => s.name === target.name) ? [target.name] : []
  const clusters: MenuEntry[] = !offered.clusters ? [] : [
    ...listed.map(s => row({ kind: 'cluster', name: s.name }, s.running
      ? { ...(s.name === options.home ? { description: 'Where this Studio runs' } : s.label !== s.name ? { description: s.name } : {}) }
      : { description: 'Start it on ACTIVATE', disabled: true })),
    ...other.map(name => row({ kind: 'cluster', name })),
    { label: 'Another cluster…', icon: <ClusterIcon aria-hidden className="code-machine-icon" />, description: 'Type its name', onClick: () => onName('cluster') },
  ]
  const ssh: MenuEntry[] = !offered.ssh ? [] : [
    ...[...hosts, ...named].map(destination => row({ kind: 'ssh', destination }, { hint: 'ssh' })),
    { label: 'Another host…', icon: <ClusterIcon aria-hidden className="code-machine-icon" />, description: 'user@host, over ssh', onClick: () => onName('ssh') },
  ]
  return [
    ...(offered.here ? [row({ kind: 'here' })] : []),
    ...(clusters.length ? [{ group: 'Clusters', items: clusters }] : []),
    ...(ssh.length ? [{ group: 'Hosts over ssh', items: ssh }] : []),
    ...(offered.bucket ? [...(offered.here ? [menuDivider] : []), row({ kind: 'bucket' }, { description: 'Of an rclone remote' })] : []),
  ]
}
