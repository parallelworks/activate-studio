import { Fragment, type ReactNode } from 'react'
import { cx } from '@parallelworks/ui'
import { LibraryIcon } from '@parallelworks/ui/icons'
import type { PublicLibrary } from '../api'
import { MachineIcon } from '../code/parts'
import { useAppConfig } from '../config'
import { type MachineRef, useMachineLook } from '../machines'

/**
 * A machine, wherever the Studio names one: its icon and its resource's name,
 * as the Code page shows it, with its host name in its tooltip. One the
 * Studio cannot match to a resource keeps the name it was given. It is drawn
 * in the line it stands in and never breaks: what it is in cuts it short.
 */
export function MachineLabel({ machine, title, className, children }: {
  machine: MachineRef
  /** Its tooltip, in place of its host name. */
  title?: string
  className?: string
  /** After its name, such as a folder there. */
  children?: ReactNode
}) {
  const look = useMachineLook(machine)
  return (
    <span className={cx('machine-label', className)} title={title ?? look.title} data-testid="machine-label">
      <MachineIcon machine={look.icon} />{look.name}{children}
    </span>
  )
}

/** A machine's icon alone, where its name is said beside it. */
export function MachineGlyph({ machine }: { machine: MachineRef }) {
  const look = useMachineLook(machine)
  return <span className="machine-glyph" title={look.title}><MachineIcon machine={look.icon} /></span>
}

/** The machine a library is on, or null for one that is on none to name: on
 *  the Studio's own computer where its cluster is not known, or in a bucket. */
export function libraryMachine(l: Partial<Pick<PublicLibrary, 'site' | 'host' | 'access'>> | undefined): MachineRef | null {
  const a = l?.access
  if (!l || a?.where === 'bucket') return null
  if (a?.where === 'system' && a.via === 'ssh') return { name: a.system || l.site, kind: 'ssh' }
  // The address says whose a cluster is; the name alone does not.
  const name = l.site || a?.system || l.host
  return name ? { name } : null
}

/** The machine a transfer's location is on, or null. */
export function locationMachine(location: string, libraries: readonly PublicLibrary[]): MachineRef | null {
  const at = location.indexOf(':')
  const kind = location.slice(0, at), id = location.slice(at + 1)
  switch (kind) {
    case 'cluster': return { name: id }
    case 'library': return libraryMachine(libraries.find(l => l.id === id))
    case 'computer': return { kind: 'local', name: 'This computer' }
    case 'saved':
    case 'host': return { kind: 'ssh' }
  }
  return null
}

/** A transfer's end, as a line of text, after the icon of where it is. */
export function TransferEnd({ location, children }: { location: string; children: ReactNode }) {
  const libraries = useAppConfig().libraries ?? []
  const machine = locationMachine(location, libraries)
  return <>{machine && <><MachineGlyph machine={machine} />{' '}</>}{children}</>
}

/** A library's icon in a list of them: its machine's, or the same width. */
export function LibraryGlyph({ library }: { library?: PublicLibrary }) {
  const machine = libraryMachine(library)
  if (machine) return <MachineGlyph machine={machine} />
  return <span className="machine-glyph" aria-hidden>{library && <LibraryIcon className="code-machine-icon" />}</span>
}

/** What is said on one line, with dots between. */
export function Dotted({ parts }: { parts: ReactNode[] }) {
  return <>{parts.filter(Boolean).map((p, i) => <Fragment key={i}>{i > 0 && ' · '}{p}</Fragment>)}</>
}
