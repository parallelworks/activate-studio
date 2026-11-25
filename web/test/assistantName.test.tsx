import { render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { chatSessionHref } from '../src/code/chatSession'
import { SECTION_SWITCHES } from '../src/config'
import type { Notices } from '../src/notices'
import { HelpView } from '../src/views/HelpView'

const row = { id: 's1', machineId: 'm1', name: 'mesh' }
const notices = (o: Partial<Notices>) => ({ chats: {}, sessions: [], machines: [], ...o }) as unknown as Notices

describe('the session a chat opens in Code', () => {
  it('is the one listed, on the machine it is listed on', () => {
    const n = notices({ chats: { c1: { session: 's1' } } as never, sessions: [row] as never })
    expect(chatSessionHref(n, 'c1')).toBe('#view=code?machine=m1&session=s1')
    // The chat's own record may name a session the notices have yet to.
    expect(chatSessionHref(n, 'c2', { session: 's9', machine: 'm2' })).toBe('#view=code?machine=m2&session=s9')
    // So may the notices name one not listed now.
    expect(chatSessionHref(notices({ chats: { c3: { session: 's3', machine: 'm3' } } as never }), 'c3')).toBe('#view=code?machine=m3&session=s3')
    // Listed, a session is where the listing says.
    expect(chatSessionHref(n, 'c1', { session: 's1', machine: 'studio' })).toBe('#view=code?machine=m1&session=s1')
  })
  it('is none for a chat with no session, and for one on no known machine', () => {
    const n = notices({ chats: { c1: { session: 's1' }, c2: {} } as never })
    expect(chatSessionHref(n, 'c1')).toBeUndefined()
    expect(chatSessionHref(n, 'c2')).toBeUndefined()
    expect(chatSessionHref(n, null)).toBeUndefined()
  })
})

describe('the page named Assistant', () => {
  afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

  it('keeps its id, so links and saved settings to it work as before', () => {
    expect(SECTION_SWITCHES.find(s => s.id === 'chat')).toEqual({ id: 'chat', label: 'Assistant' })
    expect(SECTION_SWITCHES.some(s => s.label === 'Chat')).toBe(false)
  })

  it('is a page of Help by that name, opened by its old address and its new one', async () => {
    const md = readFileSync('../docs/HELP.md', 'utf8')
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      return url === '/api/help' ? new Response(md, { status: 200 })
        : new Response(JSON.stringify({ appName: 'Studio', aiEnabled: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    for (const slug of ['chat?note=engine', 'assistant']) {
      history.replaceState(null, '', `${location.pathname}#view=help:${slug}`)
      const { unmount } = render(<HelpView />)
      expect(await screen.findByRole('heading', { level: 1, name: 'Assistant' })).toBeInTheDocument()
      unmount()
    }
  })

  it('is named so, and the Studio and pw code are, in the guide', () => {
    for (const f of ['../docs/HELP.md', '../docs/REFERENCE.md', '../docs/CUSTOMIZATION.md', '../README.md']) {
      const text = readFileSync(f, 'utf8')
      expect(text, f).not.toMatch(/[Ss]tudio assistant|Studio chat\b/)
      expect(text, f).not.toMatch(/\*\*Chat\*\*|^## Chat$|Chat and Agents/m)
    }
  })
})
