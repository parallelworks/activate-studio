import React, { useEffect, useRef, useState } from 'react'
import { Checkbox, IconButton } from '@parallelworks/ui'
import { api, KbEntry } from '../api'
import { nameTip } from './NameTip'

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

const Chevron = ({ open }: { open: boolean }) => (
  <svg viewBox="0 0 12 12" width="12" height="12" className="chev" style={{ transform: open ? 'rotate(90deg)' : 'none' }}>
    <path d="M4 2.5 8 6l-4 3.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const FolderIcon = ({ open }: { open: boolean }) => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.3" className="glyph folder">
    {open
      ? <path d="M1.5 6.5h11l-1.6 6h-9.4l-1-8h4l1.5 2" />
      : <path d="M1.5 3.5h4l1.5 2h7v7h-12.5v-9z" />}
  </svg>
)

const FileIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.3" className="glyph file">
    <path d="M4 1.5h5.5L12.5 5v9.5h-9v-13z" /><path d="M9.5 1.5V5h3" />
  </svg>
)

/** A row's selection box. The span keeps a press on the box from also
 *  reaching the row, which would open the file. */
export const RowCheck = ({ checked, onToggle }: { checked: boolean; onToggle: () => void }) => (
  <span className="row-check" title="Select for multi-item labeling" onClick={e => e.stopPropagation()}>
    <Checkbox checked={checked} onCheckedChange={() => onToggle()} aria-label="Select for multi-item labeling" />
  </span>
)

const TreeTags = ({ tags, show }: { tags?: string[]; show: boolean }) => {
  if (!show || !tags?.length) return null
  return (
    <span className="tree-tags">
      {tags.slice(0, 2).map(t => <span key={t} className="tree-tag">{t}</span>)}
      {tags.length > 2 && <span className="tree-tag">+{tags.length - 2}</span>}
    </span>
  )
}

const TagIcon = () => (
  <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4">
    <path d="M2 2h5.2L14 8.8a1 1 0 0 1 0 1.4L10.2 14a1 1 0 0 1-1.4 0L2 7.2V2z" /><circle cx="5.2" cy="5.2" r="1" fill="currentColor" stroke="none" />
  </svg>
)

interface NodeProps {
  /** What the top of an empty library says (how to add to it, or that it
   *  has nothing yet). */
  emptyHint?: string
  path: string
  name: string
  depth: number
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
  multiSelected: Set<string>
  onToggleMulti: (path: string) => void
  onDropInto: (dir: string, items: DataTransferItemList) => void
  /** Shift-click: select every row between the last click and this one. */
  onSelectRange: (path: string) => void
  /** Internal drag: move these corpus paths into this directory. */
  onMoveInto: (dir: string, paths: string[]) => void
  /** Internal drag across libraries: copy these paths into this directory. */
  onCopyInto: (dir: string, paths: string[]) => void
  /** What a drag that starts on this row may do; null: it does not drag. */
  dragKind: (path: string) => DragKind
  /** What dropping these paths on this folder does: none when it cannot
   *  take them (a read-only library, or a folder into itself). */
  dropKind: (dir: string, paths: string[]) => DropKind
  /** The paths being dragged in the tree now, null for none. */
  dragging: string[] | null
  onDragging: (paths: string[] | null) => void
  onLabel: (path: string) => void
  onContext: (path: string, isDir: boolean, x: number, y: number) => void
  selectMode: boolean
  showLabels: boolean
  showHidden: boolean
  tagsTick: number
  /** Bumping re-fetches every open directory without collapsing the tree. */
  refreshTick: number
  tags?: string[]
  /** A folder to open to, with the folders above it, and bring into view. */
  reveal?: { path: string } | null
}

/** Corpus paths carried by an internal drag. A drag from the desktop has
 *  no such type, which is how a move is told apart from an upload. */
const DRAG_TYPE = 'application/x-studio-paths'

/** A drag from a row: a move within its library, a copy to another one
 *  (from a read-only library, only that), or either. */
export type DragKind = 'move' | 'copy' | 'copyMove' | null
export type DropKind = 'move' | 'copy' | 'none'

/** The drag under way in the tree: a drop target cannot read the paths
 *  until the drop, so they are kept here from its start. */
let current: string[] | null = null

function startRowDrag(e: React.DragEvent, path: string, multiSelected: Set<string>, kind: DragKind, onDragging: (p: string[] | null) => void): void {
  // Dragging one of several selected rows moves the whole selection.
  const paths = multiSelected.has(path) ? [...multiSelected] : [path]
  e.dataTransfer.setData(DRAG_TYPE, JSON.stringify(paths))
  e.dataTransfer.effectAllowed = kind ?? 'none'
  current = paths
  // Shown once the browser has taken its drag image of the row.
  setTimeout(() => { if (current === paths) onDragging(paths) }, 0)

  // What is being dragged should be visible while dragging it: one row
  // shows its name, several show how many.
  const chip = document.createElement('div')
  chip.className = 'drag-chip'
  chip.textContent = paths.length > 1
    ? `${paths.length} items`
    : (path.split('/').pop() ?? path)
  document.body.appendChild(chip)
  e.dataTransfer.setDragImage(chip, 12, 12)
  // The browser snapshots the element for the drag image, so it can go as
  // soon as the current task yields.
  setTimeout(() => chip.remove(), 0)
}

function endRowDrag(onDragging: (p: string[] | null) => void): void {
  current = null
  onDragging(null)
}

/** A drag over a folder row: whether it takes it, with the cursor that
 *  says so. A drag from the desktop (an upload) is left as it was. */
function overRow(e: React.DragEvent, dir: string, dropKind: NodeProps['dropKind']): DropKind | 'upload' {
  if (!e.dataTransfer.types.includes(DRAG_TYPE)) return 'upload'
  const kind = current ? dropKind(dir, current) : 'none'
  e.dataTransfer.dropEffect = kind
  return kind
}

function draggedPaths(e: React.DragEvent): string[] {
  try {
    const raw = e.dataTransfer.getData(DRAG_TYPE)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter(p => typeof p === 'string') : []
  } catch { return [] }
}

/** What a row's tip says under its name: its folder, and a file's size. */
const rowDetail = (path = '', size?: number): string =>
  [path.includes('/') ? path.slice(0, path.lastIndexOf('/') + 1) : '', size === undefined ? '' : formatSize(size)].filter(Boolean).join(' · ')

/** The row the keyboard's keys are for, when it is a row's own key and not
 *  one for a button in it. */
const keyRow = (e: React.KeyboardEvent): HTMLElement | null => {
  const el = e.target as HTMLElement
  return el.matches('.tree-row[data-path]') && !(e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) ? el : null
}

/** One row of the tree takes Tab, and the arrow keys move from it, as in a
 *  file explorer: up and down a row, right and left to open and close a
 *  folder, Enter to open. Rows come and go with their folders, so which one
 *  takes Tab is kept on the rows themselves. */
function onTreeKey(e: React.KeyboardEvent<HTMLElement>): void {
  const row = keyRow(e)
  if (!row) return
  const rows = [...e.currentTarget.querySelectorAll<HTMLElement>('.tree-row[data-path]')]
  const at = rows.indexOf(row)
  const open = row.getAttribute('aria-expanded')
  const parent = () => {
    const up = (row.dataset.path ?? '').split('/').slice(0, -1).join('/')
    return rows.find(r => up && r.dataset.path === up)
  }
  let to: HTMLElement | undefined
  switch (e.key) {
    case 'ArrowDown': to = rows[at + 1]; break
    case 'ArrowUp': to = rows[at - 1]; break
    case 'Home': to = rows[0]; break
    case 'End': to = rows[rows.length - 1]; break
    case 'ArrowRight': if (open === 'false') row.click(); else if (open === 'true') to = rows[at + 1]; break
    case 'ArrowLeft': if (open === 'true') row.click(); else to = parent(); break
    case 'Enter': case ' ': row.click(); break
    default: return
  }
  e.preventDefault()
  // Marked here for the ring: WebKitGTK gives no :focus-visible to a focus a
  // key moves after a click.
  e.currentTarget.dataset.keys = ''
  to?.focus()
}

/** The row that takes Tab: the one last focused, else the open file's, else the first. */
function keepTabStop(tree: HTMLElement, row?: HTMLElement): void {
  const rows = tree.querySelectorAll<HTMLElement>('.tree-row[data-path]')
  const now = [...rows].filter(r => r.tabIndex === 0)
  const want = row ?? now[0] ?? tree.querySelector<HTMLElement>('.tree-row.selected[data-path]') ?? rows[0]
  for (const r of now) if (r !== want) r.tabIndex = -1
  if (want) want.tabIndex = 0
}

/** Visible rows in the order they are on screen, read from the tree itself
 *  rather than tracked separately: the range a shift-click means is the one
 *  the reader can see. */
export function visibleRowPaths(): string[] {
  return [...document.querySelectorAll<HTMLElement>('.tree-row[data-path]')]
    .map(el => el.dataset.path ?? '')
    .filter(Boolean)
}

function DirNode(props: NodeProps) {
  const { path, name, depth, onOpen, onDirFocus, selected, multiSelected, onToggleMulti, onSelectRange, onMoveInto, onCopyInto, onDropInto, onLabel, onContext, selectMode, showLabels, showHidden, tagsTick, refreshTick, tags, dragKind, dropKind, dragging, onDragging, reveal } = props
  const [open, setOpen] = useState(depth === 0)
  const [entries, setEntries] = useState<KbEntry[] | null>(null)
  // Why the folder could not be listed, said at the top of the tree: an
  // empty tree there would look like an empty library.
  const [failed, setFailed] = useState('')
  const [dropOver, setDropOver] = useState(false)

  // Re-fetch on every open and on tagsTick so a just-applied label shows
  // without collapsing the expanded tree.
  useEffect(() => {
    if (open) {
      api.tree(path, showHidden)
        .then(r => { setEntries(r.entries); setFailed('') })
        .catch(e => { setEntries([]); setFailed(String((e as Error)?.message ?? e)) })
    }
  }, [open, path, tagsTick, refreshTick, showHidden])

  // Reveal the selected file: expand every ancestor directory on the way.
  useEffect(() => {
    if (selected && path && (selected === path || selected.startsWith(path + '/'))) setOpen(true)
  }, [selected, path])

  // The same for a folder a link leads to, which opens itself as well.
  const row = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!reveal || !path || !(reveal.path === path || reveal.path.startsWith(path + '/'))) return
    setOpen(true)
    if (reveal.path === path) row.current?.scrollIntoView({ block: 'center' })
  }, [reveal, path])

  // Where the list does not hold it (a folder newer than the index, or one
  // the Library leaves out), that is said where it would be.
  const leads = reveal && reveal.path !== path && (path === '' ? reveal.path !== '' : reveal.path.startsWith(path + '/'))
  const missing = leads && open && entries && !failed
    && !entries.some(e => e.type === 'dir' && (reveal.path === e.path || reveal.path.startsWith(e.path + '/')))
  // While a row is dragged, each folder says whether it takes the drop:
  // a copy into another library, a move within this one, or neither.
  const takes = dragging && depth > 0 ? dropKind(path, dragging) : null
  const kind = dragKind(path)
  return (
    <div>
      {depth > 0 && (
        <div
          ref={row}
          role="treeitem" aria-level={depth} aria-expanded={open} aria-selected={reveal?.path === path || multiSelected.has(path)} tabIndex={-1}
          {...nameTip(name, rowDetail(path))}
          data-path={path}
          data-drop={takes ?? undefined}
          className={`tree-row dir ${reveal?.path === path ? 'selected' : ''} ${multiSelected.has(path) ? 'multi' : ''} ${dropOver ? 'drop-target' : ''} ${takes === 'none' ? 'drop-refused' : takes ? 'drop-ok' : ''}`}
          style={{ paddingLeft: 8 + depth * 14 }}
          draggable={kind !== null}
          onDragStart={e => { e.stopPropagation(); if (kind) startRowDrag(e, path, multiSelected, kind, onDragging); else e.preventDefault() }}
          onDragEnd={() => endRowDrag(onDragging)}
          onDragOver={e => {
            e.stopPropagation()
            const over = overRow(e, path, dropKind)
            if (over === 'none') { setDropOver(false); return }
            e.preventDefault()
            setDropOver(true)
          }}
          onDragLeave={() => setDropOver(false)}
          onDrop={e => {
            e.preventDefault()
            e.stopPropagation()
            setDropOver(false)
            const dragged = draggedPaths(e)
            endRowDrag(onDragging)
            if (!dragged.length) { onDropInto(path, e.dataTransfer.items); return }
            const k = dropKind(path, dragged)
            if (k === 'copy') onCopyInto(path, dragged)
            else if (k === 'move') onMoveInto(path, dragged)
          }}
          onClick={e => {
            if (e.shiftKey) { onSelectRange(path); return }
            if (e.ctrlKey || e.metaKey) { onToggleMulti(path); return }
            setOpen(o => !o)
            onDirFocus(path)
          }}
          onContextMenu={e => { if (path) { e.preventDefault(); e.stopPropagation(); onContext(path, true, e.clientX, e.clientY) } }}
        >
          {selectMode && <RowCheck checked={multiSelected.has(path)} onToggle={() => onToggleMulti(path)} />}
          <Chevron open={open} />
          <FolderIcon open={open} />
          <span className="tree-text">
            <span className="tree-label" data-tip-text="">{name}</span>
            <TreeTags tags={tags} show={showLabels} />
          </span>
          <IconButton className="row-tag" data-tip-cover="" size="xs" icon={<TagIcon />} label="Labels for this directory (applies to everything beneath it)"
            onClick={e => { e.stopPropagation(); onLabel(path) }} />
        </div>
      )}
      {depth === 0 && open && entries?.length === 0 && (
        failed
          ? <p className="tree-empty" role="alert">This library could not be listed: {failed}</p>
          : <p className="tree-empty">{props.emptyHint ?? 'This library is empty.'}</p>
      )}
      {missing && (
        <p className="tree-empty tree-missing" role="status" ref={el => el?.scrollIntoView({ block: 'nearest' })} style={{ paddingLeft: 8 + (depth + 1) * 14 }}>
          The folder the link leads to is not listed here. It may not be in the library's index yet.
        </p>
      )}
      {open && entries?.map(e =>
        e.type === 'dir' ? (
          <DirNode key={e.path} {...props} path={e.path} name={e.name} depth={depth + 1} tags={e.tags} />
        ) : (
          <div
            key={e.path}
            ref={el => { if (el && selected === e.path) el.scrollIntoView({ block: 'nearest' }) }}
            role="treeitem" aria-level={depth + 1} aria-selected={selected === e.path || multiSelected.has(e.path)} tabIndex={-1}
            {...nameTip(e.name, rowDetail(e.path, e.size))}
            data-path={e.path}
            className={`tree-row file ${selected === e.path ? 'selected' : ''} ${multiSelected.has(e.path) ? 'multi' : ''}`}
            style={{ paddingLeft: 8 + (depth + 1) * 14 }}
            draggable={dragKind(e.path) !== null}
            onDragStart={ev => {
              ev.stopPropagation()
              const k = dragKind(e.path)
              if (k) startRowDrag(ev, e.path, multiSelected, k, onDragging)
              else ev.preventDefault()
            }}
            onDragEnd={() => endRowDrag(onDragging)}
            onClick={ev => {
              if (ev.shiftKey) { onSelectRange(e.path); return }
              if (ev.ctrlKey || ev.metaKey) { onToggleMulti(e.path); return }
              onOpen(e.path)
            }}
            onContextMenu={ev => { ev.preventDefault(); ev.stopPropagation(); onContext(e.path, false, ev.clientX, ev.clientY) }}
          >
            {selectMode && <RowCheck checked={multiSelected.has(e.path)} onToggle={() => onToggleMulti(e.path)} />}
            <span className="chev-space" />
            <FileIcon />
            <span className="tree-text">
              <span className="tree-label" data-tip-text="">{e.name}</span>
              <TreeTags tags={e.tags} show={showLabels} />
            </span>
            <IconButton className="row-tag" data-tip-cover="" size="xs" icon={<TagIcon />} label="Labels for this file"
              onClick={ev => { ev.stopPropagation(); onLabel(e.path) }} />
          </div>
        ),
      )}
    </div>
  )
}

export function Explorer({ emptyHint, onOpen, onDirFocus, selected, rootLabel, multiSelected, onToggleMulti, onSelectRange, onMoveInto, onCopyInto, onDropInto, onLabel, onContext, selectMode, showLabels, showHidden, tagsTick, refreshTick, dragKind, dropKind, reveal }: {
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
  rootLabel: string
  /** What an empty library says at the top of the tree. */
  emptyHint?: string
  multiSelected: Set<string>
  onToggleMulti: (path: string) => void
  onDropInto: (dir: string, items: DataTransferItemList) => void
  onSelectRange: (path: string) => void
  onMoveInto: (dir: string, paths: string[]) => void
  onCopyInto: (dir: string, paths: string[]) => void
  onLabel: (path: string) => void
  onContext: (path: string, isDir: boolean, x: number, y: number) => void
  selectMode: boolean
  showLabels: boolean
  showHidden: boolean
  tagsTick: number
  refreshTick: number
  dragKind: (path: string) => DragKind
  dropKind: (dir: string, paths: string[]) => DropKind
  reveal?: { path: string } | null
}) {
  const [dragging, setDragging] = useState<string[] | null>(null)
  const tree = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = tree.current
    if (!el) return
    keepTabStop(el)
    const seen = new MutationObserver(() => keepTabStop(el))
    seen.observe(el, { childList: true, subtree: true })
    return () => seen.disconnect()
  }, [])
  return (
    <div
      ref={tree}
      role="tree" aria-label={rootLabel} aria-multiselectable
      onKeyDown={onTreeKey}
      onPointerDown={e => { delete e.currentTarget.dataset.keys }}
      onFocus={e => { if (e.target.matches('.tree-row[data-path]')) keepTabStop(e.currentTarget, e.target) }}
      className={`explorer ${selectMode ? 'has-sel' : ''} ${dragging ? 'dragging' : ''}`}
      // Right-clicking the empty space below the tree targets the root, so a
      // folder can be created at the top level; rows handle their own menu.
      onContextMenu={e => {
        if ((e.target as HTMLElement).closest('.tree-row')) return
        e.preventDefault()
        onContext('', true, e.clientX, e.clientY)
      }}
    >
      <DirNode
        path="" name={rootLabel} depth={0} emptyHint={emptyHint}
        onOpen={onOpen} onDirFocus={onDirFocus} selected={selected}
        multiSelected={multiSelected} onToggleMulti={onToggleMulti}
        onDropInto={onDropInto} onSelectRange={onSelectRange} onMoveInto={onMoveInto} onCopyInto={onCopyInto}
        dragKind={dragKind} dropKind={dropKind} dragging={dragging} onDragging={setDragging}
        onLabel={onLabel} onContext={onContext} selectMode={selectMode}
        showLabels={showLabels} showHidden={showHidden} tagsTick={tagsTick} refreshTick={refreshTick} reveal={reveal}
      />
    </div>
  )
}
