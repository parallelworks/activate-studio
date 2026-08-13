import { useEffect, useState } from 'react'
import { api, KbEntry } from '../api'

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

function DirNode({ path, name, depth, onOpen, selected }: {
  path: string
  name: string
  depth: number
  onOpen: (path: string) => void
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
        <div className="tree-row dir" style={{ paddingLeft: depth * 14 }} onClick={() => setOpen(o => !o)}>
          <span className="tw">{open ? '▾' : '▸'}</span> {name}
        </div>
      )}
      {open && entries?.map(e =>
        e.type === 'dir' ? (
          <DirNode key={e.path} path={e.path} name={e.name} depth={depth + 1} onOpen={onOpen} selected={selected} />
        ) : (
          <div
            key={e.path}
            className={`tree-row file ${selected === e.path ? 'selected' : ''}`}
            style={{ paddingLeft: (depth + 1) * 14 }}
            onClick={() => onOpen(e.path)}
            title={`${e.path} (${formatSize(e.size)})`}
          >
            <span className="tw"> </span>{e.name}
          </div>
        ),
      )}
    </div>
  )
}

export function Explorer({ onOpen, selected }: { onOpen: (path: string) => void; selected: string | null }) {
  return (
    <div className="explorer">
      <DirNode path="" name="PW_KNOWLEDGE_BASE" depth={0} onOpen={onOpen} selected={selected} />
    </div>
  )
}
