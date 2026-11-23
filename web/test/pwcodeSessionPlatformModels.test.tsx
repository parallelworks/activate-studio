import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const chat = {
  models: [{ id: 'org/model-a', name: 'Model A' }],
  providerIssues: [{ provider: 'x' }],
  refreshModels: vi.fn(),
  isLoadingModels: false,
  modelsError: null,
}
vi.mock('@parallelworks/ui/ai', () => ({ useChat: () => chat }))

import { usePlatformModels } from '../src/pwcode/session/usePlatformModels'

const answer = (source: string | null, ok = true) =>
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok, json: async () => (source ? { models: { source } } : {}) })))

describe('the platform models a pw code session is offered', () => {
  afterEach(() => vi.unstubAllGlobals())

  it("are the chat's models while they come from ACTIVATE", async () => {
    answer('cli')
    const { result } = renderHook(() => usePlatformModels())
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/access'))
    await new Promise(r => setTimeout(r, 10))
    expect(result.current.models).toEqual(chat.models)
  })

  it.each(['endpoint', 'sidecar', 'none'])('are none while the Studio takes its models from %s', async source => {
    answer(source)
    const { result } = renderHook(() => usePlatformModels())
    await waitFor(() => expect(result.current.models).toEqual([]))
    expect(result.current.providerIssues).toEqual([])
  })

  it("keep the chat's models when the source cannot be read", async () => {
    answer(null, false)
    const { result } = renderHook(() => usePlatformModels())
    await new Promise(r => setTimeout(r, 10))
    expect(result.current.models).toEqual(chat.models)
  })
})
