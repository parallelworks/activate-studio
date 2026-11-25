import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@parallelworks/ui/ai', () => ({
  useChat: () => ({ currentConversation: { id: 'c8' }, isStreaming: false, loadConversation: async () => {} }),
}))

import { PWCodeEngine } from '../src/components/PWCodeEngine'
import { forgetEngines } from '../src/chatEngine'

let visibility: DocumentVisibilityState = 'visible'
beforeEach(() => {
  localStorage.clear(); forgetEngines(); visibility = 'visible'
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility })
})
afterEach(() => { vi.unstubAllGlobals() })

const NEEDS = 'pw code needs ACTIVATE: sign the pw CLI in, or add your ACTIVATE API key under Settings, Accounts.'

// The pw CLI signed in at a terminal tells the page nothing: an open chat
// learns of it when the person comes back to the tab.
describe('A sign-in made outside the Studio', () => {
  it('reaches an open chat when its tab is shown or focused again, with one read each time', async () => {
    let signedIn = false
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url)
      if (calls.length > 40) return new Promise<Response>(() => {})
      const body = url.startsWith('/api/chat/engine')
        ? signedIn
          ? { setting: 'auto', default: 'pwcode', current: 'pwcode', answered: false, pwcode: true, reason: null }
          : { setting: 'auto', default: 'studio', current: 'studio', answered: false, pwcode: false, reason: NEEDS }
        : { id: 'c8', messages: [] }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const reads = () => calls.filter(u => u.startsWith('/api/chat/engine')).length
    render(<PWCodeEngine conversationId="c8" />)
    // Without ACTIVATE the Studio answers and there is nothing to choose.
    await waitFor(() => expect(reads()).toBe(1))
    // Open, it reads once and does not read again by itself.
    await new Promise(r => setTimeout(r, 400))
    expect(reads()).toBe(1)
    expect(document.querySelector('.engine-btn')).toBeNull()

    // The person goes to a terminal and signs the pw CLI in.
    signedIn = true
    await act(async () => {
      visibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('blur'))
    })
    expect(reads()).toBe(1)
    // Back: shown and focused at once, one read.
    await act(async () => {
      visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('focus'))
    })
    await waitFor(() => expect(document.querySelector('.engine-btn')).toHaveTextContent('pw code'))
    expect(reads()).toBe(2)
    await new Promise(r => setTimeout(r, 400))
    expect(reads()).toBe(2)
    // Focus alone, later (a second window on the same tab), reads again.
    await act(async () => { window.dispatchEvent(new Event('focus')) })
    await waitFor(() => expect(reads()).toBe(3))
    // Opening the menu reads again too, so its rows are never stale.
    await act(async () => { (document.querySelector('.engine-btn') as HTMLElement).click() })
    await waitFor(() => expect(reads()).toBe(4))
    await new Promise(r => setTimeout(r, 300))
    expect(reads()).toBe(4)
  })
})
