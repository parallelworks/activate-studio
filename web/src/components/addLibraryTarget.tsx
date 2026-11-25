import type { ReactNode } from 'react'
import { menuDivider, type MenuEntry } from '@parallelworks/ui'
import { BucketIcon, ClusterIcon } from '@parallelworks/ui/icons'
import { MachineIcon } from '../code/parts'
import { systemLooks } from '../machines'
import { pwBucketPlace, type PwBucket, type PwSystem, type PwSystems } from '../api'

/** Where a library is added from: this computer, one of the person's
 *  clusters over pw ssh, a host over plain ssh, one of their ACTIVATE buckets,
 *  or a bucket of an rclone remote. */
export type Target =
  | { kind: 'here' }
  | { kind: 'cluster'; name: string }
  | { kind: 'ssh'; destination: string }
  | { kind: 'pwbucket'; uri: string }
  | { kind: 'bucket' }

/** The viewer's ACTIVATE buckets, once asked for. */
export interface PwBuckets { buckets: PwBucket[]; available?: boolean; why?: string; loading: boolean }

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
  /** The viewer's ACTIVATE buckets, listed and read as them. */
  buckets?: boolean
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
    buckets: !!o.buckets,
    /** The platform's workflow is the only way to build: shown then alone. */
    workflowOnly: o.workflow && !asked && !o.ssh && !o.buildHere && !o.buckets,
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

export const targetKey = (t: Target | null): string => (!t ? '' : t.kind === 'cluster' ? `cluster:${t.name}` : t.kind === 'ssh' ? `ssh:${t.destination}` : t.kind === 'pwbucket' ? t.uri : t.kind)

/** A target's name in a sentence. */
export const targetName = (t: Target | null, systems?: PwSystems | null, buckets: PwBucket[] = []): string => {
  switch (t?.kind) {
    case 'here': return 'This computer'
    case 'cluster': return systems?.systems.find(s => s.name === t.name)?.label ?? t.name
    case 'ssh': return t.destination
    case 'pwbucket': return buckets.find(b => b.uri === t.uri)?.label ?? t.uri
    case 'bucket': return 'A bucket'
  }
  return ''
}

export function TargetIcon({ target, systems }: { target: Target; systems?: PwSystems | null }): ReactNode {
  switch (target.kind) {
    case 'here': return <MachineIcon machine={{ kind: 'local' }} />
    case 'pwbucket':
    case 'bucket': return <BucketIcon aria-hidden className="code-machine-icon" />
    case 'ssh': return <MachineIcon machine={{ kind: 'existing' }} />
    case 'cluster': return <MachineIcon machine={systemLooks(systems?.systems.find(s => s.name === target.name))} />
  }
}

/** The menu of the computers and buckets a library can be added from. */
export function targetMenu({ options, systems, buckets, hosts, target, onPick, onName }: {
  options: AddOptions
  systems: PwSystems | null
  buckets?: PwBuckets
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
    label: targetName(t, systems, buckets?.buckets), icon: <TargetIcon target={t} systems={systems} />, checked: targetKey(t) === now, onClick: () => onPick(t), ...more,
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
  const bucketIcon = <BucketIcon aria-hidden className="code-machine-icon" />
  const note = (label: string, description?: string): MenuEntry => ({ label, icon: bucketIcon, disabled: true, ...(description ? { description } : {}), onClick: () => {} })
  const found = buckets?.buckets ?? []
  const pw: MenuEntry[] = !offered.buckets ? [] : found.length
    ? found.map(b => row({ kind: 'pwbucket', uri: b.uri }, b.why ? { description: b.why, disabled: true } : { description: `${b.namespace} · ${pwBucketPlace(b)}` }))
    : [buckets?.why ? note(buckets.available === false ? 'Your buckets' : 'Your buckets could not be listed', buckets.why) : buckets && !buckets.loading ? note('No buckets yet', 'Make one on ACTIVATE, under Storage') : note('Finding your buckets…')]
  return [
    ...(offered.here ? [row({ kind: 'here' })] : []),
    ...(clusters.length ? [{ group: 'Clusters', items: clusters }] : []),
    ...(ssh.length ? [{ group: 'Hosts over ssh', items: ssh }] : []),
    ...(pw.length ? [{ group: 'ACTIVATE buckets', items: pw }] : []),
    ...(offered.bucket ? [
      ...(offered.here ? [menuDivider] : []),
      { ...row({ kind: 'bucket' }), label: offered.buckets ? 'Another bucket (rclone remote)' : 'A bucket (rclone remote)' } as MenuEntry,
    ] : []),
  ]
}
