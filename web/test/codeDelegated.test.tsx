import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CodeView, sessionInHash, type CodeSession, type Delegated } from '../src/views/CodeView'
import { sectionAllowed } from '../src/config'

const row = (o: Partial<CodeSession>): CodeSession => ({ machineId: 'm1', machine: 'This computer', id: 'x', title: 't', messages: 1, status: 'idle', url: '', ...o })

const report: Delegated = {
  site: 'local', session: 's1', workspace: '/kb/agents/reviewer/20261121-0905-q3', folder: 'agents/reviewer/20261121-0905-q3', instructions: 'reviewer',
  objective: 'Summarize the Q3 pump tests', started: '2026-11-21T09:05:00Z', state: 'finished', files: ['agents/reviewer/20261121-0905-q3/summary.md'],
}

describe('delegated work on the Code page', () => {
  beforeEach(() => {
    location.hash = ''
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const body = url === '/api/code-sessions'
        ? { machines: [{ id: 'm1', name: 'This computer', kind: 'local', status: 'connected' }], codeUrl: '', local: true, sessions: [
          row({ id: 's1', title: 'Q3 pump summary', workspace: report.workspace }),
          row({ id: 's2', title: 'Something else' }),
        ] }
        : url === '/api/delegated' ? { sessions: [report] }
        : url === '/api/chat/pwcode/here' ? { available: true, daemonId: 'm1', hostname: 'box', chats: [] }
        : url.startsWith('/api/code-sessions/item') ? { session: row({ id: 's1', title: 'Q3 pump summary' }), history: [], earlier: 0 }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); location.hash = '' })

  it('marks delegated sessions, filters to them, and shows the folder and its files', async () => {
    render(<CodeView />)
    expect(await screen.findByText(/delegated, reviewer/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Delegated (1)' }))
    await waitFor(() => expect(screen.queryByText('Something else')).toBeNull())
    fireEvent.click(screen.getByText('Q3 pump summary'))
    const note = await screen.findByRole('region', { name: 'Delegated work' })
    expect(note).toHaveTextContent('Delegated from a chat, finished, with the instructions reviewer in its AGENTS.md, in the library folder agents/reviewer/20261121-0905-q3.')
    expect(within(note).getByRole('link', { name: 'summary.md' })).toHaveAttribute('href', '#open=file:agents/reviewer/20261121-0905-q3/summary.md')
    // No platform: nothing to open in ACTIVATE.
    expect(screen.queryByText('Open in ACTIVATE')).toBeNull()
  })

  it('says a delegated session that stopped on an error failed, and why', async () => {
    report.state = 'stopped'
    report.error = 'This model is not enabled for the AI connection'
    try {
      render(<CodeView />)
      fireEvent.click(await screen.findByText('Q3 pump summary'))
      const note = await screen.findByRole('region', { name: 'Delegated work' })
      expect(note).toHaveTextContent('Delegated from a chat, failed, with the instructions reviewer')
      expect(note).toHaveTextContent('It stopped on an error: This model is not enabled for the AI connection')
    } finally {
      report.state = 'finished'
      delete report.error
    }
  })

  it('opens a session a delegate link names without its machine', async () => {
    expect(sessionInHash('#view=code?session=s1')).toEqual({ machine: '', session: 's1' })
    location.hash = '#view=code?session=s1'
    render(<CodeView />)
    expect(await screen.findByRole('region', { name: 'Delegated work' })).toBeInTheDocument()
  })

  it('shows the Code page with no platform where it lists this computer\'s sessions', () => {
    expect(sectionAllowed('code', { platform: false })).toBe(false)
    expect(sectionAllowed('code', { platform: false, localCode: true })).toBe(true)
    expect(sectionAllowed('workflows', { platform: false, localCode: true })).toBe(false)
  })
})
