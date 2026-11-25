import { ALL_LIBRARIES, LibrarySwitch, libraryReadOnlyWhy, libraryWritable } from '../components/LibrarySwitch'
import { currentLibrary, libraryOf, onLibraryChange, warmRemoteHosts } from '../api'
// eslint-disable-next-line @typescript-eslint/no-unused-vars
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'
import {
  Button, Callout, Checkbox, ConfirmModal, EmptyState, IconButton, Input, Modal, ProgressBar, RefreshButton,
  toast, useContextMenu, type MenuEntry,
} from '@parallelworks/ui'
import type { Display } from '../App'
import { api } from '../api'
import { Explorer, visibleRowPaths, type DragKind, type DropKind } from '../components/Explorer'
import { Viewer } from '../components/Viewer'
import { DagViewer } from '../components/DagViewer'
import { LibraryAddMenu } from '../components/UploadMenu'
import { AddLibraryDialog, ImportLibrariesDialog, useLibraryAddOffer, type DroppedList } from '../components/LibraryAddDialogs'
import { checkList, LIST_FORMAT } from '../libraryLists'
import { TagMenu } from '../components/TagMenu'
import { sectionAllowed, useAppConfig } from '../config'
import { shownPath } from '../libraryPath'
import { copyItemLink, copyLinkLabel } from '../itemLinks'
import { askPlace, CHAT_HERE, chatHere, knownPlace } from '../pwcode/chatHere'
import { collectChosen, collectDropped, collectListed, type UploadItem } from '../upload'
import { chooseUpload, onShellDrop, shellListsFolders } from '../nativeShell'
import { downloadPath } from '../libraryDownload'
import { indexText, startPathJob, startUpload } from '../transfers'
import { NO_BASE, TextConflict } from '../textFiles'
import { leaveUnsaved } from '../unsaved'
import { TransferPanel } from '../components/TransferPanel'
import { forgetSelectionUnder, renameSelection, selectLibraryEntry } from '../components/LibraryDetails'
import { libraryWritten } from '../libraryCatchUp'
import { ShareDialog } from '../components/ShareDialog'
import { ResultsTransferDialog, useTransfersShown, type ResultItem } from '../components/ResultsTransfer'
import { destinationWhy, libraryLocation, transferApi } from '../transferApi'
import { LockedButton } from '../components/LockedButton'
import { MID_QUERY, useMediaQuery, usePhoneLayout } from '../phoneLayout'
import { TreeResize, clampTree, treeMax } from '../components/TreeResize'

/** The narrowest an open file reads well beside the tree, in pixels. */
const FILE_MIN = 480
/** The divider between the tree and the file. */
const DIVIDER = 5

/** Human-sized byte counts for the upload panel. */
/** A folder a link leads to, as the tree reads its path: shown open, in
 *  view and selected. Each link is one of these, the same folder or not. */
export interface Reveal { path: string }

export function LibraryView({ display, onDisplay, reveal, replaced, onReplacedRead }: {
  display: Display | null
  onDisplay: (d: Display | null) => void
  reveal?: Reveal | null
  /** What to say over an item that took the place of the one its link was
   *  made for, while it is the one open. */
  replaced?: { path: string; text: string } | null
  onReplacedRead?: () => void
}) {
  // Uploads land in uploads/ until a folder is picked; with All libraries
  // selected, the primary library's.
  const [targetDir, setTargetDir] = useState(() => (currentLibrary() === ALL_LIBRARIES ? 'kb/uploads' : 'uploads'))
  const [refreshKey, setRefreshKey] = useState(0)
  const [railWidth, setRailWidth] = useState(() => Number(localStorage.getItem('ade-rail-width')) || 320)
  const [railCollapsed, setRailCollapsed] = useState(() => localStorage.getItem('ade-rail-collapsed') === '1')
  const [multiSel, setMultiSel] = useState<Set<string>>(new Set())
  const [tagMenuOpen, setTagMenuOpen] = useState(false)
  const [selectMode, setSelectMode] = useState(false)
  const [allDirs, setAllDirs] = useState<string[]>([])
  /** Proposed labels awaiting review; nothing is applied until accepted. */
  const [suggest, setSuggest] = useState<{
    dir: string
    busy: boolean
    error?: string
    considered?: number
    rows: { path: string; labels: string[]; why: string; accept: boolean }[]
  } | null>(null)
  const [showLabels, setShowLabels] = useState(() => localStorage.getItem('ade-tree-labels') === '1')
  const [showHidden, setShowHidden] = useState(() => localStorage.getItem('ade-tree-hidden') === '1')
  const [tagsTick, setTagsTick] = useState(0)
  const [confirmDelete, setConfirmDelete] = useState(false)
  // Right-click context menu on tree rows. Its New folder and Rename take a
  // name in a dialog, and its Delete asks first.
  const { openMenu, contextMenu } = useContextMenu()
  const [nameDialog, setNameDialog] = useState<{
    mode: 'mkdir' | 'newfile' | 'rename'
    path: string
    isDir: boolean
    value: string
    busy: boolean
    error: string
  } | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<{ path: string; isDir: boolean; error: string } | null>(null)
  /** A folder of the viewer's space being shared: the space and the folder in it. */
  const [shareTarget, setShareTarget] = useState<{ library: string; path: string } | null>(null)
  /** A menu choice that opens a dialog, run once the menu has closed and
   *  handed focus back, so the dialog keeps the focus it takes. */
  const afterMenu = useRef<(() => void) | null>(null)
  // Upload files and Upload a folder in the right-click menu: the folder
  // they go into, and the choosers they open.
  const uploadDir = useRef('')
  const filesInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  /** Rows being copied to another library through Transfers, and the
   *  folder to start at when they were dropped or pasted on one. */
  const [copyTo, setCopyTo] = useState<{ items: ResultItem[]; noun: string; destination?: { location: string; path: string } } | null>(null)
  const transfersShown = useTransfersShown()
  /** Cut or copied paths, waiting for a destination. */
  const [clipboard, setClipboard] = useState<{ mode: 'cut' | 'copy'; paths: string[] } | null>(null)
  const selPaths = useMemo(() => [...multiSel], [multiSel])

  const ctxDelete = async () => {
    const t = deleteTarget
    if (!t) return
    if (t.isDir) {
      setDeleteTarget(null)
      if (display?.kind === 'file' && display.target.startsWith(t.path + '/')) onDisplay(null)
      forgetSelectionUnder(t.path)
      void startPathJob('delete', [t.path], '', () => api.deleteDirJob(t.path))
      return
    }
    setDeleteTarget({ ...t, error: '' })
    const lib = libraryOf(t.path)
    try {
      await api.deleteFile(t.path)
      if (display?.kind === 'file' && typeof display.target === 'string' && (display.target === t.path || display.target.startsWith(t.path + '/'))) onDisplay(null)
      // The Details card describes the library again, not the file deleted.
      forgetSelectionUnder(t.path)
      setRefreshKey(k => k + 1)
      libraryWritten(lib)
      setDeleteTarget(null)
    } catch (e) {
      setDeleteTarget({ ...t, error: String((e as Error).message ?? e) })
    }
  }

  /** A file just made, which the viewer opens ready to type in. */
  const [startEditing, setStartEditing] = useState<{ path: string } | null>(null)

  /** New folder, New file or Rename, from the name typed in the dialog. */
  const submitName = async () => {
    const d = nameDialog
    if (!d || d.busy || !d.value.trim()) return
    setNameDialog({ ...d, busy: true, error: '' })
    // The library written to (in All mode the one the path names), even if
    // another is selected by the time the write returns.
    const lib = libraryOf(d.path)
    try {
      if (d.mode === 'rename') {
        const r = await api.rename(d.path, d.value.trim())
        // A renamed file that is open in the viewer should follow its name.
        if (r.renamed && !d.isDir && selectedFile === d.path) onDisplay({ kind: 'file', target: r.renamed.to })
        // The Details card follows the new name.
        if (r.renamed) renameSelection(r.renamed.from, r.renamed.to)
        libraryWritten(lib)
      } else if (d.mode === 'newfile') {
        const name = d.value.trim().replace(/^\/+|\/+$/g, '')
        const r = await api.saveText(d.path ? `${d.path}/${name}` : name, '', NO_BASE)
        void indexText(r.path, { created: true, reindexing: r.reindexing })
        setRefreshKey(k => k + 1)
        setStartEditing({ path: r.path })
        onDisplay({ kind: 'file', target: r.path })
      } else {
        const name = d.value.trim().replace(/^\/+|\/+$/g, '')
        const r = await api.mkdir(d.path ? `${d.path}/${name}` : name)
        setTargetDir(r.created)
        setRefreshKey(k => k + 1)
        libraryWritten(lib)
      }
      setNameDialog(null)
    } catch (e) {
      const error = e instanceof TextConflict && e.state === 'exists'
        ? `There is already something named ${d.value.trim()} in this folder. Choose another name.`
        : String((e as Error).message ?? e)
      setNameDialog(cur => cur && { ...cur, busy: false, error })
    }
  }

  /** Runs in the panel below the tree, so the dialog closes at once. */
  const bulkDelete = () => {
    const files = [...multiSel]
    setMultiSel(new Set())
    if (display?.kind === 'file' && files.includes(display.target)) onDisplay(null)
    for (const f of files) forgetSelectionUnder(f)
    void startPathJob('delete', files, '', paths => api.deleteJob(paths))
  }
  const drag = useRef<{ startX: number; startWidth: number } | null>(null)
  const cfg = useAppConfig()
  // Switching library re-reads the tree and clears whatever was open,
  // since a path only means something inside its own library.
  const [libId, setLibId] = useState(currentLibrary())
  // The sessions to the clusters the viewer's own libraries are on, opened
  // before the first click, and again when the tab comes back.
  useEffect(() => {
    warmRemoteHosts()
    const back = () => { if (document.visibilityState === 'visible') warmRemoteHosts() }
    document.addEventListener('visibilitychange', back)
    return () => document.removeEventListener('visibilitychange', back)
  }, [])
  useEffect(() => onLibraryChange(id => { setLibId(id); setRefreshKey(k => k + 1); onDisplay(null); setTargetDir(id === ALL_LIBRARIES ? 'kb/uploads' : 'uploads') }), [onDisplay])
  // A library added, imported or removed in Settings changes the folders
  // of All libraries: read the tree again when the set of libraries does.
  const libSet = cfg.libraries?.map(l => l.id).join('\n') ?? ''
  const seenLibSet = useRef(libSet)
  useEffect(() => {
    if (seenLibSet.current === libSet) return
    seenLibSet.current = libSet
    setRefreshKey(k => k + 1)
  }, [libSet])
  const writable = libraryWritable(cfg.libraries, libId)
  // With All libraries selected the tree has a folder per library, and a
  // path is writable when its library is; a library's own folder takes
  // uploads and new folders but is not itself renamed or deleted.
  const inAll = libId === ALL_LIBRARIES
  const primaryId = cfg.libraries?.find(l => l.primary)?.id ?? 'kb'
  const writableAt = (p: string) => (inAll ? libraryWritable(cfg.libraries, p.replace(/^\/+/, '').split('/')[0]) : writable)
  const changeableAt = (p: string) => !!p && writableAt(p) && (!inAll || p.includes('/'))
  const libraryAt = (p: string) => (inAll ? p.replace(/^\/+/, '').split('/')[0] : libId)
  // Why a path cannot be changed (its library is read only), shown on the
  // actions that would change it; null when it can. Labels still apply,
  // except where the viewer may not change the library at all (labelsLockedAt).
  const readOnlyAt = (p: string): string | null => {
    if (writableAt(p)) return null
    const id = libraryAt(p)
    return libraryReadOnlyWhy(cfg.libraries, id) ?? `${cfg.libraries?.find(l => l.id === id)?.label ?? 'This library'} is read only`
  }
  // Why labels cannot be set at a path: the main library on a shared
  // Studio, read only for everyone but its administrators.
  const labelsLockedAt = (p: string): string | null => libraryReadOnlyWhy(cfg.libraries, libraryAt(p))
  // A folder the viewer owns, which they can share: its library and
  // the folder within it ('' for all of it).
  const spaceAt = (p: string): { library: string; path: string } | null => {
    const clean = p.replace(/^\/+/, '')
    const id = inAll ? clean.split('/')[0] : libId
    const lib = cfg.libraries?.find(l => l.id === id)
    if (!(lib?.space || (lib?.own && lib.access?.via !== 'ssh')) || lib.sharedFrom || lib.readOnlyWhy) return null
    return { library: id, path: inAll ? clean.split('/').slice(1).join('/') : clean }
  }
  // A tree path as Transfers names it: its library and the path there.
  const itemAt = (p: string): ResultItem => {
    const clean = p.replace(/^\/+/, '')
    if (!inAll) return { library: libId, path: clean }
    const [id, ...rest] = clean.split('/')
    return { library: id, path: rest.join('/') }
  }
  // Copy to another library: with Transfers shown and another library to
  // copy to; never a library's own top folder in All libraries.
  const canCopyOut = transfersShown && (cfg.libraries?.length ?? 0) > 1
  // Which libraries a transfer can write in, as Transfers lists them: this
  // follows who reaches the files, which can differ from the Library's own
  // writes (a library on this computer with sign-in off takes a transfer,
  // and only the main library and spaces take the Library's edits).
  const [xferInto, setXferInto] = useState<Set<string> | null>(null)
  useEffect(() => {
    if (!canCopyOut) return
    let live = true
    transferApi.locations().then(locs => {
      if (live) setXferInto(new Set(locs.filter(l => l.kind === 'library' && !destinationWhy(l)).map(l => l.id)))
    }, () => {})
    return () => { live = false }
  }, [canCopyOut, libSet])
  const libOf = (p: string) => itemAt(p).library
  const libraryTopAt = (p: string) => inAll && !p.replace(/^\/+/, '').includes('/')
  /** Copy rows into a folder of another library, or of a space (whose
   *  own copy is not there yet), through Transfers: the dialog opens on
   *  that folder. */
  const copyInto = (dir: string, paths: string[]) => {
    const to = itemAt(dir)
    const rows = paths.filter(p => p && !libraryTopAt(p))
    if (!rows.length) return
    setCopyTo({ items: rows.map(itemAt), noun: 'item', destination: { location: `library:${to.library}`, path: to.path } })
  }
  // A row drags as a move within its library and as a copy to another
  // library, a read-only library's rows only as a copy; a library's own
  // folder in All libraries does not drag.
  const dragKind = (p: string): DragKind => {
    if (!p || libraryTopAt(p)) return null
    const w = writableAt(p)
    if (inAll && canCopyOut) return w ? 'copyMove' : 'copy'
    return w ? 'move' : null
  }
  // A folder takes a drop when its library is writable: a move from the
  // same library, a copy from another (with Transfers on). Never a folder
  // into itself or one inside it, or the top of All libraries.
  const dropKind = (dir: string, paths: string[]): DropKind => {
    if (!paths.length || (inAll && !dir)) return 'none'
    const target = libOf(dir)
    if (paths.some(p => libOf(p) !== target)) {
      const takes = xferInto ? xferInto.has(libraryLocation(target)) : writableAt(dir)
      return canCopyOut && takes ? 'copy' : 'none'
    }
    if (!writableAt(dir) || paths.some(p => p === dir || dir.startsWith(`${p}/`))) return 'none'
    return paths.every(p => writableAt(p)) ? 'move' : 'none'
  }
  // A selection in a read-only library can be labeled but not moved or deleted.
  const selLocked = selPaths.map(p => readOnlyAt(p)).find(Boolean) ?? null
  const selLabelsLocked = selPaths.map(p => labelsLockedAt(p)).find(Boolean) ?? null
  const uploadsDir = inAll ? `${primaryId}/uploads` : 'uploads'

  useEffect(() => { localStorage.setItem('ade-rail-width', String(railWidth)) }, [railWidth])
  useEffect(() => { localStorage.setItem('ade-rail-collapsed', railCollapsed ? '1' : '0') }, [railCollapsed])

  useEffect(() => {
    const onChange = () => setRefreshKey(k => k + 1)
    const onTags = () => setTagsTick(k => k + 1)
    window.addEventListener('ade-kb-changed', onChange)
    window.addEventListener('ade-tags-changed', onTags)
    return () => {
      window.removeEventListener('ade-kb-changed', onChange)
      window.removeEventListener('ade-tags-changed', onTags)
    }
  }, [])

  useEffect(() => { localStorage.setItem('ade-tree-labels', showLabels ? '1' : '0') }, [showLabels])
  useEffect(() => { localStorage.setItem('ade-tree-hidden', showHidden ? '1' : '0') }, [showHidden])
  // Labeling is what select mode is for, so the tree shows them, and the
  // toggle says so and can still turn them off.
  useEffect(() => { if (selectMode) setShowLabels(true) }, [selectMode])

  // Cut, copy and paste from the keyboard, ignored while typing so a
  // rename box or a search field keeps its own shortcuts.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
      const paths = [...multiSel]
      if (e.key === 'x' && paths.length) { setClipboard({ mode: 'cut', paths }); e.preventDefault() }
      else if (e.key === 'c' && paths.length) { setClipboard({ mode: 'copy', paths }); e.preventDefault() }
      else if (e.key === 'v' && clipboard?.paths.length) { void pasteInto(targetDir === uploadsDir ? (inAll ? primaryId : '') : targetDir); e.preventDefault() }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // The row a range extends from: the last one clicked without shift.
  const anchor = useRef<string | null>(null)
  /** Live while an upload is in flight, so it can be stopped. */

  const toggleMulti = (p: string) => {
    anchor.current = p
    setMultiSel(s => {
      const next = new Set(s)
      if (next.has(p)) next.delete(p)
      else next.add(p)
      return next
    })
  }

  /** Shift-click selects everything between the anchor and this row, in the
   *  order the rows appear on screen. */
  const selectRange = (p: string) => {
    const rows = visibleRowPaths()
    const from = anchor.current && rows.includes(anchor.current) ? rows.indexOf(anchor.current) : rows.indexOf(p)
    const to = rows.indexOf(p)
    if (from < 0 || to < 0) return
    const [lo, hi] = from <= to ? [from, to] : [to, from]
    setSelectMode(true)
    setMultiSel(s => {
      const next = new Set(s)
      for (const row of rows.slice(lo, hi + 1)) next.add(row)
      return next
    })
    anchor.current ??= p
  }

  /** Paste whatever was cut or copied into a directory. Into another
   *  library it is a copy through Transfers, cut or not, so nothing leaves
   *  the library it was in; a copy inside a space, or inside a library of
   *  the viewer's own on another system, goes the same way. */
  const pasteInto = async (dir: string) => {
    if (!clipboard?.paths.length) return
    const target = libOf(dir)
    const across = clipboard.paths.some(p => libOf(p) !== target)
    const to = cfg.libraries?.find(l => l.id === target)
    const inSpace = clipboard.mode === 'copy' && !!(to?.space || to?.mine)
    if ((across || inSpace) && canCopyOut) {
      copyInto(dir, clipboard.paths)
      return
    }
    if (clipboard.mode === 'cut') {
      await moveInto(dir, clipboard.paths)
      setClipboard(null)
      return
    }
    await startPathJob('copy', clipboard.paths, dir, paths => api.copyJob(paths, dir))
  }

  /** Ask the model what this material should be labelled. */
  const runSuggest = async (dir: string) => {
    setSuggest({ dir, busy: true, rows: [] })
    try {
      const r = await api.labelSuggestions(dir, 40)
      setSuggest({
        dir, busy: false, considered: r.considered,
        rows: r.proposals.map(p2 => ({ ...p2, accept: true })),
      })
    } catch (e) {
      setSuggest({ dir, busy: false, rows: [], error: String((e as Error).message ?? e) })
    }
  }

  /** Apply the accepted proposals, grouped so identical label sets go in
   *  one call rather than one per file. */
  const applySuggestions = async () => {
    if (!suggest) return
    const groups = new Map<string, string[]>()
    for (const row of suggest.rows.filter(r => r.accept && r.labels.length)) {
      const key = row.labels.join(',')
      groups.set(key, [...(groups.get(key) ?? []), row.path])
    }
    setSuggest({ ...suggest, busy: true })
    // A refused apply is said, not dropped: the panel stays open on the
    // proposals that were not saved, with the server's reason.
    const failed: { paths: string[]; why: string }[] = []
    for (const [labels, paths] of groups) {
      await api.applyTags(paths, labels.split(','), []).catch(e => { failed.push({ paths, why: String((e as Error).message ?? e) }) })
    }
    window.dispatchEvent(new CustomEvent('ade-tags-changed'))
    window.dispatchEvent(new CustomEvent('ade-kb-changed'))
    if (!failed.length) { setSuggest(null); return }
    const left = new Set(failed.flatMap(f => f.paths))
    setSuggest({ ...suggest, busy: false, rows: suggest.rows.filter(r => left.has(r.path)), error: `These labels were not saved: ${failed[0].why}` })
  }

  /** Hand a path to the chat as a reference, the way an editor does. */
  const addToChat = (path: string) => {
    window.dispatchEvent(new CustomEvent('ade-chat-insert', { detail: { text: path } }))
    location.hash = '#view=chat'
  }

  /** Drag a row (or the whole selection) onto a directory to move it, or
   *  pick a destination for the selection. Runs as a job so a few hundred
   *  files report progress instead of freezing the panel. */
  const moveInto = async (dir: string, paths: string[]) => {
    const clean = paths.filter(p => p && p !== dir && !dir.startsWith(`${p}/`))
    if (!clean.length) return
    leaveUnsaved(() => {
      void startPathJob('move', clean, dir, paths => api.moveJob(paths, dir))
      setMultiSel(new Set())
    }, clean)
  }

  /** Pick a destination for the selection from every folder in the library. */
  const openMoveMenu = async (at: HTMLElement) => {
    const dirs = allDirs.length ? allDirs : (await api.dirs().catch(() => ({ dirs: [] as string[] }))).dirs
    if (!allDirs.length) setAllDirs(dirs)
    const paths = [...multiSel]
    openMenu(at, [
      { label: `${cfg.kbLabel} (root)`, onClick: () => void moveInto('', paths) },
      ...dirs.map(d => ({ label: d, onClick: () => void moveInto(d, paths) })),
    ], {
      search: { placeholder: `Move ${paths.length} ${paths.length === 1 ? 'item' : 'items'} to…`, empty: 'No matching folder' },
    })
  }

  /** Names a text file to make in a folder, once an unsaved edit is settled. */
  const newFile = (dir: string) => leaveUnsaved(() => setNameDialog({ mode: 'newfile', path: dir, isDir: true, value: 'Untitled.md', busy: false, error: '' }))

  const openContextMenu = (path: string, isDir: boolean, x: number, y: number) => {
    const later = (run: () => void) => () => { afterMenu.current = run }
    // Cut and copy take the whole selection when the row is part of it.
    const group = multiSel.has(path) && multiSel.size > 1 ? ` ${multiSel.size} items` : ''
    const targets = () => (multiSel.has(path) ? [...multiSel] : [path])
    const items: MenuEntry[] = []
    let hereAt = -1
    if (!isDir) items.push({ label: 'Open', onClick: () => onDisplay({ kind: 'file', target: path }) })
    const locked = readOnlyAt(path)
    const libraryTop = inAll && !path.includes('/')
    if (isDir) {
      items.push({ label: 'New folder', disabled: !!locked, disabledReason: locked ?? undefined, onClick: later(() => setNameDialog({ mode: 'mkdir', path, isDir, value: '', busy: false, error: '' })) })
      // The top of All libraries is no folder: a file needs a library.
      if (!inAll || path) items.push({ label: 'New file…', disabled: !!locked, disabledReason: locked ?? undefined, onClick: later(() => newFile(path)) })
    }
    // With All libraries the top of the tree is no folder: it holds the
    // libraries, so nothing is uploaded to it or downloaded from it.
    const allTop = inAll && !path
    if (isDir && !allTop) {
      // The choosers open from the click itself, which a browser requires.
      const choose = (input: RefObject<HTMLInputElement | null>) => () => { uploadDir.current = path; input.current?.click() }
      items.push(
        { label: 'Upload files…', disabled: !!locked, disabledReason: locked ?? undefined, onClick: choose(filesInput) },
        {
          label: 'Upload a folder…', disabled: !!locked, disabledReason: locked ?? undefined,
          // The Linux and Windows window's chooser cannot pick a folder, so
          // the window lists one itself.
          onClick: shellListsFolders()
            ? () => void chooseUpload('Choose a folder to upload').then(files => uploadItems(path, collectListed(files)))
            : choose(folderInput),
        },
      )
    }
    if (!allTop) items.push({ label: isDir ? 'Download as zip' : 'Download', onClick: () => void downloadPath(path, isDir) })
    if (path && !libraryTop) {
      items.push({ label: 'Rename…', disabled: !!locked, disabledReason: locked ?? undefined, onClick: later(() => leaveUnsaved(() => setNameDialog({ mode: 'rename', path, isDir, value: path.split('/').pop() ?? '', busy: false, error: '' }), [path])) })
    }
    const space = isDir ? spaceAt(path) : null
    if (space) items.push({ label: 'Share…', onClick: later(() => setShareTarget(space)) })
    // Both call a model, so they are offered only with AI features on.
    const labelsLocked = labelsLockedAt(path)
    if (isDir && cfg.aiEnabled !== false) items.push({ label: 'Suggest labels…', disabled: !!labelsLocked, disabledReason: labelsLocked ?? undefined, onClick: () => void runSuggest(path) })
    if (path && sectionAllowed('chat', cfg)) items.push({ label: 'Add to chat', onClick: () => addToChat(path) })
    // The reason it cannot is written in the entry: a tip does not open on
    // touch. A bucket's, and a library's read only to the person, is known
    // here; the rest the Studio finds as the menu opens.
    const here = (why?: string): MenuEntry => ({ label: CHAT_HERE, disabled: !!why, description: why, onClick: () => void chatHere(itemAt(path), primaryId) })
    const held = cfg.libraries?.find(l => l.id === libraryAt(path))
    const noChat = held?.fromBucket || held?.access?.where === 'bucket' ? 'A bucket has no folder a pw code session can work in.' : labelsLocked ? 'This library is read only for you, so a pw code session cannot work here.' : undefined
    if (!allTop && sectionAllowed('chat', cfg)) hereAt = items.push(here(noChat ?? knownPlace(itemAt(path))?.why)) - 1
    if (path && !libraryTop && canCopyOut) {
      const rows = targets().filter(p => !inAll || p.includes('/'))
      items.push({
        label: rows.length > 1 ? `Copy ${rows.length} items to another library…` : 'Copy to another library…',
        onClick: later(() => setCopyTo({ items: rows.map(itemAt), noun: rows.length > 1 ? 'item' : isDir ? 'folder' : 'file' })),
      })
    }
    if (path) {
      items.push(
        { label: copyLinkLabel(cfg.linkBase), onClick: () => void copyItemLink(itemAt(path), cfg.linkBase) },
        { label: 'Copy path', copy: { text: path, label: 'path' } },
        { label: `Cut${group}`, disabled: !!locked, disabledReason: locked ?? undefined, onClick: () => setClipboard({ mode: 'cut', paths: targets() }) },
        { label: `Copy${group}`, onClick: () => setClipboard({ mode: 'copy', paths: targets() }) },
      )
    }
    if (isDir && clipboard?.paths.length) {
      const n = clipboard.paths.length
      items.push({ label: `Paste ${n} ${n === 1 ? 'item' : 'items'}`, disabled: !!locked, disabledReason: locked ?? undefined, onClick: () => void pasteInto(path) })
    }
    if (path) {
      items.push({ label: 'Labels…', disabled: !!labelsLocked, disabledReason: labelsLocked ?? undefined, onClick: later(() => { setMultiSel(new Set([path])); setTagMenuOpen(true); setSelectMode(true) }) })
    }
    if (path && !libraryTop) {
      items.push({ label: isDir ? 'Delete folder…' : 'Delete…', destructive: true, disabled: !!locked, disabledReason: locked ?? undefined, onClick: later(() => leaveUnsaved(() => setDeleteTarget({ path, isDir, error: '' }), [path])) })
    }
    let open = true
    const show = (rows: MenuEntry[]) => openMenu({ clientX: x, clientY: y }, rows, {
      themeClassName: 'library-menu',
      onClose: () => {
        open = false
        const run = afterMenu.current
        afterMenu.current = null
        run?.()
      },
    })
    show(items)
    // Where the chat would work is looked at as the person, on the machine
    // that holds the folder: a reason it cannot comes into the open menu.
    if (hereAt >= 0 && !noChat && !knownPlace(itemAt(path))) {
      void askPlace(itemAt(path)).then(({ why }) => {
        if (!why || !open) return
        const rows = items.slice()
        rows[hereAt] = here(why)
        show(rows)
      })
    }
  }

  const railRef = useRef<HTMLElement>(null)

  const uploadItems = useCallback(async (dir: string, files: UploadItem[]) => {
    if (!files.length) return
    const result = await startUpload(files, dir)
    if (result.done > 0 && files.length === 1) {
      onDisplay({ kind: 'file', target: (dir ? dir + '/' : '') + files[0].rel })
    }
  }, [onDisplay])

  // Adding a library, and importing a library list, in dialogs over this
  // page, for a viewer who may add libraries (as Settings decides).
  const libOffer = useLibraryAddOffer()
  const [addingLibrary, setAddingLibrary] = useState(false)
  const [importing, setImporting] = useState<{ dropped: DroppedList | null } | null>(null)
  const libraryAdded = () => { libOffer.reload(); setRefreshKey(k => k + 1) }

  /** A drop of one .json file that the server reads as a library list
   *  offers its import, with adding the file as it is still a choice. Any
   *  other drop, or a file the check refuses, is added as files. */
  const dropped = useCallback(async (dir: string, files: UploadItem[]) => {
    const one = files.length === 1 ? files[0] : null
    if (libOffer.allowed && one && !one.linked && /\.json$/i.test(one.rel) && one.file.size <= LIST_MAX_BYTES) {
      const text = await one.file.text().catch(() => '')
      const check = text.includes(LIST_FORMAT) ? await checkList(text).catch(() => null) : null
      if (check) {
        setImporting({ dropped: { text, check, upload: writableAt(dir) ? () => void uploadItems(dir, files) : undefined } })
        return
      }
    }
    if (writableAt(dir)) await uploadItems(dir, files)
  }, [libOffer.allowed, uploadItems, writableAt])

  const uploadTo = useCallback(async (dir: string, items: DataTransferItemList) => {
    await dropped(dir, await collectDropped(items))
  }, [dropped])

  // The Linux window takes a file manager's drop itself and says where it
  // landed: a folder row, a file's folder, or the destination in use. Not
  // while a dialog about libraries is open over the page.
  const dialogOpen = addingLibrary || importing !== null
  useEffect(() => onShellDrop(({ x, y, files }) => {
    if (dialogOpen) return
    const row = document.elementFromPoint(x, y)?.closest<HTMLElement>('.tree-row[data-path]')
    const at = row?.dataset.path
    const dir = at === undefined ? targetDir : row?.classList.contains('dir') ? at : at.split('/').slice(0, -1).join('/')
    if (writableAt(dir)) void uploadItems(dir, collectListed(files))
  }), [targetDir, uploadItems, writableAt, dialogOpen])

  const selectedFile = display?.kind === 'file' ? display.target : null
  // The folder a link led to, until another row is chosen.
  const [revealed, setRevealed] = useState<Reveal | null>(null)
  useEffect(() => {
    if (!reveal) return
    setRevealed(reveal)
    setRailCollapsed(false)
    if (/(^|\/)\./.test(reveal.path)) setShowHidden(true)
    setTargetDir(reveal.path || uploadsDir)
    selectLibraryEntry({ path: reveal.path, isDir: true })
    // Once for each link.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal])
  useEffect(() => { if (selectedFile) setRevealed(null) }, [selectedFile])
  useEffect(() => onLibraryChange(() => setRevealed(null)), [])
  const replacedNote = replaced && (selectedFile === replaced.path || (!selectedFile && revealed?.path === replaced.path)) ? replaced : null
  const [dropOver, setDropOver] = useState(false)
  // The status panel describes what was last chosen in the tree: the file
  // open in the viewer, or a folder clicked since.
  useEffect(() => { if (selectedFile) selectLibraryEntry({ path: selectedFile, isDir: false }) }, [selectedFile])
  useEffect(() => () => selectLibraryEntry(null), [])

  // On a phone the tree and the open file take the whole width in turn, as a
  // phone's file app does: the tree until a file opens, then the file, with a
  // way back to the tree above it. Side by side, the file had no width left.
  // The tree cannot be collapsed there, since it is the page.
  const phone = usePhoneLayout()
  // Between the phone width and 1200px, where the tree at its width would
  // leave the open file narrower than FILE_MIN, the tree folds to its strip.
  // » brings it back beside that file; « or opening another file folds it
  // again. It stays mounted while folded, so its open folders, selection and
  // uploads are as they were, and the saved collapse setting is not touched.
  // From 1200px up the layout is as before.
  const mid = useMediaQuery(MID_QUERY)
  const cardRef = useRef<HTMLDivElement>(null)
  const stripRef = useRef<HTMLDivElement>(null)
  const [cardWidth, setCardWidth] = useState(0)
  useLayoutEffect(() => {
    const card = cardRef.current
    if (!card || phone) return
    const measure = () => setCardWidth(card.clientWidth)
    measure()
    if (typeof ResizeObserver !== 'function') return
    const ro = new ResizeObserver(measure)
    ro.observe(card)
    return () => ro.disconnect()
  }, [phone])
  // The width kept for this browser, as far as this window has room for it.
  const treeWidest = treeMax(cardWidth)
  const treeWidth = clampTree(railWidth, treeWidest)
  const openKey = display ? JSON.stringify([display.kind, display.target, display.kind === 'file' ? display.lib : undefined]) : null
  // What » asked for holds for the file it was asked beside, not for a later
  // visit to it.
  const [asked, setAsked] = useState({ key: openKey, tree: false })
  if (asked.key !== openKey) setAsked({ key: openKey, tree: false })
  const askTree = (tree: boolean) => setAsked({ key: openKey, tree })
  const crowded = mid && openKey !== null && cardWidth > 0 && cardWidth - treeWidth - DIVIDER < FILE_MIN
  const folded = crowded && !(asked.key === openKey && asked.tree)
  const collapsed = !phone && (crowded ? folded : railCollapsed)
  const treeMounted = !collapsed || (folded && !railCollapsed)
  // Focus inside the tree as it folds goes to the button that brings it back.
  useLayoutEffect(() => {
    if (folded && railRef.current?.contains(document.activeElement)) stripRef.current?.querySelector('button')?.focus()
  }, [folded])

  return (
    <>
    <div
      className={`library${display ? ' has-open' : ''}${dropOver ? ' drop-over' : ''}`}
      onDragOver={e => { e.preventDefault(); setDropOver(true) }}
      onDragLeave={() => setDropOver(false)}
      onDrop={e => { e.preventDefault(); setDropOver(false); if (writableAt(targetDir) || libOffer.allowed) void uploadTo(targetDir, e.dataTransfer.items) }}
    >
      <div ref={cardRef} className="library-card card">
        {collapsed && (
          <div ref={stripRef} className="rail-strip">
            <IconButton icon="»" label="Expand knowledge base tree" onClick={() => { if (crowded) askTree(true); else setRailCollapsed(false) }} />
          </div>
        )}
        {treeMounted && (
          <>
            <aside ref={railRef} className="library-rail" style={{ width: treeWidth, display: collapsed ? 'none' : undefined }}>
              <div className={`rail-head${cfg.libraries && cfg.libraries.length > 1 ? ' rail-head-switch' : ''}`}>
                {cfg.libraries && cfg.libraries.length > 1
                  ? <LibrarySwitch libraries={cfg.libraries} />
                  : <span className="rail-title" title={cfg.kbLabel}>Library</span>}
                <span className="rail-actions">
                  <IconButton
                    label="Show applied labels in the tree"
                    pressed={showLabels}
                    onClick={() => setShowLabels(v => !v)}
                    icon={<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 2h5.2L14 8.8a1 1 0 0 1 0 1.4L10.2 14a1 1 0 0 1-1.4 0L2 7.2V2z"/><circle cx="5.2" cy="5.2" r="1" fill="currentColor" stroke="none"/></svg>}
                  />
                  <IconButton
                    label="Show hidden files and folders"
                    pressed={showHidden}
                    onClick={() => setShowHidden(v => !v)}
                    icon={<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M1.5 8S4 3.5 8 3.5 14.5 8 14.5 8 12 12.5 8 12.5 1.5 8 1.5 8z" /><circle cx="8" cy="8" r="2" /></svg>}
                  />
                  <IconButton
                    label="Select multiple items for labeling or deletion"
                    pressed={selectMode}
                    onClick={() => { setSelectMode(m => !m); setMultiSel(new Set()); setTagMenuOpen(false); setConfirmDelete(false) }}
                    icon={<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><rect x="2" y="2" width="8" height="8" rx="2"/><path d="M4.5 6l1.6 1.6L9 4.7"/><path d="M12.5 6.5v5a1.5 1.5 0 0 1-1.5 1.5H6"/></svg>}
                  />
                  <RefreshButton iconOnly size="sm" onRefresh={() => setRefreshKey(k => k + 1)} />
                  <LibraryAddMenu
                    onAddLibrary={libOffer.allowed ? () => setAddingLibrary(true) : undefined}
                    onImport={libOffer.allowed ? () => setImporting({ dropped: null }) : undefined}
                    readOnly={inAll ? null : readOnlyAt('')}
                    targetDir={targetDir}
                    onTargetDir={setTargetDir}
                    onNewFile={() => newFile(targetDir)}
                    newFileWhy={readOnlyAt(targetDir)}
                    onDone={paths => {
                      setRefreshKey(k => k + 1)
                      if (paths[0]) onDisplay({ kind: 'file', target: paths[0] })
                    }}
                  />
                  {!phone && <IconButton icon="«" label="Collapse tree" onClick={() => { if (crowded) askTree(false); else setRailCollapsed(true) }} />}
                </span>
              </div>
              {replacedNote && !selectedFile && <Callout tone="warning" variant="bar" size="sm" live className="link-replaced" onDismiss={onReplacedRead}>{replacedNote.text}</Callout>}
              {selectMode && (
                <div className="multi-bar">
                  <span>{multiSel.size} selected</span>
                  {selLabelsLocked
                    ? <LockedButton reason={selLabelsLocked}>Labels…</LockedButton>
                    : (
                      <TagMenu
                        paths={selPaths}
                        open={tagMenuOpen}
                        onOpenChange={setTagMenuOpen}
                        align="start"
                        trigger={<Button variant="secondary" size="sm" disabled={!multiSel.size}>Labels…</Button>}
                      />
                    )}
                  {selLocked
                    ? (
                      <>
                        <LockedButton reason={selLocked}>Move to…</LockedButton>
                        <LockedButton reason={selLocked} variant="outline">Delete…</LockedButton>
                      </>
                    )
                    : (
                      <>
                        <Button variant="secondary" size="sm" disabled={!multiSel.size} onClick={e => void openMoveMenu(e.currentTarget)}>Move to…</Button>
                        <Button variant="outline" size="sm" disabled={!multiSel.size} onClick={() => leaveUnsaved(() => setConfirmDelete(true), selPaths)}>Delete…</Button>
                      </>
                    )}
                  <Button variant="ghost" size="sm" className="multi-clear" onClick={() => setMultiSel(new Set())}>clear</Button>
                  <ConfirmModal
                    open={confirmDelete}
                    onClose={() => setConfirmDelete(false)}
                    title={`Delete ${multiSel.size} ${multiSel.size === 1 ? 'item' : 'items'}?`}
                    confirmLabel={`Delete ${multiSel.size}`}
                    destructive
                    onConfirm={bulkDelete}
                  />
                </div>
              )}
              <Explorer
                refreshTick={refreshKey}
                onOpen={p => { anchor.current = p; onDisplay({ kind: 'file', target: p }) }}
                onDirFocus={dir => { anchor.current = dir; setRevealed(null); setTargetDir(dir || uploadsDir); selectLibraryEntry({ path: dir, isDir: true }) }}
                selected={selectedFile}
                reveal={revealed}
                rootLabel={cfg.kbLabel}
                emptyHint={writable
                  ? 'Nothing here yet. Add files with + Add, by dropping them here, or with Transfers.'
                  : 'Nothing here yet.'}
                multiSelected={multiSel}
                onToggleMulti={toggleMulti}
                onDropInto={(dir, items) => { if (writableAt(dir) || libOffer.allowed) void uploadTo(dir, items) }}
                onSelectRange={selectRange}
                onMoveInto={(dir, paths) => void moveInto(dir, paths)}
                onCopyInto={copyInto}
                dragKind={dragKind}
                dropKind={dropKind}
                onLabel={p => { setMultiSel(new Set([p])); setTagMenuOpen(true); setSelectMode(true) }}
                onContext={openContextMenu}
                selectMode={selectMode}
                showLabels={showLabels}
                showHidden={showHidden}
                tagsTick={tagsTick}
              />
              {suggest && (
                <div className="upload-panel suggest-panel">
                  <div className="upload-panel-head">
                    <span>
                      {suggest.busy && !suggest.rows.length
                        ? `Reading ${suggest.dir || 'the library'} and proposing labels…`
                        : suggest.error && !suggest.rows.length
                          ? 'Could not propose labels'
                          : `${suggest.rows.length} of ${suggest.considered ?? 0} unlabelled files have proposals`}
                    </span>
                    <Button variant="ghost" size="sm" onClick={() => setSuggest(null)}>dismiss</Button>
                  </div>
                  {suggest.error && <Callout tone="error" size="sm" className="upload-failed">{suggest.error}</Callout>}
                  {!suggest.busy && !suggest.error && !suggest.rows.length && (
                    <EmptyState
                      variant="bare"
                      title="Nothing to propose here."
                      description="Everything already carries a label, or there was no text to read."
                    />
                  )}
                  {suggest.rows.length > 0 && (
                    <>
                      <div className="suggest-rows">
                        {suggest.rows.map((row, i) => (
                          <label className="suggest-row" key={row.path} title={row.why}>
                            <Checkbox
                              checked={row.accept}
                              onCheckedChange={accept => setSuggest(s2 => s2 && {
                                ...s2,
                                rows: s2.rows.map((r, j) => (j === i ? { ...r, accept } : r)),
                              })}
                            />
                            <span className="suggest-path">{row.path}</span>
                            <span className="suggest-tags">{row.labels.map(l => <code key={l}>{l}</code>)}</span>
                          </label>
                        ))}
                      </div>
                      <div className="suggest-actions">
                        <Button variant="primary" size="sm" loading={suggest.busy} disabled={!suggest.rows.some(r => r.accept)}
                          onClick={() => void applySuggestions()}>
                          {suggest.busy ? 'Applying…' : `Apply ${suggest.rows.filter(r => r.accept).length}`}
                        </Button>
                        <Button variant="secondary" size="sm" onClick={() => setSuggest(s2 => s2 && { ...s2, rows: s2.rows.map(r => ({ ...r, accept: false })) })}>
                          Clear all
                        </Button>
                      </div>
                    </>
                  )}
                </div>
              )}
              <TransferPanel openFile={selectedFile} />
            </aside>
            {!collapsed && <TreeResize width={treeWidth} max={treeWidest} rail={() => railRef.current} onWidth={setRailWidth} />}
          </>
        )}
        {phone && display && (
          <div className="library-back">
            <Button variant="ghost" size="sm" onClick={() => onDisplay(null)}>
              <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><path d="m10 4-4 4 4 4"/></svg>
              Back to the list
            </Button>
          </div>
        )}
        <section className="library-main">
          {replacedNote && selectedFile && <Callout tone="warning" variant="bar" size="sm" live className="link-replaced" onDismiss={onReplacedRead}>{replacedNote.text}</Callout>}
          {display?.kind === 'workflow_dag' ? (
            <DagViewer workflow={display.target} />
          ) : (
            <Viewer
              path={selectedFile}
              find={display?.kind === 'file' ? display.q : undefined}
              onDeleted={() => { onDisplay(null); setRefreshKey(k => k + 1) }}
              onOpen={p => onDisplay({ kind: 'file', target: p })}
              startEditing={startEditing}
              readOnly={selectedFile ? readOnlyAt(selectedFile) : null}
              labelsLocked={selectedFile ? labelsLockedAt(selectedFile) : null}
            />
          )}
        </section>
      </div>
      {contextMenu}
      <input
        ref={filesInput} type="file" multiple hidden aria-hidden="true" tabIndex={-1}
        onChange={e => { void uploadItems(uploadDir.current, collectChosen([...(e.target.files ?? [])])); e.target.value = '' }}
      />
      <input
        ref={el => { folderInput.current = el; el?.setAttribute('webkitdirectory', '') }}
        type="file" hidden aria-hidden="true" tabIndex={-1}
        onChange={e => { void uploadItems(uploadDir.current, collectChosen([...(e.target.files ?? [])])); e.target.value = '' }}
      />
      {copyTo && (
        <ResultsTransferDialog items={copyTo.items} noun={copyTo.noun} copyOnly prefer="library" destination={copyTo.destination}
          labels={Object.fromEntries((cfg.libraries ?? []).map(l => [l.id, l.label]))}
          onClose={() => setCopyTo(null)} onDone={() => setCopyTo(null)} />
      )}
      {shareTarget && (
        <ShareDialog open onClose={() => setShareTarget(null)} library={shareTarget.library} path={shareTarget.path} />
      )}
      {nameDialog && (
        <Modal
          open
          onClose={() => setNameDialog(null)}
          size="sm"
          title={nameDialog.mode === 'rename' ? 'Rename' : nameDialog.mode === 'newfile' ? 'New file' : 'New folder'}
          description={nameDialog.mode === 'newfile' ? `In ${nameDialog.path ? shownPath(nameDialog.path, cfg.libraries) : cfg.kbLabel}` : nameDialog.path || cfg.kbLabel}
          footer={
            <>
              <Button variant="ghost" size="sm" onClick={() => setNameDialog(null)}>Cancel</Button>
              <Button variant="primary" size="sm" loading={nameDialog.busy} disabled={!nameDialog.value.trim()} onClick={() => void submitName()}>
                {nameDialog.mode === 'rename' ? (nameDialog.busy ? 'Renaming…' : 'Rename') : 'Create'}
              </Button>
            </>
          }
        >
          <Input
            aria-label={nameDialog.mode === 'rename' ? 'New name' : nameDialog.mode === 'newfile' ? 'File name' : 'Folder name'}
            value={nameDialog.value}
            placeholder={nameDialog.mode === 'rename' ? 'new name' : nameDialog.mode === 'newfile' ? 'file name' : 'folder name'}
            onChange={e => { const value = e.target.value; setNameDialog(d => d && { ...d, value }) }}
            onFocus={nameDialog.mode !== 'mkdir' ? e => {
              // Select the stem, leaving the extension alone, which is
              // what a rename usually changes and a new file's name needs.
              const dot = e.target.value.lastIndexOf('.')
              e.target.setSelectionRange(0, dot > 0 ? dot : e.target.value.length)
            } : undefined}
            onKeyDown={e => { if (e.key === 'Enter') void submitName() }}
          />
          {nameDialog.error && <Callout tone="error" size="sm" className="name-dialog-error">{nameDialog.error}</Callout>}
        </Modal>
      )}
      <ConfirmModal
        open={deleteTarget !== null}
        onClose={() => setDeleteTarget(null)}
        title={deleteTarget?.isDir ? 'Delete folder and all contents?' : 'Delete this file?'}
        description={deleteTarget ? shownPath(deleteTarget.path, cfg.libraries) : undefined}
        confirmLabel={deleteTarget?.isDir ? 'Delete folder' : 'Delete'}
        destructive
        closeOnConfirm={false}
        onConfirm={ctxDelete}
      >
        {deleteTarget?.error ? <Callout tone="error" size="sm">{deleteTarget.error}</Callout> : undefined}
      </ConfirmModal>
    </div>
    {/* Outside the page's drop target, so a file dropped on a dialog is the
        dialog's alone. */}
    {libOffer.allowed && (
      <>
        <AddLibraryDialog open={addingLibrary} onClose={() => setAddingLibrary(false)} offer={libOffer} onAdded={libraryAdded} />
        <ImportLibrariesDialog open={importing !== null} dropped={importing?.dropped} onClose={() => setImporting(null)} onImported={libraryAdded} />
      </>
    )}
    </>
  )
}

/** The largest .json file a drop reads to see whether it is a library
 *  list; a list is a few kilobytes. */
const LIST_MAX_BYTES = 1 << 20
