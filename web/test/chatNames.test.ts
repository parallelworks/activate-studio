import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const warned = vi.hoisted(() => [] as string[])
vi.mock('@parallelworks/ui', async orig => ({ ...(await orig<Record<string, unknown>>()), toast: { warning: (s: string) => warned.push(s), error: () => undefined, success: () => undefined } }))

import { createStudioAdapter } from '../src/adapter'

let answer: unknown
let asked: string[]
beforeEach(() => {
  vi.resetModules()
  answer = { changed: 0 }
  asked = []
  warned.length = 0
  vi.stubGlobal('fetch', vi.fn(async (u: RequestInfo | URL, init?: RequestInit) => {
    asked.push(`${init?.method ?? 'GET'} ${String(u)}`)
    return new Response(JSON.stringify(answer), { status: 200 })
  }))
})
afterEach(() => vi.unstubAllGlobals())

describe('one name for a pw code chat and its session', () => {
  it('reads the names now and then, and the chats again only when one changed', async () => {
    const { readNames } = await import('../src/pwcode/chatNames')
    let reads = 0
    const on = () => { reads++ }
    window.addEventListener('ade:chats-changed', on)
    await readNames()
    await readNames()
    expect(asked).toEqual(['POST /api/chat/pwcode/names'])
    expect(reads).toBe(0)
    answer = { changed: 1 }
    await readNames(0)
    expect(reads).toBe(1)
    window.removeEventListener('ade:chats-changed', on)
  })

  it('reads the chats once for a name the session view has just seen', async () => {
    const { announceName } = await import('../src/pwcode/chatNames')
    let reads = 0
    const on = () => { reads++ }
    window.addEventListener('ade:chats-changed', on)
    announceName('c1', 'Mesh study')
    await vi.waitFor(() => expect(reads).toBe(1))
    announceName('c1', 'Mesh study')
    await new Promise(r => setTimeout(r, 10))
    expect(reads).toBe(1)
    window.removeEventListener('ade:chats-changed', on)
  })

  it('says why a session keeps its name when its chat is renamed', async () => {
    const rename = createStudioAdapter().conversations!.rename!
    answer = { id: 'c1', title: 'Mesh study' }
    await rename('c1', 'Mesh study')
    expect(warned).toEqual([])
    answer = { id: 'c1', title: 'Mesh study', sessionKeeps: 'The chat is renamed here. Its pw code session keeps its name for now.' }
    await rename('c1', 'Mesh study')
    expect(warned).toEqual(['The chat is renamed here. Its pw code session keeps its name for now.'])
  })
})
