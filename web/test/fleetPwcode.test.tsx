import { readFileSync } from 'node:fs'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FleetPage } from '../src/views/FleetPage'

const agent = {
  id: 'watch-abcd', name: 'Queue watcher', goal: 'Watch the queues.', persona: 'watcher', model: '', resource: null,
  triggers: { every: '30m', onRunEnd: true, watchPath: null }, budget: { maxTicks: null, maxTokens: null, maxTurns: 5 },
  state: 'input-required', note: 'resubmit run 42?', lastTickAt: null, nextTickAt: null, ticks: 4, tokens: 1200,
  folder: 'agents/fleet/watch-abcd', session: '20261121-120001-abcdef', sessions: [],
  migrated: 'Moved to a pw code session working in agents/fleet/watch-abcd. Not carried over: its model gpt-x is a Studio model.',
}

describe('the Fleet page over pw code sessions', () => {
  let posts: { url: string; body: Record<string, unknown> }[]
  beforeEach(() => {
    posts = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body ?? '{}')) })
      const body = url === '/api/fleet' ? (init?.method === 'POST' ? { agent: { ...agent, id: 'new-1' } } : { agents: [agent] })
        : url === '/api/fleet/templates' ? { templates: [] }
        : url === '/api/fleet/watch-abcd' ? { agent, journal: [{ at: '2026-11-21T12:00:00.000Z', kind: 'result', text: '**Did:** checked two queues.' }], session: { id: agent.session, recent: ['> Wake-up 4', 'Checked two queues.'] } }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('shows an agent\'s folder, its session, and what did not carry over', async () => {
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    expect(await screen.findByText(/4 wake-ups/)).toBeInTheDocument()
    fireEvent.click(screen.getByText('Queue watcher', { selector: '.task-card-objective' }))
    expect(await screen.findByText(/is a Studio model/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'agents/fleet/watch-abcd' })).toHaveAttribute('href', '#open=file:agents/fleet/watch-abcd/AGENTS.md')
    expect(screen.getByText('Its session on the Code page').closest('a')).toHaveAttribute('href', '#view=code?session=20261121-120001-abcdef')
    expect(screen.getByText(/Its session, latest first/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Wake now' })).toBeInTheDocument()
  })

  it('renders a result, says what it needs in the detail, and asks before retiring', async () => {
    const changed = vi.fn()
    window.addEventListener('ade:fleet-changed', changed)
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    // Who needs you is a line in the header, by name.
    const needs = await screen.findByRole('group', { name: 'Needs you' })
    expect(needs).toHaveTextContent('Queue watcher')
    fireEvent.click(screen.getByText('Queue watcher', { selector: '.task-card-objective' }))
    const result = await screen.findByText(/checked two queues/)
    expect(result.closest('.md-body')).not.toBeNull()
    expect(result.closest('.md-body')?.textContent).not.toContain('**')
    expect(screen.getByText(/It asks: resubmit run 42\? Answer below\./)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retire' }))
    expect(await screen.findByText('Retire Queue watcher?')).toBeInTheDocument()
    expect(posts.some(p => p.url.endsWith('/retire'))).toBe(false)
    fireEvent.click(screen.getAllByRole('button', { name: 'Retire' }).at(-1)!)
    await waitFor(() => expect(posts.some(p => p.url === '/api/fleet/watch-abcd/retire')).toBe(true))
    await waitFor(() => expect(changed).toHaveBeenCalled())
    window.removeEventListener('ade:fleet-changed', changed)
  })

  it('keeps the actions in their places before the first wake-up and while it works', async () => {
    let shown: Record<string, unknown> = { ...agent, state: 'idle', note: '', session: null }
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const body = url === '/api/fleet' ? { agents: [shown] } : url === '/api/fleet/templates' ? { templates: [] }
        : url === '/api/fleet/watch-abcd' ? { agent: shown, journal: [], session: null } : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const actions = () => Array.from(document.querySelectorAll('.fleet-detail-actions > *')).map(e => e.textContent)
    const { unmount } = render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    await screen.findByText('No session until its first wake-up')
    expect(actions()).toEqual(['Wake now', 'Pause', 'Retire', 'No session until its first wake-up'])
    expect(document.querySelector('.fleet-detail-meta')).toHaveTextContent('watcher · pw code\'s default model · every 30m · on run end')
    unmount()
    shown = { ...agent, state: 'working', note: '' }
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    await screen.findByText('Its session on the Code page')
    expect(actions()).toEqual(['Wake now', 'Pause', 'Retire', 'Its session on the Code page'])
    expect(screen.getByRole('button', { name: 'Wake now' })).toBeDisabled()
    // The actions are a row of their own under the name and settings.
    expect(readFileSync('src/styles.css', 'utf8')).toMatch(/\.fleet-detail-head \{ display: flex; flex-direction: column;/)
  })

  it('creates an agent with a pw code model and turns per wake-up', async () => {
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'New agent' }))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Digest' } })
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Write the digest.' } })
    fireEvent.change(screen.getByLabelText('pw code model'), { target: { value: 'claude-code/haiku' } })
    fireEvent.change(screen.getByLabelText('Turns per wake-up'), { target: { value: '4' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create agent' }))
    await waitFor(() => expect(posts.some(p => p.url === '/api/fleet')).toBe(true))
    const body = posts.find(p => p.url === '/api/fleet')!.body
    expect(body).toMatchObject({ name: 'Digest', goal: 'Write the digest.', model: 'claude-code/haiku', maxTurns: 4 })
    expect(body).not.toHaveProperty('execution')
  })

  it('answers a call its session waits to have approved, from the detail', async () => {
    const waiting = { ...agent, note: 'waiting for your approval: rm -rf build' }
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body ?? '{}')) })
      const body = url === '/api/fleet' ? { agents: [waiting] }
        : url === '/api/fleet/templates' ? { templates: [] }
        : url === '/api/fleet/watch-abcd' ? { agent: waiting, journal: [], session: { id: agent.session, recent: [] }, approval: { id: 'ap1', text: 'rm -rf build' } }
        : { ok: true }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    expect(await screen.findByText('rm -rf build', { selector: '.fleet-approval-text' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'the Code page' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    await waitFor(() => expect(posts).toContainEqual({ url: '/api/fleet/watch-abcd/approve', body: { approval: 'ap1', allowed: false } }))
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(posts).toContainEqual({ url: '/api/fleet/watch-abcd/approve', body: { approval: 'ap1', allowed: true } }))
  })
})
