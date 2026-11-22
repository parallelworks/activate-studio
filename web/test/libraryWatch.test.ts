import { afterEach, describe, expect, it, vi } from 'vitest'

// A library added from the Library page's dialog joins the switcher when its
// build finishes, without Settings open: the app config is read again then.
const state = { user: [] as { id: string; state: string }[], here: [] as unknown[] }
const api = vi.hoisted(() => ({
  userLibraries: vi.fn(),
  libraries: vi.fn(),
}))
vi.mock('../src/api', () => ({ api }))
const reloadAppConfig = vi.hoisted(() => vi.fn())
vi.mock('../src/config', () => ({ reloadAppConfig }))

import { reloadWhenLibrariesBuilt, resetLibraryWatchForTests } from '../src/libraryWatch'

api.userLibraries.mockImplementation(async () => ({ libraries: state.user }))
api.libraries.mockImplementation(async () => ({ libraries: state.here }))

afterEach(() => { resetLibraryWatchForTests(); reloadAppConfig.mockClear(); state.user = []; state.here = [] })

describe('after a library is added', () => {
  it('reads the library list again when its build finishes, and stops', async () => {
    state.user = [{ id: 'u-space-ada', state: 'building' }]
    reloadWhenLibrariesBuilt({ everyMs: 10 })
    await new Promise(r => setTimeout(r, 40))
    expect(reloadAppConfig).not.toHaveBeenCalled()
    state.user = [{ id: 'u-space-ada', state: 'ready' }]
    await vi.waitFor(() => expect(reloadAppConfig).toHaveBeenCalled())
    const calls = api.userLibraries.mock.calls.length
    await new Promise(r => setTimeout(r, 40))
    expect(api.userLibraries.mock.calls.length).toBe(calls)
  })

  it('covers a library built on this computer too', async () => {
    state.here = [{ id: 'docs', build: { state: 'building' }, caps: { index: false } }]
    reloadWhenLibrariesBuilt({ everyMs: 10 })
    await new Promise(r => setTimeout(r, 40))
    expect(reloadAppConfig).not.toHaveBeenCalled()
    state.here = [{ id: 'docs', build: { state: 'ready' }, caps: { index: true } }]
    await vi.waitFor(() => expect(reloadAppConfig).toHaveBeenCalled())
  })

  it('runs one watch at a time', async () => {
    state.user = [{ id: 'u-space-ada', state: 'building' }]
    const before = api.userLibraries.mock.calls.length
    reloadWhenLibrariesBuilt({ everyMs: 10 })
    reloadWhenLibrariesBuilt({ everyMs: 10 })
    await new Promise(r => setTimeout(r, 35))
    // One watch polls about every 10 ms: two would ask twice as often.
    expect(api.userLibraries.mock.calls.length - before).toBeLessThanOrEqual(5)
    state.user = [{ id: 'u-space-ada', state: 'failed' }]
    await vi.waitFor(() => expect(reloadAppConfig).toHaveBeenCalled())
  })

  it('keeps watching when one list fails to answer, rather than reading it as empty', async () => {
    state.user = [{ id: 'u-space-ada', state: 'building' }]
    let fail = 0
    api.userLibraries.mockImplementation(async () => { if (fail-- > 0) throw new Error('502'); return { libraries: state.user } })
    reloadWhenLibrariesBuilt({ everyMs: 10 })
    await new Promise(r => setTimeout(r, 25))
    fail = 3
    await new Promise(r => setTimeout(r, 60))
    // The failures were not taken as nothing building: no reload yet, and
    // the watch is still asking.
    expect(reloadAppConfig).not.toHaveBeenCalled()
    state.user = [{ id: 'u-space-ada', state: 'ready' }]
    await vi.waitFor(() => expect(reloadAppConfig).toHaveBeenCalled())
    api.userLibraries.mockImplementation(async () => ({ libraries: state.user }))
  })
})
