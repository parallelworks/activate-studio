import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useEffectiveTheme } from '../src/theme'

describe('useEffectiveTheme', () => {
  it('follows the data-theme attribute App writes', async () => {
    document.documentElement.dataset.theme = 'light'
    const { result } = renderHook(() => useEffectiveTheme())
    expect(result.current).toBe('light')
    await act(async () => { document.documentElement.dataset.theme = 'dark'; await new Promise(r => setTimeout(r, 0)) })
    expect(result.current).toBe('dark')
    await act(async () => { document.documentElement.dataset.theme = 'light'; await new Promise(r => setTimeout(r, 0)) })
    expect(result.current).toBe('light')
  })
})
