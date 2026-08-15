import { useEffect, useRef, useState } from 'react'
import { DependencyGraphPreview } from '@parallelworks/ui/workflow'
import '@parallelworks/ui/styles.css'
import '@parallelworks/ui/theme.css'

interface DagData {
  name: string
  displayName?: string
  nodes: { id: string; steps: string[]; dependsOn?: string[] }[]
  edges: { from: string; to: string }[]
  /** The workflow document, rendered by the platform's own graph. */
  yaml?: Record<string, unknown>
  form?: Record<string, { label?: string; type?: string; default?: unknown; tooltip?: string }> | null
  yamlText?: string
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
  const [tab, setTab] = useState<'dag' | 'inputs' | 'yaml'>('dag')
  // Pan/zoom viewBox; null = fit the whole DAG to the canvas.
  const [view, setView] = useState<{ x: number; y: number; w: number; h: number } | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const dragRef = useRef<{ px: number; py: number; view: { x: number; y: number; w: number; h: number } } | null>(null)
  const movedRef = useRef(false)

  useEffect(() => {
    setDag(null)
    setError(null)
    setSelectedNode(null)
    setView(null)
    fetch(`/api/workflows/${encodeURIComponent(workflow)}`)
      .then(r => (r.ok ? r.json() : Promise.reject(new Error(`${r.status}`))))
      .then(setDag)
      .catch(e => setError(String(e)))
  }, [workflow])

  if (error) return <div className="viewer"><p className="error">Could not load workflow {workflow}: {error}</p></div>
  if (!dag) return <div className="viewer"><p className="muted pad">Loading workflow {workflow}…</p></div>

  const { pos, width, height } = layout(dag)
  const selected = dag.nodes.find(n => n.id === selectedNode)

  // Default view fits the entire DAG with padding; interactions replace it.
  const fitView = { x: -20, y: -20, w: width + 40, h: height + 40 }
  const v = view ?? fitView

  const clientToView = (e: { clientX: number; clientY: number }) => {
    const r = svgRef.current!.getBoundingClientRect()
    return {
      x: v.x + ((e.clientX - r.left) / r.width) * v.w,
      y: v.y + ((e.clientY - r.top) / r.height) * v.h,
    }
  }

  const zoom = (factor: number, cx?: number, cy?: number) => {
    const center = { x: cx ?? v.x + v.w / 2, y: cy ?? v.y + v.h / 2 }
    const w = Math.min(Math.max(v.w * factor, 120), fitView.w * 4)
    const h = w * (v.h / v.w)
    setView({ x: center.x - (center.x - v.x) * (w / v.w), y: center.y - (center.y - v.y) * (h / v.h), w, h })
  }

  const onWheel = (e: React.WheelEvent) => {
    e.preventDefault()
    const p = clientToView(e)
    zoom(e.deltaY > 0 ? 1.15 : 1 / 1.15, p.x, p.y)
  }

  const onPointerDown = (e: React.PointerEvent) => {
    movedRef.current = false
    dragRef.current = { px: e.clientX, py: e.clientY, view: v }
    ;(e.target as Element).setPointerCapture?.(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = dragRef.current
    if (!d) return
    const r = svgRef.current!.getBoundingClientRect()
    const dx = ((e.clientX - d.px) / r.width) * d.view.w
    const dy = ((e.clientY - d.py) / r.height) * d.view.h
    if (Math.abs(e.clientX - d.px) + Math.abs(e.clientY - d.py) > 4) movedRef.current = true
    if (movedRef.current) setView({ ...d.view, x: d.view.x - dx, y: d.view.y - dy })
  }
  const onPointerUp = () => { dragRef.current = null }

  return (
    <div className="viewer">
      <div className="viewer-head">
        <span className="viewer-path">workflow: {dag.name}{dag.displayName && dag.displayName !== dag.name ? ` (${dag.displayName})` : ''}</span>
        <span className="viewer-meta">{dag.nodes.length} jobs, {dag.edges.length} dependencies</span>
      </div>
      {dag.yamlText && (
        <div className="viewer-tabs">
          <button className={tab === 'dag' ? 'active' : ''} onClick={() => setTab('dag')}>DAG</button>
          {dag.form && Object.keys(dag.form).length > 0 && (
            <button className={tab === 'inputs' ? 'active' : ''} onClick={() => setTab('inputs')}>Inputs</button>
          )}
          <button className={tab === 'yaml' ? 'active' : ''} onClick={() => setTab('yaml')}>workflow.yaml</button>
        </div>
      )}
      {tab === 'yaml' && dag.yamlText ? (
        <div className="viewer-body"><pre className="text-body">{dag.yamlText}</pre></div>
      ) : tab === 'inputs' && dag.form ? (
        // What a run of this workflow asks for, as the platform's parser
        // resolves it from the document.
        <div className="viewer-body">
          <table className="rag-calls-table wf-inputs">
            <thead><tr><th>Input</th><th>Type</th><th>Default</th><th>Description</th></tr></thead>
            <tbody>
              {Object.entries(dag.form).map(([id, f]) => (
                <tr key={id}>
                  <td>{f?.label || id}</td>
                  <td><code>{f?.type ?? 'string'}</code></td>
                  <td>{f?.default === undefined || f?.default === '' ? '-' : <code>{String(f.default)}</code>}</td>
                  <td>{f?.tooltip ?? ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : dag.yaml ? (
        // The platform's own dependency graph, so the workflow reads the
        // same here as it does in ACTIVATE.
        <div className="dag-canvas platform-dag">
          {/* The platform's own graph. Its run view (per-step status icons)
              needs an actual run; a definition renders as job cards, which
              is what ACTIVATE shows for a workflow too. */}
          <DependencyGraphPreview yml={dag.yaml} removeBorder />
        </div>
      ) : (
      <div className="dag-canvas own-dag">
        <div className="dag-controls">
          <button title="Zoom in" onClick={() => zoom(1 / 1.3)}>+</button>
          <button title="Zoom out" onClick={() => zoom(1.3)}>−</button>
          <button title="Fit the whole workflow" onClick={() => setView(null)}>Fit</button>
        </div>
        <svg
          ref={svgRef}
          className={dragRef.current ? 'dragging' : ''}
          viewBox={`${v.x} ${v.y} ${v.w} ${v.h}`}
          preserveAspectRatio="xMidYMid meet"
          onWheel={onWheel}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerLeave={onPointerUp}
        >
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
              <g key={n.id} transform={`translate(${p.x}, ${p.y})`} onClick={() => { if (!movedRef.current) setSelectedNode(n.id) }} style={{ cursor: 'pointer' }}>
                <rect width={NODE_W} height={NODE_H} rx="8"
                  fill={active ? 'var(--pw-active-pill)' : 'var(--pw-panel)'} stroke={active ? 'var(--pw-navy)' : 'var(--pw-border-strong)'} strokeWidth={active ? 1.8 : 1.2} />
                <text x="12" y="22" fontSize="12.5" fontWeight="600" fill="var(--pw-navy)">{n.id.slice(0, 24)}</text>
                <text x="12" y="40" fontSize="10.5" fill="var(--pw-muted)">{n.steps.length} step{n.steps.length === 1 ? '' : 's'}</text>
              </g>
            )
          })}
        </svg>
      </div>
      )}
      {tab === 'dag' && selected && (
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
