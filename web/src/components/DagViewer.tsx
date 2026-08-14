import { useEffect, useState } from 'react'

interface DagData {
  name: string
  displayName?: string
  nodes: { id: string; steps: string[] }[]
  edges: { from: string; to: string }[]
}

const NODE_W = 190
const NODE_H = 54
const GAP_X = 70
const GAP_Y = 24

/** Longest-path layering: a node sits one level right of its deepest dependency. */
function layout(dag: DagData) {
  const levels = new Map<string, number>()
  const level = (id: string, seen: string[] = []): number => {
    if (levels.has(id)) return levels.get(id)!
    if (seen.includes(id)) return 0
    const deps = dag.edges.filter(e => e.to === id).map(e => e.from)
    const l = deps.length ? Math.max(...deps.map(d => level(d, [...seen, id]))) + 1 : 0
    levels.set(id, l)
    return l
  }
  dag.nodes.forEach(n => level(n.id))
  const byLevel = new Map<number, string[]>()
  for (const n of dag.nodes) {
    const l = levels.get(n.id) ?? 0
    byLevel.set(l, [...(byLevel.get(l) ?? []), n.id])
  }
  const maxRows = Math.max(...[...byLevel.values()].map(v => v.length), 1)
  const height = maxRows * (NODE_H + GAP_Y) + GAP_Y
  const pos = new Map<string, { x: number; y: number }>()
  for (const [l, ids] of byLevel) {
    const columnH = ids.length * (NODE_H + GAP_Y) - GAP_Y
    ids.forEach((id, i) => {
      pos.set(id, {
        x: 20 + l * (NODE_W + GAP_X),
        y: (height - columnH) / 2 + i * (NODE_H + GAP_Y),
      })
    })
  }
  const width = 40 + ([...byLevel.keys()].length) * (NODE_W + GAP_X) - GAP_X
  return { pos, width, height }
}

export function DagViewer({ workflow }: { workflow: string }) {
  const [dag, setDag] = useState<DagData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [selectedNode, setSelectedNode] = useState<string | null>(null)

  useEffect(() => {
    setDag(null)
    setError(null)
    setSelectedNode(null)
    fetch(`/api/workflows/${encodeURIComponent(workflow)}`)
      .then(r => (r.ok ? r.json() : Promise.reject(new Error(`${r.status}`))))
      .then(setDag)
      .catch(e => setError(String(e)))
  }, [workflow])

  if (error) return <div className="viewer"><p className="error">Could not load workflow {workflow}: {error}</p></div>
  if (!dag) return <div className="viewer"><p className="muted pad">Loading workflow {workflow}…</p></div>

  const { pos, width, height } = layout(dag)
  const selected = dag.nodes.find(n => n.id === selectedNode)

  return (
    <div className="viewer">
      <div className="viewer-head">
        <span className="viewer-path">workflow: {dag.name}{dag.displayName && dag.displayName !== dag.name ? ` (${dag.displayName})` : ''}</span>
        <span className="viewer-meta">{dag.nodes.length} jobs, {dag.edges.length} dependencies</span>
      </div>
      <div className="dag-canvas">
        <svg width={Math.max(width, 400)} height={Math.max(height, 200)}>
          <defs>
            <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
              <path d="M 0 1 L 9 5 L 0 9 z" fill="var(--pw-muted-2)" />
            </marker>
          </defs>
          {dag.edges.map((e, i) => {
            const a = pos.get(e.from)
            const b = pos.get(e.to)
            if (!a || !b) return null
            const x1 = a.x + NODE_W
            const y1 = a.y + NODE_H / 2
            const x2 = b.x - 4
            const y2 = b.y + NODE_H / 2
            const mx = (x1 + x2) / 2
            return (
              <path key={i} d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                fill="none" stroke="var(--pw-muted-2)" strokeWidth="1.6" markerEnd="url(#arrow)" />
            )
          })}
          {dag.nodes.map(n => {
            const p = pos.get(n.id)
            if (!p) return null
            const active = n.id === selectedNode
            return (
              <g key={n.id} transform={`translate(${p.x}, ${p.y})`} onClick={() => setSelectedNode(n.id)} style={{ cursor: 'pointer' }}>
                <rect width={NODE_W} height={NODE_H} rx="8"
                  fill={active ? 'var(--pw-active-pill)' : 'var(--pw-panel)'} stroke={active ? 'var(--pw-navy)' : 'var(--pw-border-strong)'} strokeWidth={active ? 1.8 : 1.2} />
                <text x="12" y="22" fontSize="12.5" fontWeight="600" fill="var(--pw-navy)">{n.id.slice(0, 24)}</text>
                <text x="12" y="40" fontSize="10.5" fill="var(--pw-muted)">{n.steps.length} step{n.steps.length === 1 ? '' : 's'}</text>
              </g>
            )
          })}
        </svg>
      </div>
      {selected && (
        <div className="dag-steps">
          <div className="dag-steps-title">{selected.id}</div>
          <ol>
            {selected.steps.map((s, i) => <li key={i}>{s}</li>)}
          </ol>
        </div>
      )}
    </div>
  )
}
