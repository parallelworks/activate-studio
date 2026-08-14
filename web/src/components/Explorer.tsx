import { useEffect, useState } from 'react'
import { api, KbEntry } from '../api'

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

interface NodeProps {
  path: string
  name: string
  depth: number
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
  multiSelected: Set<string>
  onToggleMulti: (path: string) => void
  onDropInto: (dir: string, items: DataTransferItemList) => void
}

function DirNode(props: NodeProps) {
  const { path, name, depth, onOpen, onDirFocus, selected, multiSelected, onToggleMulti, onDropInto } = props
  const [open, setOpen] = useState(depth === 0)
  const [entries, setEntries] = useState<KbEntry[] | null>(null)
  const [dropOver, setDropOver] = useState(false)

  useEffect(() => {
    if (open && entries === null) {
      api.tree(path).then(r => setEntries(r.entries)).catch(() => setEntries([]))
    }
  }, [open, entries, path])

  // Reveal the selected file: expand every ancestor directory on the way.
  useEffect(() => {
    if (selected && path && (selected === path || selected.startsWith(path + '/'))) setOpen(true)
  }, [selected, path])

  return (
    <div>
      {depth > 0 && (
        <div
          className={`tree-row dir ${multiSelected.has(path) ? 'multi' : ''} ${dropOver ? 'drop-target' : ''}`}
          style={{ paddingLeft: 8 + depth * 14 }}
          onDragOver={e => { e.preventDefault(); e.stopPropagation(); setDropOver(true) }}
          onDragLeave={() => setDropOver(false)}
          onDrop={e => {
            e.preventDefault()
            e.stopPropagation()
            setDropOver(false)
            onDropInto(path, e.dataTransfer.items)
          }}
          onClick={e => {
            if (e.ctrlKey || e.metaKey) { onToggleMulti(path); return }
            setOpen(o => !o)
            onDirFocus(path)
          }}
        >
          <Chevron open={open} />
          <FolderIcon open={open} />
          <span className="tree-label">{name}</span>
        </div>
      )}
      {open && entries?.map(e =>
        e.type === 'dir' ? (
          <DirNode key={e.path} {...props} path={e.path} name={e.name} depth={depth + 1} />
        ) : (
          <div
            key={e.path}
            ref={el => { if (el && selected === e.path) el.scrollIntoView({ block: 'nearest' }) }}
            className={`tree-row file ${selected === e.path ? 'selected' : ''} ${multiSelected.has(e.path) ? 'multi' : ''}`}
            style={{ paddingLeft: 8 + (depth + 1) * 14 }}
            onClick={ev => (ev.ctrlKey || ev.metaKey ? onToggleMulti(e.path) : onOpen(e.path))}
            title={`${e.path} (${formatSize(e.size)})`}
          >
            <span className="chev-space" />
            <FileIcon />
            <span className="tree-label">{e.name}</span>
          </div>
        ),
      )}
    </div>
  )
}

export function Explorer({ onOpen, onDirFocus, selected, rootLabel, multiSelected, onToggleMulti, onDropInto }: {
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
  rootLabel: string
  multiSelected: Set<string>
  onToggleMulti: (path: string) => void
  onDropInto: (dir: string, items: DataTransferItemList) => void
}) {
  return (
    <div className="explorer">
      <DirNode
        path="" name={rootLabel} depth={0}
        onOpen={onOpen} onDirFocus={onDirFocus} selected={selected}
        multiSelected={multiSelected} onToggleMulti={onToggleMulti}
        onDropInto={onDropInto}
      />
    </div>
  )
}
