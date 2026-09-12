import { fireEvent, render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { currentLibrary, setLibrary, withLibrary } from '../src/api'
import { LibrarySwitch, libraryWritable } from '../src/components/LibrarySwitch'

const libs = [
  { id: 'kb', label: 'Knowledge base', primary: true, writable: true, pinned: true, source: true, caps: { index: true, fullText: true, vectors: true } },
  { id: 'scratch', label: 'Scratch', primary: false, writable: false, pinned: true, source: false, caps: { index: true, fullText: false, vectors: false } },
]

describe('libraries on the client', () => {
  it('sends nothing for the primary and names any other library on every URL', () => {
    setLibrary('kb')
    expect(withLibrary('/api/kb/tree?path=a')).toBe('/api/kb/tree?path=a')
    setLibrary('scratch')
    expect(withLibrary('/api/kb/tree?path=a')).toBe('/api/kb/tree?path=a&library=scratch')
    expect(withLibrary('/api/kb/stats')).toBe('/api/kb/stats?library=scratch')
    setLibrary('kb')
  })

  it('renders a switcher only when there is a choice, marks read-only ones, and switches', () => {
    const { container: one } = render(<LibrarySwitch libraries={[libs[0]]} />)
    expect(one.querySelector('select')).toBeNull()
    const { container } = render(<LibrarySwitch libraries={libs} />)
    const select = container.querySelector('select') as HTMLSelectElement
    expect(select.options.length).toBe(2)
    expect(select.options[1].text).toContain('read only')
    fireEvent.change(select, { target: { value: 'scratch' } })
    expect(currentLibrary()).toBe('scratch')
    expect(libraryWritable(libs, 'scratch')).toBe(false)
    expect(libraryWritable(libs, 'kb')).toBe(true)
    setLibrary('kb')
  })
})
