import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SlashPalette } from '../src/components/SlashPalette'

function Harness() {
  const ref = createRef<HTMLDivElement>()
  return (
    <div ref={ref} className="chat-canvas" style={{ position: 'relative' }}>
      <textarea aria-label="composer" />
      <SlashPalette canvas={ref} />
    </div>
  )
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => ({
    json: async () => url.includes('/api/extensions')
      ? { skills: [{ name: 'deep_research', description: 'Research a topic' }], agents: [{ name: 'reviewer', description: 'Reviews' }] }
      : { tools: [{ name: 'search_kb', description: 'Search the knowledge base. More text.' }, { name: 'run_workflow', description: 'Run a workflow.' }] },
  })))
})

describe('SlashPalette', () => {
  it('opens on a leading slash, filters, inserts with Enter, and keeps Enter from the composer', async () => {
    const user = userEvent.setup()
    const bubbled = vi.fn()
    document.addEventListener('keydown', bubbled)
    render(<Harness />)
    const ta = screen.getByLabelText('composer') as HTMLTextAreaElement
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
    await user.click(ta)
    await user.type(ta, '/')
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
    expect(screen.getAllByRole('option').map(o => o.textContent)).toEqual(expect.arrayContaining([expect.stringContaining('/help'), expect.stringContaining('/deep_research'), expect.stringContaining('/search_kb')]))
    await user.type(ta, 'sea')
    const options = screen.getAllByRole('option')
    expect(options[0].textContent).toContain('/search_kb')
    bubbled.mockClear()
    await user.keyboard('{Enter}')
    expect(ta.value).toBe('/search_kb ')
    expect(screen.queryByRole('listbox')).toBeNull()
    expect(bubbled.mock.calls.some(([e]) => (e as KeyboardEvent).key === 'Enter')).toBe(false)
    document.removeEventListener('keydown', bubbled)
  })
  it('closes on Escape and stays closed for ordinary text', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const ta = screen.getByLabelText('composer') as HTMLTextAreaElement
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
    await user.click(ta)
    await user.type(ta, '/to')
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('listbox')).toBeNull()
    await user.clear(ta)
    await user.type(ta, 'hello /tools')
    expect(screen.queryByRole('listbox')).toBeNull()
  })
})
