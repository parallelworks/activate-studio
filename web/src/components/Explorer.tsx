import { useEffect, useState } from 'react'
import { api, KbEntry } from '../api'

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

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

function DirNode({ path, name, depth, onOpen, onDirFocus, selected }: {
  path: string
  name: string
  depth: number
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
}) {
  const [open, setOpen] = useState(depth === 0)
  const [entries, setEntries] = useState<KbEntry[] | null>(null)

  useEffect(() => {
    if (open && entries === null) {
      api.tree(path).then(r => setEntries(r.entries)).catch(() => setEntries([]))
    }
  }, [open, entries, path])

  return (
    <div>
      {depth > 0 && (
        <div
          className="tree-row dir"
          style={{ paddingLeft: 8 + depth * 14 }}
          onClick={() => { setOpen(o => !o); onDirFocus(path) }}
        >
          <span className="tw">{open ? '▾' : '▸'}</span>
          <FolderIcon open={open} />
          <span className="tree-label">{name}</span>
        </div>
      )}
      {open && entries?.map(e =>
        e.type === 'dir' ? (
          <DirNode key={e.path} path={e.path} name={e.name} depth={depth + 1}
            onOpen={onOpen} onDirFocus={onDirFocus} selected={selected} />
        ) : (
          <div
            key={e.path}
            className={`tree-row file ${selected === e.path ? 'selected' : ''}`}
            style={{ paddingLeft: 8 + (depth + 1) * 14 }}
            onClick={() => onOpen(e.path)}
            title={`${e.path} (${formatSize(e.size)})`}
          >
            <span className="tw" />
            <FileIcon />
            <span className="tree-label">{e.name}</span>
          </div>
        ),
      )}
    </div>
  )
}

export function Explorer({ onOpen, onDirFocus, selected, rootLabel }: {
  onOpen: (path: string) => void
  onDirFocus: (path: string) => void
  selected: string | null
  rootLabel: string
}) {
  return (
    <div className="explorer">
      <DirNode path="" name={rootLabel} depth={0}
        onOpen={onOpen} onDirFocus={onDirFocus} selected={selected} />
    </div>
  )
}
