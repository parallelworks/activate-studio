import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FleetPage } from '../src/views/FleetPage'

const why = "Standing agents run on this computer's pw code, which needs ACTIVATE. Sign in with ACTIVATE under Settings, Accounts."

describe('the Fleet page with no ACTIVATE in use', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('asks to sign in, and offers neither New agent nor the templates', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const body = url === '/api/fleet' ? { agents: [], unavailable: why }
        : url === '/api/fleet/templates' ? { templates: [{ key: 'w', name: 'Queue watcher', persona: 'watcher', goal: 'Watch.', every: '1h' }] }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<FleetPage personas={[]} onOpenTask={() => {}} />)
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    expect(screen.getByText(why)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Open Accounts' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New agent' })).toBeNull()
    expect(screen.queryByText('Queue watcher')).toBeNull()
    expect(screen.queryByText(/A standing agent keeps a goal/)).toBeNull()
  })
})
