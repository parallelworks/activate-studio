import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import type { PublicLibrary } from '../src/api'
import { useChatScope } from '../src/chatScope'
import { LibraryTicks, scopeLabel } from '../src/components/LibraryScope'
import { getChatLibrary } from '../src/labelScope'
import { resetScopesForTests } from '../src/scope'

// A checkbox forwards its press to its hidden input as a PointerEvent,
// which jsdom does not have.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const lib = (id: string, label: string, extra: Partial<PublicLibrary> = {}): PublicLibrary => ({
  id, label, primary: id === 'kb', writable: true, pinned: id === 'kb', source: true, caps: { index: true, fullText: true, vectors: true }, meaning: 'on', ...extra,
})
const TWO = [lib('kb', 'Notes'), lib('desktop', 'Desktop')]
const THREE = [...TWO, lib('projects', 'Projects')]

// The chat's Scope menu, as ChatView puts it together: the button's
// sub-label and the library ticks.
function Scope({ libraries }: { libraries: PublicLibrary[] | undefined }) {
  const [scope, setScope] = useChatScope(libraries)
  return (
    <>
      <span data-testid="label">{scopeLabel(libraries ?? [], scope)}</span>
      <LibraryTicks libraries={libraries ?? []} value={scope} onChange={setScope} />
    </>
  )
}

const tick = (name: string) => fireEvent.click(within(screen.getByRole('group', { name: 'Libraries to include' })).getByRole('checkbox', { name }))
const label = () => screen.getByTestId('label').textContent

beforeEach(() => { resetScopesForTests() })

describe("the chat's libraries", () => {
  it('searches every library when nothing is kept', () => {
    render(<Scope libraries={TWO} />)
    expect(label()).toBe('All libraries')
    expect(getChatLibrary()).toBe('all')
    expect(within(screen.getByRole('group', { name: 'Libraries to include' })).getByRole('checkbox', { name: 'All libraries' })).toHaveAttribute('aria-checked', 'true')
  })

  it('sends nothing with one library, and the kept choice before the libraries are known', () => {
    const one = render(<Scope libraries={[TWO[0]]} />)
    expect(getChatLibrary()).toBeNull()
    one.unmount()
    localStorage.setItem('ade-scope-chat', JSON.stringify(['desktop', 'gone']))
    render(<Scope libraries={undefined} />)
    expect(getChatLibrary()).toEqual(['desktop', 'gone'])
  })

  it('keeps a library picked before it took several, and turns the old default into all', () => {
    localStorage.setItem('ade-chat-library', 'desktop')
    const first = render(<Scope libraries={THREE} />)
    expect(label()).toBe('Desktop')
    expect(getChatLibrary()).toBe('desktop')
    first.unmount()

    resetScopesForTests()
    localStorage.setItem('ade-chat-library', 'all')
    const second = render(<Scope libraries={THREE} />)
    expect(label()).toBe('All libraries')
    second.unmount()

    resetScopesForTests()
    localStorage.setItem('ade-chat-library', 'gone')
    render(<Scope libraries={THREE} />)
    expect(label()).toBe('All libraries')
    expect(getChatLibrary()).toBe('all')
  })

  it('ticks any of the libraries, names the choice, and keeps one ticked', () => {
    render(<Scope libraries={THREE} />)
    act(() => tick('Projects'))
    expect(label()).toBe('2 libraries')
    expect(getChatLibrary()).toEqual(['kb', 'desktop'])
    expect(JSON.parse(localStorage.getItem('ade-scope-chat')!)).toEqual(['kb', 'desktop'])
    act(() => tick('Notes'))
    expect(label()).toBe('Desktop')
    expect(getChatLibrary()).toBe('desktop')
    const group = screen.getByRole('group', { name: 'Libraries to include' })
    expect(within(group).getByRole('checkbox', { name: 'Desktop' })).toHaveAttribute('aria-disabled', 'true')
    expect(within(group).getByRole('checkbox', { name: 'All libraries' })).toHaveAttribute('aria-checked', 'mixed')
    act(() => tick('Projects'))
    expect(label()).toBe('2 libraries')
    expect(getChatLibrary()).toEqual(['desktop', 'projects'])
    act(() => tick('All libraries'))
    expect(label()).toBe('All libraries')
    expect(getChatLibrary()).toBe('all')
    expect(JSON.parse(localStorage.getItem('ade-scope-chat')!)).toBe('all')
    expect(localStorage.getItem('ade-chat-library')).toBeNull()
  })

  it('names the knowledge base alone by sending nothing, as before', () => {
    render(<Scope libraries={TWO} />)
    act(() => tick('Desktop'))
    expect(label()).toBe('Notes')
    expect(getChatLibrary()).toBeNull()
  })
})

describe('the Scope button', () => {
  it('names one library, all of them, or how many', () => {
    expect(scopeLabel(THREE, 'all')).toBe('All libraries')
    expect(scopeLabel(THREE, ['desktop'])).toBe('Desktop')
    expect(scopeLabel(THREE, ['kb', 'projects'])).toBe('2 libraries')
  })
})
