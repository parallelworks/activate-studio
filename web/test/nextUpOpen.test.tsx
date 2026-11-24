import { act, fireEvent, render, screen } from '@testing-library/react'
import { useRef } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const conv = { id: 'c1', messages: [{ id: 'u1', role: 'user', content: 'write it' }, { id: 'a1', role: 'assistant', content: 'Done.' }] }
vi.mock('@parallelworks/ui/ai', () => ({ useChat: () => ({ currentConversation: conv, isStreaming: false }) }))

import { middleCut, NextUp } from '../src/components/NextUp'
import { currentLibrary, setLibrary } from '../src/api'

function Host() {
  const ref = useRef<HTMLDivElement | null>(null)
  return <div ref={ref}><textarea placeholder="Send a message" /><NextUp canvas={ref} /></div>
}

const served = (suggestions: unknown[]) => vi.spyOn(globalThis, 'fetch').mockResolvedValue({ json: async () => ({ after: 'a1', suggestions }) } as Response)

afterEach(() => { vi.restoreAllMocks(); location.hash = ''; setLibrary('kb') })

describe('a file the turn wrote', () => {
  it('opens in the viewer when clicked, and sends no message', async () => {
    served([
      { text: 'Follow run sim-00012 until it finishes and tell me the outcome', why: 'a run was launched' },
      { text: 'q3-summary.pdf', why: 'a file was written', kind: 'open', path: 'reports/q3-summary.pdf', title: 'Open reports/q3-summary.pdf in the Library. Also written as q3-summary.md', also: ['reports/q3-summary.md'] },
    ])
    render(<Host />)
    const chip = await screen.findByRole('button', { name: 'Open reports/q3-summary.pdf in the Library. Also written as q3-summary.md' })
    expect(chip).toHaveTextContent('q3-summary.pdf+md')
    const ta = document.querySelector('textarea')!
    // The placeholder and Tab offer the message, never the file.
    expect(ta.getAttribute('placeholder')).toBe('Follow run sim-00012 until it finishes and tell me the outcome  (Tab)')
    fireEvent.click(chip)
    expect(location.hash).toBe('#open=file:reports/q3-summary.pdf')
    expect(ta.value).toBe('')
  })

  it('opens in the library it was written to, and a folder chip opens the folder', async () => {
    served([
      { text: 'a.pdf', why: 'a file was written', kind: 'open', path: 'drafts/a.pdf', lib: 'u-example', title: 'Open drafts/a.pdf in My library' },
      { text: 'Open the 4 files written', why: 'files were written', kind: 'open', path: 'drafts', lib: 'u-example', folder: true, title: 'Open drafts in My library' },
    ])
    render(<Host />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open drafts/a.pdf in My library' }))
    expect(location.hash).toBe('#open=file:drafts/a.pdf&lib=u-example')
    expect(currentLibrary()).toBe('u-example')
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Open drafts in My library' })) })
    expect(location.hash).toBe('#open=file:drafts&lib=u-example')
  })

  it('a file in the main library opens there when another library is open', async () => {
    setLibrary('u-example')
    served([{ text: 'b.docx', why: 'a file was written', kind: 'open', path: 'notes/b.docx', title: 'Open notes/b.docx in the Library' }])
    render(<Host />)
    fireEvent.click(await screen.findByRole('button', { name: 'Open notes/b.docx in the Library' }))
    expect(currentLibrary()).toBe('kb')
    expect(location.hash).toBe('#open=file:notes/b.docx')
  })
})

describe('a long file name', () => {
  it('is cut in its middle, keeping its extension', () => {
    expect(middleCut('short.pdf')).toBe('short.pdf')
    const cut = middleCut('a very long report name for the third quarter.pdf')
    expect([...cut].length).toBe(32)
    expect(cut.endsWith('quarter.pdf')).toBe(true)
    expect(cut).toContain('…')
    expect(cut.startsWith('a very long')).toBe(true)
  })
})
