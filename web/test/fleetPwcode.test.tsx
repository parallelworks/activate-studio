import { readFileSync } from 'node:fs'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FleetPage, nextWake } from '../src/views/FleetPage'

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

  it('opens the agent a link names, as Status Activity links it', async () => {
    location.hash = '#view=agents:fleet?agent=watch-abcd'
    try {
      render(<FleetPage personas={[]} onOpenTask={() => {}} />)
      await waitFor(() => expect(document.querySelector('.fleet-card.selected')).not.toBeNull())
      await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(c => String(c[0]) === '/api/fleet/watch-abcd')).toBe(true))
    } finally { location.hash = '' }
  })

  it('opens nothing for a hand-made link whose id does not decode', async () => {
    location.hash = '#view=agents:fleet?agent=%'
    try {
      render(<FleetPage personas={[]} onOpenTask={() => {}} />)
      expect(await screen.findByText('Queue watcher', { selector: '.task-card-objective' })).toBeInTheDocument()
      expect(document.querySelector('.fleet-card.selected')).toBeNull()
    } finally { location.hash = '' }
  })

  it('answers a call its session waits to have approved, from the detail', async () => {
    const waiting = { ...agent, note: 'waiting for your approval: Bash: rm -rf build' }
    let approval: Record<string, unknown> = { id: 'ap1', kind: 'confirm', tool: 'Bash', command: 'rm -rf build', commandLength: 12, approvable: true }
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') posts.push({ url, body: JSON.parse(String(init.body ?? '{}')) })
      const body = url === '/api/fleet' ? { agents: [waiting] }
        : url === '/api/fleet/templates' ? { templates: [] }
        : url === '/api/fleet/watch-abcd' ? { agent: waiting, journal: [], session: { id: agent.session, recent: [] }, approval }
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
    cleanup()
    // A command too long to show whole is offered Deny alone.
    approval = { id: 'ap1', kind: 'confirm', tool: 'Bash', command: 'y'.repeat(100), commandCut: true, commandLength: 70000, approvable: false }
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    expect(await screen.findByText(/70,000 characters, too long to show whole/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeInTheDocument()
    cleanup()
    // Review 2, R3: a long command shows its first lines, and Approve is
    // offered only once the whole command is on screen.
    const long = Array.from({ length: 60 }, (_, i) => `echo line ${i + 1}`).join('\n')
    approval = { id: 'ap2', kind: 'confirm', tool: 'Bash', command: long, commandLength: long.length, approvable: true }
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    const pre = await screen.findByText(/echo line 1\b/, { selector: '.fleet-approval-text' })
    expect(pre.textContent).not.toContain('echo line 60')
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Show the whole command \(60 lines/ }))
    await waitFor(() => expect(screen.getByText(/echo line 60/, { selector: '.fleet-approval-text' })).toBeInTheDocument())
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeInTheDocument()
    cleanup()
    // After review 2, R6: a call with a tool and no command has nothing to
    // open, and is offered Approve as on the Code page.
    approval = { id: 'ap3', kind: 'confirm', tool: 'WebFetch', reason: 'Fetch the release notes', approvable: true }
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    expect(await screen.findByText('Fetch the release notes')).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(posts).toContainEqual({ url: '/api/fleet/watch-abcd/approve', body: { approval: 'ap3', allowed: true } }))
    cleanup()
    // A plan is answered on the Code page, not here.
    approval = { id: 'ap1', kind: 'plan', reason: 'Rewrite the build', approvable: false }
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    fireEvent.click(await screen.findByText('Queue watcher', { selector: '.task-card-objective' }))
    expect(await screen.findByText(/It waits for you on a plan/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
  })

  it('keeps a card\'s footer to one line', () => {
    const now = new Date(2026, 10, 22, 17, 30)
    expect(nextWake(new Date(2026, 10, 22, 18, 26).toISOString(), now)).not.toMatch(/Nov/)
    expect(nextWake(new Date(2026, 10, 23, 18, 26).toISOString(), now)).toMatch(/Nov/)
    expect(readFileSync('src/styles.css', 'utf8')).toMatch(/\.fleet-card \.task-card-meta \{[^}]*white-space: nowrap/)
  })
})
