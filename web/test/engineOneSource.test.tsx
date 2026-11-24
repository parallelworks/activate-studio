import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const chat = vi.hoisted(() => ({ loadConversation: vi.fn(async () => {}), streaming: false, id: 'c7' }))
vi.mock('@parallelworks/ui/ai', () => ({
  useChat: () => ({ currentConversation: { id: chat.id }, isStreaming: chat.streaming, loadConversation: chat.loadConversation }),
}))

import { PWCodeEngine } from '../src/components/PWCodeEngine'
import { useChatLink } from '../src/pwcode/useChatLink'
import { forgetEngines } from '../src/chatEngine'

beforeEach(() => { localStorage.clear(); forgetEngines() })
afterEach(() => { vi.unstubAllGlobals() })

/** The chat's parts as the chat page puts them together: the engine menu
 *  and the part that decides whether the session view shows. */
function OpenChat({ id }: { id: string }) {
  const pw = useChatLink(id)
  return <><PWCodeEngine conversationId={id} /><p data-testid="session-view">{pw.shown ? 'session' : 'thread'}</p></>
}

// An old pw code chat opened by a person pw code is not for here, before
// the Studio assistant has answered it: its record says pw code, and the
// Studio says the Studio assistant answers it now.
describe('What answers a chat, on the page', () => {
  it('is read a few times when a pw code chat its person cannot use opens, not over and over', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url)
      // A loop is cut short here, so the count says so rather than the run
      // running out of memory.
      if (calls.length > 60) return new Promise<Response>(() => {})
      const body = url.startsWith('/api/chat/engine')
        ? { setting: 'auto', default: 'studio', current: 'studio', answered: true, pwcode: false, reason: 'pw code runs as you: sign in with ACTIVATE under Settings, Accounts.' }
        : url.startsWith('/api/chat/conversations/')
          ? { id: 'c7', engine: 'pwcode', pwcode: { session: 's1', relay: true, machine: 'm1' }, messages: [
              { id: 'u1', role: 'user', content: 'Hello' },
              { id: 'pwc-1', role: 'assistant', content: 'Hi', model: 'pw code · m' },
            ] }
          : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<OpenChat id="c7" />)
    await new Promise(r => setTimeout(r, 600))
    const engineReads = calls.filter(u => u.startsWith('/api/chat/engine')).length
    const chatReads = calls.filter(u => u.startsWith('/api/chat/conversations/c7')).length
    expect(engineReads).toBeGreaterThan(0)
    expect(engineReads).toBeLessThanOrEqual(3)
    expect(chatReads).toBeLessThanOrEqual(3)
    // Its next message goes through the Studio: the chat shows its own
    // thread, not the session's view with the session's message box.
    expect(screen.getByTestId('session-view')).toHaveTextContent('thread')
  })
})
