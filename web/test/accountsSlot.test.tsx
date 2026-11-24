import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@parallelworks/code', () => ({ loadPwCodeMessages: async () => ({}) }))
vi.mock('../src/pwcode/session/AccountPanel', () => ({
  AccountPanelBody: (p: { machine: string; machineName: string }) => <div data-testid="panel">{p.machine} on {p.machineName}</div>,
}))
// The account names, as the panel reads them.
const names = vi.hoisted(() => ({ data: undefined as { name: string }[] | undefined, error: undefined as Error | undefined, asked: [] as [string, boolean][] }))
vi.mock('../src/pwcode/api', () => ({
  pwCode: { useClaudeAccountNames: (machine: string, enabled: boolean) => { names.asked.push([machine, enabled]); return enabled ? { data: names.data, error: names.error } : {} } },
}))

import { ClaudeAccountSlot as AccountSlot } from '../src/components/ClaudeAccountSlot'

describe('the pw code accounts row on Settings, Accounts', () => {
  afterEach(() => { vi.unstubAllGlobals(); names.data = undefined; names.error = undefined; names.asked = [] })
  const here = (body: unknown) => vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => body })))

  it("manages the accounts of the Studio's own pw code, and says how many there are", async () => {
    here({ available: true, hostname: 'gpu-box.example' })
    names.data = [{ name: 'work' }, { name: 'home' }]
    const known = vi.fn()
    const toggled = vi.fn()
    render(<AccountSlot open onOpenChange={toggled} onKnown={known} />)
    await waitFor(() => expect(screen.getByTestId('panel')).toHaveTextContent('studio on gpu-box.example'))
    const head = screen.getByRole('button', { name: 'Claude Code accounts' })
    expect(head).toHaveAttribute('aria-expanded', 'true')
    expect(head).toHaveAccessibleDescription(/pw code signs in with.*2 accounts$/)
    expect(known).toHaveBeenCalledWith(2)
    expect(names.asked).toContainEqual(['studio', true])
    fireEvent.click(head)
    expect(toggled).toHaveBeenCalledWith(false)
  })

  it('says none are added yet, and is closed when the page says so', async () => {
    here({ available: true })
    names.data = []
    const known = vi.fn()
    render(<AccountSlot open={false} onKnown={known} />)
    const head = await screen.findByRole('button', { name: 'Claude Code accounts' })
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(head).toHaveAccessibleDescription(/None added$/)
    expect(known).toHaveBeenCalledWith(0)
  })

  it('shows nothing where Claude Code accounts are off for the person', async () => {
    here({ available: true, claudeCode: false, claudeCodeReason: 'Claude Code accounts are turned off on this Studio by its administrator.' })
    names.data = [{ name: 'work' }]
    const { container } = render(<AccountSlot />)
    await new Promise(r => setTimeout(r, 20))
    expect(container).toBeEmptyDOMElement()
    expect(names.asked.every(([, on]) => !on)).toBe(true)
  })

  it('shows nothing where the Studio has no pw code for the person', async () => {
    const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ available: false }) }))
    vi.stubGlobal('fetch', fetch)
    const { container } = render(<AccountSlot />)
    await waitFor(() => expect(fetch).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 20))
    expect(container).toBeEmptyDOMElement()
    expect(names.asked.every(([, on]) => !on)).toBe(true)
  })
})
