import { useEffect, useMemo, useSyncExternalStore } from 'react'
import { api, type PwSystem, type PwSystems } from './api'
import type { IconMachine } from './code/machineIcon'
import { useNoticeMachines } from './notices'
import { VIEWER_CACHES_CLEARED } from './viewerEvents'

/**
 * Which of the viewer's machines a name is of, so that wherever the Studio
 * names one it is shown as the Code page shows it: its icon and its
 * resource's name (components/MachineLabel.tsx). A name may be a resource's,
 * its address (pw://owner/name), or the host name a pw code daemon reports.
 *
 * They are read from the Code page's own listing, which the notices carry,
 * and, for a system it does not have, from the viewer's clusters. Both are
 * read as the viewer, and both go when the viewer's sign-in does.
 */

/** A machine as something in the Studio names it. */
export interface MachineRef {
  /** Its name, address or host name. */
  name?: string | null
  /** Its id in the pw code listing. */
  id?: string | null
  /** The machine id its pw code daemon reports. */
  daemonId?: string | null
  /** The host it runs on, where the name is not that. */
  hostname?: string | null
  /** What it is, where that is said: ssh for a host over plain ssh, which is
   *  none of the platform's whatever it is called. */
  kind?: string
  /** What its icon is picked by, for one the platform does not list. */
  csp?: string | null
  imageUrl?: string | null
  os?: string | null
  schedulerType?: string | null
}

/** A machine the platform lists for the viewer. */
export interface KnownMachine extends Partial<IconMachine> {
  name: string
  id?: string
  owner?: string
  hostname?: string
  daemonId?: string
}

const ADDRESS = /^pw:\/\/([^/]+)\/([^/]+)\/?$/
const same = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase()

/** A name as a person reads it: an address by its last part. */
export const shortMachine = (name: string | null | undefined): string => ADDRESS.exec(name ?? '')?.[2] ?? name ?? ''

/** Whether two host names are one host's: the same, or one is the other
 *  with its domain. */
function sameHost(a: string | undefined, b: string): boolean {
  if (!a || !b) return false
  const x = a.toLowerCase(), y = b.toLowerCase()
  return x === y || x.startsWith(`${y}.`) || y.startsWith(`${x}.`)
}

/** Whether host is a node of the resource as the platform names the nodes
 *  of its cloud clusters: <owner>-<name>-00037-<node>. */
function nodeOf(m: KnownMachine, host: string): boolean {
  const lead = `${m.owner}-${m.name}-`.toLowerCase()
  return !!m.owner && m.kind !== 'local' && m.kind !== 'workspace' && host.toLowerCase().startsWith(lead) && /^\d{5}(-|$)/.test(host.slice(lead.length))
}

/** The one resource of found, with what either listing says of it; none
 *  where they are several, so no machine is shown as another. */
function only(found: KnownMachine[]): KnownMachine | undefined {
  if (!found.length) return undefined
  const [first, ...rest] = found
  if (rest.some(m => m.name !== first.name || (m.owner && first.owner && m.owner !== first.owner) || (m.id && first.id && m.id !== first.id))) return undefined
  return Object.assign({}, ...[...found].reverse()) as KnownMachine
}

/** Whether ref is of a host as a person wrote it, over plain ssh. */
const typed = (ref: MachineRef) => ref.kind === 'ssh' || !!ref.name?.includes('@')

/** The machine of known that ref names, or undefined. */
export function findMachine(ref: MachineRef, known: readonly KnownMachine[]): KnownMachine | undefined {
  if (typed(ref)) return undefined
  const byId = ref.id ? known.find(m => m.id === ref.id) : undefined
  if (byId) return byId
  if (ref.daemonId) {
    const found = only(known.filter(m => m.daemonId === ref.daemonId))
    if (found) return found
  }
  const name = ref.name?.trim()
  if (!name) return undefined
  const address = ADDRESS.exec(name)
  if (address) return only(known.filter(m => m.kind !== 'local' && m.name === address[2] && (!m.owner || m.owner === address[1])))
  // Another daemon's machine is another machine, whatever its host is called.
  const may = known.filter(m => !(ref.daemonId && m.daemonId))
  const named = may.filter(m => same(m.name, name))
  if (named.length) return only(named)
  const hosts = may.filter(m => sameHost(m.hostname, name))
  return hosts.length ? only(hosts) : only(may.filter(m => nodeOf(m, name)))
}

// ---- the viewer's clusters ----

const KEEP_MS = 10 * 60_000
/** How long after a failed read another may go. */
const RETRY_MS = 60_000

/** The listing's machines as last read, kept while it cannot be. */
let listed: readonly KnownMachine[] = []
let systems: KnownMachine[] = []
let readAt = 0
let reading = false
let viewer = 0
const listeners = new Set<() => void>()
const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f) } }
const snapshot = () => systems

const ofSystem = (s: PwSystem): KnownMachine => ({ name: s.name, owner: s.owner, ...systemLooks(s) })

/** A cluster as its icon is picked: a resource the person added, or one of
 *  a cloud's, by what the platform says of it. */
export const systemLooks = (s?: PwSystem): IconMachine => ({
  kind: s?.type === 'existing' ? 'existing' : 'cloud',
  ...(s?.imageUrl ? { imageUrl: s.imageUrl } : {}),
  ...(s?.csp ?? s?.type ? { csp: s?.csp ?? s?.type ?? undefined } : {}),
  ...(s?.schedulerType ? { schedulerType: s.schedulerType } : {}),
  ...(s?.os ? { os: s.os } : {}),
})

/** Takes the viewer's clusters, which a form has just read. */
export function knowSystems(r: PwSystems): void {
  if (r.error) return
  systems = (r.systems ?? []).map(ofSystem)
  readAt = Date.now()
  for (const f of listeners) f()
}

function readSystems(): void {
  if (reading || Date.now() - readAt < KEEP_MS) return
  reading = true
  const asked = viewer
  api.pwSystems().then(
    r => { if (asked === viewer) { if (r.error) readAt = Date.now() - KEEP_MS + RETRY_MS; else knowSystems(r) } },
    () => { if (asked === viewer) readAt = Date.now() - KEEP_MS + RETRY_MS },
  ).finally(() => { if (asked === viewer) reading = false })
}

/** Forgets the viewer's clusters. */
export function forgetMachines(): void {
  viewer++
  listed = []
  systems = []
  readAt = 0
  reading = false
  for (const f of listeners) f()
}
if (typeof window !== 'undefined') window.addEventListener(VIEWER_CACHES_CLEARED, forgetMachines)

// ---- how it is shown ----

/** How a machine is shown: its resource's name and icon, or, for one the
 *  Studio cannot match to a resource, the name it was given and a glyph. */
export interface MachineLook {
  name: string
  /** What its tooltip says: its host name, or the address it was named by. */
  title: string
  icon: IconMachine
  known: boolean
}

export function machineLook(ref: MachineRef, known: readonly KnownMachine[]): MachineLook {
  const given = shortMachine(ref.name)
  const m = findMachine(ref, known)
  const name = m?.name ?? given
  const host = m?.hostname ?? ref.hostname ?? (m && !ADDRESS.test(ref.name ?? '') ? given : undefined)
  const title = host && !same(host, name) ? `${name} (${host})` : ref.name ?? name
  const { csp, imageUrl, os, schedulerType } = m ?? ref
  const kind = m?.kind ?? (ref.kind === 'ssh' ? undefined : ref.kind) ?? 'existing'
  return { name, title, icon: { kind, csp, imageUrl, os, schedulerType } as IconMachine, known: !!m }
}

/** Every machine known of the viewer's, and whether the listing was read. */
function useKnownMachines(): { known: KnownMachine[]; read: boolean } {
  const { machines, loaded } = useNoticeMachines()
  const more = useSyncExternalStore(subscribe, snapshot, snapshot)
  if (machines) listed = machines as unknown as KnownMachine[]
  const now = listed
  return { known: useMemo(() => [...now, ...more], [now, more]), read: loaded }
}

/** How to show each of refs, as a machine's label does. */
export function useMachineLooks(): (ref: MachineRef) => MachineLook {
  const { known } = useKnownMachines()
  return useMemo(() => (ref: MachineRef) => machineLook(ref, known), [known])
}

/** How to show ref; its clusters are read where the listing has no such
 *  machine. */
export function useMachineLook(ref: MachineRef): MachineLook {
  const { known, read } = useKnownMachines()
  const look = machineLook(ref, known)
  const ask = read && !look.known && !ref.kind && !typed(ref) && !!(ref.name || ref.id)
  useEffect(() => { if (ask) readSystems() }, [ask])
  return look
}
