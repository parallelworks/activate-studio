import { renderHook, waitFor } from '@testing-library/react'
import type { SessionViewHandle } from '@parallelworks/ui/ai'
import { describe, expect, it, vi } from 'vitest'
import { useTakeHandedBack } from '../src/pwcode/session/useTakeHandedBack'

const setup = (over: { took?: boolean; shown?: boolean; holdsImages?: boolean; drawn?: boolean } = {}) => {
  const recall = vi.fn(async () => over.took ?? true)
  const drop = vi.fn()
  const view = { current: { recall } as unknown as SessionViewHandle }
  const hook = renderHook(({ n, shown }) => useTakeHandedBack(view,
    { handedBack: n, handedBackShown: !!over.drawn, dropShownHandedBack: drop }, { shown, holdsImages: over.holdsImages }),
  { initialProps: { n: 0, shown: over.shown ?? true } })
  return { recall, drop, hook }
}

describe('a message a turn gave back', () => {
  it('goes back in the box as it comes back', () => {
    const { recall, hook } = setup()
    expect(recall).not.toHaveBeenCalled()
    hook.rerender({ n: 1, shown: true })
    expect(recall).toHaveBeenCalledExactlyOnceWith(undefined)
  })

  it('waits while another conversation is in view', () => {
    const { recall, hook } = setup({ shown: false })
    hook.rerender({ n: 1, shown: false })
    expect(recall).not.toHaveBeenCalled()
    hook.rerender({ n: 1, shown: true })
    expect(recall).toHaveBeenCalledOnce()
  })

  it('fills only an empty box while it is still drawn where it was sent', async () => {
    const { recall, drop, hook } = setup({ drawn: true, took: false })
    hook.rerender({ n: 1, shown: true })
    expect(recall).toHaveBeenCalledExactlyOnceWith({ ifEmpty: true })
    await waitFor(() => expect(drop).toHaveBeenCalledOnce())
  })

  it('is let go where the box holds images', () => {
    const { recall, drop, hook } = setup({ drawn: true, holdsImages: true })
    hook.rerender({ n: 1, shown: true })
    expect(recall).not.toHaveBeenCalled()
    expect(drop).toHaveBeenCalledOnce()
  })
})
