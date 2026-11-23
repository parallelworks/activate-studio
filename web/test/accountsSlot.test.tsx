import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@parallelworks/code', () => ({ loadPwCodeMessages: async () => ({}) }))
vi.mock('../src/pwcode/session/AccountPanel', () => ({
  AccountPanelBody: (p: { machine: string; machineName: string }) => <div data-testid="panel">{p.machine} on {p.machineName}</div>,
}))

import { ClaudeAccountSlot as AccountSlot } from '../src/components/ClaudeAccountSlot'

describe('the pw code accounts card on Settings, Accounts', () => {
  afterEach(() => vi.unstubAllGlobals())

  it("manages the accounts of the Studio's own pw code", async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ available: true, hostname: 'gpu-box.example' }) })))
    render(<AccountSlot />)
    await waitFor(() => expect(screen.getByTestId('panel')).toHaveTextContent("studio on gpu-box.example"))
    expect(screen.getByText(/where this Studio runs pw code for you/)).toBeInTheDocument()
  })

  it('shows nothing where the Studio has no pw code for the person', async () => {
    const fetch = vi.fn(async () => ({ ok: true, json: async () => ({ available: false }) }))
    vi.stubGlobal('fetch', fetch)
    const { container } = render(<AccountSlot />)
    await waitFor(() => expect(fetch).toHaveBeenCalled())
    await new Promise(r => setTimeout(r, 20))
    expect(container).toBeEmptyDOMElement()
  })
})
