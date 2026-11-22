import { useCallback, useEffect, useState } from 'react'
import { Button, Modal, toast } from '@parallelworks/ui'
import { api } from '../api'
import { reloadAppConfig, useAppConfig } from '../config'
import { reloadWhenLibrariesBuilt } from '../libraryWatch'
import type { ListCheck } from '../libraryLists'
import { AddLibrary, type AddOptions } from './AddLibrary'
import { LibraryImport } from './LibraryImport'
import { json, type SetupStatus } from './PrimaryFolder'

/** What GET /api/user-libraries says this viewer may add of their own. */
export interface UserOffer { available: boolean; ssh: boolean; pwOffered: boolean; workflow: boolean; space?: boolean; home?: string }

/** The choices Add a library offers, from what the server says this viewer
 *  may add: building here (`canBuild`, sign-in off), ssh (sign-in off), pw
 *  ssh wherever it is offered (the form says why when it cannot be used
 *  yet), the platform's workflow on a Studio the platform hosts, and a copy
 *  of a folder in a Transfers location (`canBuild` too). */
export function addOptions(canBuild: boolean, user: UserOffer, platform: boolean): AddOptions {
  return { buildHere: canBuild, ssh: user.ssh, pw: user.pwOffered, workflow: user.workflow && platform && user.available, copy: canBuild, bucket: canBuild, space: !!user.space, home: user.home || undefined }
}

/**
 * Whether this viewer has a library they may add, by the same answers the
 * Settings forms and the library list check use. With sign-in off
 * (`canBuild`), anything. With sign-in on, libraries on the Studio's own
 * computer are the deployment's, so a viewer may add only libraries of
 * their own, and only once signed in: over pw ssh, or through the
 * platform's workflow.
 */
export function mayAddLibraries(canBuild: boolean, user: UserOffer | null, platform: boolean): boolean {
  if (!user) return false
  if (canBuild) return true
  const o = addOptions(canBuild, user, platform)
  return user.available && (o.pw || o.workflow || !!o.space)
}

/** The toast after one library is added, or its build started. */
export function addedMessage(lib: { label: string; bucket?: unknown }, started: boolean, here?: boolean): string {
  if (!started) return `Added ${lib.label}. It is in the Library switcher.`
  if (lib.bucket) return `Listing ${lib.label}. Its progress shows in the list. It is searchable by name once the listing is indexed.`
  return here
    ? `Building ${lib.label}. Its progress shows in the list. It is searchable by name once its folders are indexed; text and meaning, where its depth has them, follow in that order.`
    : `Building ${lib.label}. Its progress shows in the list; it is searchable once the build is done.`
}

export const CLUSTER_STARTED = 'Building the index on the cluster. It appears in the Library switcher when it is ready.'

/** The words about a library list, the same in Settings and the Library. */
export const IMPORT_ABOUT = 'A library list exported from another Studio, such as one your administrator or a colleague shares. Each library is checked and added as the Add a library form adds it.'

export interface LibraryAddOffer {
  /** Answered: until then nothing about libraries is offered. */
  loaded: boolean
  /** The viewer has a library they may add (`mayAddLibraries`). */
  allowed: boolean
  options: AddOptions
  /** A single-user install, where folders here can be picked from a list. */
  browse: boolean
  reload: () => void
}

/**
 * What the Library's Add menu may offer about libraries, asked of the same
 * routes Settings, Indexing, Libraries asks: GET /api/libraries for
 * `canBuild`, GET /api/user-libraries for what the viewer may add of their
 * own, and GET /api/setup/status for a single-user install.
 */
export function useLibraryAddOffer(): LibraryAddOffer {
  const cfg = useAppConfig()
  const [canBuild, setCanBuild] = useState(false)
  const [user, setUser] = useState<UserOffer | null>(null)
  const [browse, setBrowse] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [tick, setTick] = useState(0)
  useEffect(() => {
    let live = true
    void Promise.all([
      api.libraries().then(r => !!r?.canBuild, () => false),
      api.userLibraries().then(
        r => (r && typeof r === 'object' ? { available: !!r.available, ssh: !!r.ssh, pwOffered: !!(r.pwOffered ?? r.pw), workflow: !!r.workflow } : null),
        () => null,
      ),
      json<SetupStatus>('/api/setup/status').then(s => !!s?.standalone, () => false),
      // A folder of one's own is offered where one can be made, or where
      // the form can say what is missing (the Studio knows its cluster).
      api.spaceStatus().then(s => ({ space: !!(s?.available || s?.cluster), home: s?.cluster ?? '' }), () => ({ space: false, home: '' })),
    ]).then(([b, u, s, sp]) => {
      if (!live) return
      setCanBuild(b); setUser(u && { ...u, ...sp }); setBrowse(s); setLoaded(true)
    })
    return () => { live = false }
  }, [tick])
  const platform = !!cfg.platform
  const offer = user ?? { available: false, ssh: false, pwOffered: false, workflow: false }
  return {
    loaded,
    allowed: loaded && mayAddLibraries(canBuild, user, platform),
    options: addOptions(canBuild, offer, platform),
    browse,
    reload: useCallback(() => setTick(t => t + 1), []),
  }
}

/** Add a library, the form Settings shows, in a dialog. It closes once a
 *  library is added or its build starts. */
export function AddLibraryDialog({ open, onClose, offer, onAdded }: {
  open: boolean
  onClose: () => void
  offer: LibraryAddOffer
  /** A library was added or its build started. */
  onAdded?: () => void
}) {
  const done = (message: string) => {
    toast.success(message)
    reloadAppConfig()
    // A build that finishes later joins the switcher then, though Settings
    // is not open to notice it.
    reloadWhenLibrariesBuilt()
    onAdded?.()
    onClose()
  }
  return (
    // One size whatever the choice shows, so switching Where or How, or a
    // check's results appearing, never resizes the dialog: the form
    // scrolls inside it.
    <Modal open={open} onClose={onClose} size="xl" align="center" className="lib-add-modal" title="Add a library"
      description="An index or a folder, on this computer, on one of your systems, or in a bucket. On a system it is read as you, and written as you where your account may write."
      footer={<Button variant="secondary" onClick={onClose}>Close</Button>}>
      {open && (
        <AddLibrary options={offer.options} browse={offer.browse}
          onAdded={(lib, started, here) => done(addedMessage(lib, started, here))}
          onStarted={() => done(CLUSTER_STARTED)} />
      )}
    </Modal>
  )
}

/** A library list to import, already read and checked: a file dropped on
 *  the Library page. `upload` adds it to the library as a file instead. */
export interface DroppedList { text: string; check: ListCheck; upload?: () => void }

/**
 * Import a library list in a dialog: chosen, pasted, or dropped onto it.
 * When every ticked library was added it closes and the libraries join the
 * switcher; when one failed it stays open on each entry's result.
 */
export function ImportLibrariesDialog({ open, onClose, dropped, onImported }: {
  open: boolean
  onClose: () => void
  dropped?: DroppedList | null
  /** Some libraries were added. */
  onImported?: (added: number) => void
}) {
  const finished = (added: number, failed: number) => {
    if (added) {
      reloadAppConfig()
      reloadWhenLibrariesBuilt()
      onImported?.(added)
    }
    if (failed) return
    if (added) toast.success(added === 1 ? 'Added 1 library. It is in the Library switcher once it is ready.' : `Added ${added} libraries. Each is in the Library switcher once it is ready.`)
    onClose()
  }
  const upload = dropped?.upload
  return (
    <Modal open={open} onClose={onClose} size="lg" title="Import a library list" description={IMPORT_ABOUT}
      footer={(
        <>
          {upload && <Button variant="ghost" onClick={() => { onClose(); upload() }}>Add the file to the library instead</Button>}
          <Button variant="secondary" onClick={onClose}>Close</Button>
        </>
      )}>
      {open && <LibraryImport initial={dropped ?? undefined} onDone={finished} />}
    </Modal>
  )
}
