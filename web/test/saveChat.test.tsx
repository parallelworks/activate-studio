import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SaveChatButton, readersNote, type ChatDest } from '../src/components/SaveChatButton'

const MAIN: ChatDest = { library: 'kb', label: 'Notes', path: 'chat-sessions/2026-11-20-abcd1234.md', space: false, shared: false }

function serve(dest: ChatDest | { status: number; error: string }) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${url}`)
    if ('status' in dest) return new Response(JSON.stringify({ error: dest.error }), { status: dest.status })
    return new Response(JSON.stringify(dest), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

afterEach(() => { vi.unstubAllGlobals() })

describe('Save to library', () => {
  it('says where the chat goes before writing it, then saves it there', async () => {
    const calls = serve(MAIN)
    render(<SaveChatButton conversationId="c 1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/Saves a copy of this chat to Notes as/)).toBeInTheDocument()
    expect(screen.getByText('chat-sessions/2026-11-20-abcd1234.md')).toBeInTheDocument()
    expect(screen.getByText(/Chats are not added to a library unless you save them/)).toBeInTheDocument()
    expect(calls).toEqual(['GET /api/chat/conversations/c%201/save'])
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' }).at(-1)!)
    expect(await screen.findByText(/Saved to Notes as/)).toBeInTheDocument()
    expect(calls).toEqual(['GET /api/chat/conversations/c%201/save', 'POST /api/chat/conversations/c%201/save'])
    expect(screen.getByRole('button', { name: 'Done' })).toBeInTheDocument()
  })

  it('names who can read the copy', () => {
    expect(readersNote({ ...MAIN, space: true, label: 'My library' })).toBe('Only you can read it there.')
    expect(readersNote({ ...MAIN, shared: true })).toBe('Everyone who uses this Studio can read it there.')
    expect(readersNote(MAIN)).toBe('')
  })

  it('says why when the chat cannot be saved, and offers no Save', async () => {
    serve({ status: 409, error: 'the main library is read only' })
    render(<SaveChatButton conversationId="c1" />)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Cannot save this chat: the main library is read only.')
    await waitFor(() => expect(screen.getAllByRole('button', { name: 'Save' })).toHaveLength(1))
  })
})
