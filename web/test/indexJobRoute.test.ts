import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, setLibrary } from '../src/api'

// Phase 4, U: with All libraries showing, an upload into My library's top
// folder asked for its index job with library "all", which the server
// refuses ("This action works in one library at a time"). The folder's
// prefix names the library the job is for.
describe('the index job after an upload in All libraries', () => {
  afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

  it('goes to the library the folder is in, its top folder included', async () => {
    const bodies: unknown[] = []
    vi.stubGlobal('fetch', vi.fn(async (_u: string, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body)))
      return new Response(JSON.stringify({ id: 1 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    setLibrary('all')
    await api.startIndexJob('kb')
    await api.startIndexJob('kb/uploads')
    await api.startIndexJob('u-space-ada/uploads')
    expect(bodies).toEqual([{ path: '' }, { path: 'uploads' }, { path: 'uploads', library: 'u-space-ada' }])
    setLibrary('kb')
    await api.startIndexJob('uploads')
    expect(bodies.at(-1)).toEqual({ path: 'uploads' })
  })
})
