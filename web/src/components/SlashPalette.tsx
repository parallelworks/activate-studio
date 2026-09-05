import { useEffect, useRef, useState } from 'react'

/**
 * A command palette for the composer: type "/" and the slash-invocable
 * things appear (meta commands, skills, agents, tools), filtered as you
 * type, chosen with the arrow keys and Enter or Tab. The composer is the
 * chat package's, so this attaches to its textarea from outside: a native
 * listener in the capture phase sees the keystroke before the package's
 * delegated handler, and stopping propagation there is what keeps Enter
 * from sending a half-typed command. Acceptance writes the value through
 * the native setter and dispatches an input event, which is how a
 * controlled React textarea learns about text it did not type itself.
 */
interface Item { name: string; kind: 'command' | 'skill' | 'agent' | 'tool'; description: string }

const META: Item[] = [
  { name: 'help', kind: 'command', description: 'List every slash command, skill, agent, and tool' },
  { name: 'tools', kind: 'command', description: 'Same listing, by the name people try first' },
  { name: 'skills', kind: 'command', description: 'List the skills available here' },
  { name: 'agents', kind: 'command', description: 'List the agents available here' },
]

export function SlashPalette({ canvas }: { canvas: React.RefObject<HTMLDivElement | null> }) {
  const [items, setItems] = useState<Item[]>(META)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [index, setIndex] = useState(0)
  const [pos, setPos] = useState<{ left: number; bottom: number; width: number } | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const stateRef = useRef({ open: false, index: 0, shown: [] as Item[] })

  useEffect(() => {
    Promise.all([
      fetch('/api/extensions').then(r => r.json()).catch(() => ({})),
      fetch('/api/chat/tools').then(r => r.json()).catch(() => ({})),
    ]).then(([ext, tl]) => {
      const skills = ((ext.skills ?? []) as { name: string; description?: string }[]).map(s => ({ name: s.name, kind: 'skill' as const, description: s.description ?? '' }))
      const agents = ((ext.agents ?? []) as { name: string; description?: string }[]).map(a => ({ name: a.name, kind: 'agent' as const, description: a.description ?? '' }))
      const tools = ((tl.tools ?? []) as { name: string; description?: string }[]).map(t => ({ name: t.name, kind: 'tool' as const, description: (t.description ?? '').split(/(?<=\.)\s/)[0].slice(0, 110) }))
      setItems([...META, ...skills, ...agents, ...tools])
    })
  }, [])

  const shown = (() => {
    const q = query.toLowerCase()
    const starts = items.filter(i => i.name.toLowerCase().startsWith(q))
    const contains = items.filter(i => !i.name.toLowerCase().startsWith(q) && (i.name.toLowerCase().includes(q) || i.description.toLowerCase().includes(q)))
    return [...starts, ...contains].slice(0, 12)
  })()
  stateRef.current = { open, index: Math.min(index, Math.max(0, shown.length - 1)), shown }

  const accept = (item: Item) => {
    const ta = taRef.current
    if (!ta) return
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
    setter?.call(ta, `/${item.name} `)
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    ta.focus()
    ta.setSelectionRange(ta.value.length, ta.value.length)
    setOpen(false)
  }

  useEffect(() => {
    const root = canvas.current
    if (!root) return
    let detach: (() => void) | null = null
    const place = (ta: HTMLTextAreaElement) => {
      const c = root.getBoundingClientRect()
      const t = ta.getBoundingClientRect()
      setPos({ left: t.left - c.left, bottom: c.bottom - t.top + 8, width: Math.min(560, Math.max(320, t.width)) })
    }
    const attach = () => {
      const ta = root.querySelector('textarea') as HTMLTextAreaElement | null
      if (!ta || ta === taRef.current) return
      detach?.()
      taRef.current = ta
      const onInput = () => {
        const m = /^\/([A-Za-z0-9_-]*)$/.exec(ta.value)
        if (m) { setQuery(m[1]); setIndex(0); setOpen(true); place(ta) } else setOpen(false)
      }
      const onKey = (e: KeyboardEvent) => {
        const st = stateRef.current
        if (!st.open) return
        if (e.key === 'ArrowDown') { e.preventDefault(); e.stopPropagation(); setIndex(i => Math.min(i + 1, st.shown.length - 1)); return }
        if (e.key === 'ArrowUp') { e.preventDefault(); e.stopPropagation(); setIndex(i => Math.max(i - 1, 0)); return }
        if ((e.key === 'Enter' && !e.shiftKey) || e.key === 'Tab') {
          const item = st.shown[st.index]
          if (item) { e.preventDefault(); e.stopPropagation(); accept(item) }
          return
        }
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false) }
      }
      const onBlur = () => setTimeout(() => setOpen(false), 150)
      ta.addEventListener('input', onInput)
      ta.addEventListener('keydown', onKey, true)
      ta.addEventListener('blur', onBlur)
      detach = () => { ta.removeEventListener('input', onInput); ta.removeEventListener('keydown', onKey, true); ta.removeEventListener('blur', onBlur); taRef.current = null }
    }
    attach()
    // The composer remounts when the conversation changes; follow it.
    const obs = new MutationObserver(attach)
    obs.observe(root, { childList: true, subtree: true })
    return () => { obs.disconnect(); detach?.() }
  }, [canvas])

  if (!open || !pos || !shown.length) return null
  const cur = Math.min(index, shown.length - 1)
  return (
    <div className="slash-palette" style={{ left: pos.left, bottom: pos.bottom, width: pos.width }} role="listbox" aria-label="Slash commands">
      {shown.map((it, i) => (
        <div
          key={`${it.kind}:${it.name}`}
          role="option"
          aria-selected={i === cur}
          className={`slash-item${i === cur ? ' current' : ''}`}
          onMouseDown={e => { e.preventDefault(); accept(it) }}
          onMouseEnter={() => setIndex(i)}
        >
          <span className="slash-name">/{it.name}</span>
          <span className={`slash-kind k-${it.kind}`}>{it.kind}</span>
          <span className="slash-desc">{it.description}</span>
        </div>
      ))}
      <div className="slash-foot muted">Enter or Tab inserts · Esc closes</div>
    </div>
  )
}
