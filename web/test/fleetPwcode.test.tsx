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
        : url === '/api/fleet/watch-abcd' ? { agent, journal: [{ at: '2026-11-21T12:00:00.000Z', kind: 'result', text: 'Checked two queues.' }], session: { id: agent.session, recent: ['> Wake-up 4', 'Checked two queues.'] } }
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
    expect(screen.getByText('its session on the Code page').closest('a')).toHaveAttribute('href', '#view=code?session=20261121-120001-abcdef')
    expect(screen.getByText(/Its session, latest first/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Wake now' })).toBeInTheDocument()
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
})
