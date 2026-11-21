import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentsView } from '../src/views/AgentsView'

const task = {
  id: '2026-11-02-live-cd34', objective: 'Was running', state: 'canceled', maxAgents: 4, maxDepth: 1, agents: [
    { name: 'w1', persona: 'reviewer', objective: 'Review', parent: null, depth: 0, state: 'canceled', note: 'stopped when the task engine was retired', resultPath: null, updatedAt: '', sessionId: '20261102-120000-abcdef12', approvals: null },
    { name: 'w2', persona: '', objective: 'Write', parent: 'w1', depth: 1, state: 'completed', note: '', resultPath: 'tasks/2026-11-02-live-cd34/w2.md', updatedAt: '' },
  ],
  board: [{ seq: 1, at: '2026-11-02T00:00:00.000Z', from: 'orchestrator', topic: 'task', body: 'start' }],
}

describe('the Earlier tasks page', () => {
  let posts: string[]
  let asked: string[]
  beforeEach(() => {
    posts = []
    asked = []
    localStorage.setItem('ade-agents-page', 'tasks')
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') posts.push(url)
      asked.push(url)
      const body = url === '/api/tasks' ? { tasks: [{ id: task.id, objective: task.objective, state: 'canceled', agents: 2, running: 0, waiting: 0 }] }
        : url === `/api/tasks/${task.id}` ? task
        : url.endsWith('/output') ? { tail: 'the agent log' }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

  it('shows a past task to read, with no way to run, approve, steer or cancel it', async () => {
    const onOpen = vi.fn()
    render(<AgentsView onOpen={onOpen} />)
    expect(screen.getByRole('tab', { name: 'Earlier tasks' })).toBeInTheDocument()
    fireEvent.click(await screen.findByText('Was running', { selector: '.task-card-objective' }))
    expect(await screen.findByText('stopped when the task engine was retired')).toBeInTheDocument()
    fireEvent.click(screen.getByText('w1'))
    await screen.findByText('20261102-120000-abcdef12')
    expect(screen.getAllByRole('link', { name: 'Code page' }).map(a => a.getAttribute('href'))).toContain('#view=code?session=20261102-120000-abcdef12')
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Steer' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
    fireEvent.click(screen.getByText('results in the library'))
    expect(onOpen).toHaveBeenCalledWith('tasks/2026-11-02-live-cd34')
    expect(asked).toContain(`/api/tasks/${task.id}/agents/w1/output`)
    expect(posts).toEqual([])
  })
})
