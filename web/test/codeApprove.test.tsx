import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CodeView, approvalText, type Approval, type CodeSession } from '../src/views/CodeView'

const row = (o: Partial<CodeSession>): CodeSession => ({ machineId: 'm1', machine: 'This computer', id: 's1', title: 'Check the notes', messages: 1, status: 'awaiting_approval', waiting: 2, url: '', ...o })

describe('approving a waiting call on the Code page', () => {
  let waiting: Approval[]
  let posted: { url: string; body: Record<string, unknown> }[]
  let refuse = ''
  beforeEach(() => {
    location.hash = ''
    posted = []
    refuse = ''
    waiting = [
      { id: 'ap1', kind: 'confirm', tool: 'Bash', command: 'ls -la notes', reason: 'shell command' },
      { id: 'ap2', kind: 'ask' },
    ]
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        posted.push({ url, body })
        if (refuse) return new Response(JSON.stringify({ error: refuse }), { status: 409, headers: { 'Content-Type': 'application/json' } })
        waiting = waiting.filter(a => a.id !== body.approval)
        return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      const body = url === '/api/code-sessions'
        ? { machines: [{ id: 'm1', name: 'This computer', kind: 'local', status: 'connected' }], codeUrl: '', local: true, sessions: [row({})] }
        : url === '/api/delegated' ? { sessions: [] }
        : url === '/api/chat/pwcode/here' ? { available: true, daemonId: 'm1', hostname: 'box', chats: [] }
        : url.startsWith('/api/code-sessions/item') ? { session: row({ waiting: waiting.length }), history: [], earlier: 0, approvals: waiting }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals(); location.hash = '' })

  it('shows what it waits on and approves one call', async () => {
    render(<CodeView />)
    fireEvent.click(await screen.findByText('Check the notes'))
    const card = await screen.findByRole('region', { name: 'Waiting for you' })
    expect(within(card).getByText('2 waiting for you')).toBeInTheDocument()
    expect(within(card).getByText('ls -la notes')).toBeInTheDocument()
    expect(card).toHaveTextContent('It asks you a question; answer it in a Studio chat that continues the session, where the session runs.')
    expect(card).toHaveTextContent('Nothing is approved for later calls.')
    fireEvent.click(within(card).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(posted).toHaveLength(1))
    expect(posted[0]).toEqual({ url: '/api/code-sessions/approve', body: { machine: 'm1', id: 's1', approval: 'ap1', allowed: true } })
    await waitFor(() => expect(screen.queryByText('ls -la notes')).toBeNull())
  })

  it('denies, and says why when the answer is refused', async () => {
    render(<CodeView />)
    fireEvent.click(await screen.findByText('Check the notes'))
    const card = await screen.findByRole('region', { name: 'Waiting for you' })
    refuse = 'That approval is no longer waiting: the session went on, or someone answered it.'
    fireEvent.click(within(card).getByRole('button', { name: 'Deny' }))
    expect(await within(card).findByText(/no longer waiting/)).toBeInTheDocument()
    expect(posted[0].body).toMatchObject({ approval: 'ap1', allowed: false })
  })

  it('shows the command, else the tool', () => {
    expect(approvalText({ id: 'a', command: '  cat log.md ' })).toBe('cat log.md')
    expect(approvalText({ id: 'a', tool: 'ReadFile' })).toBe('ReadFile')
    expect(approvalText({ id: 'a' })).toBe('a tool call')
  })
})
