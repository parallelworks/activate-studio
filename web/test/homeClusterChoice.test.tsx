import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary } from '../src/components/AddLibrary'
import { closeMenu, computers } from './addLibraryHelpers'

// On a Studio a cluster hosts, Add a library listed that cluster under
// Remote computer, chosen already, hinted "the Studio's cluster". This
// computer reads as the Studio's account there, and the cluster under the
// list is read as the person over pw ssh: the form says which, does not
// call the Studio's own cluster remote, and chooses nothing for them. In
// the apps, where every cluster is remote, nothing changes.
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const SYSTEMS = { usable: true, why: null, error: null, systems: [
  { name: 'cluster-b', label: 'cluster-b', status: 'active', running: true, type: null },
  { name: 'cluster-a', label: 'cluster-a', status: 'active', running: true, type: null },
] }

function stub() {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url === '/api/user-libraries/pw/systems') return json(SYSTEMS)
    if (url === '/api/user-libraries/space') return json({ available: true, why: '', cluster: 'cluster-a', suggested: '~/studio', spaces: [], pw: true })
    return json({ error: `no route for ${url}` }, 404)
  }))
}

describe('the cluster a Studio runs on, in Add a library', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('on a hosted Studio, says whose account reads, lists its cluster first, and chooses none', async () => {
    stub()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: true, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Choose a cluster'))
    const rows = (await computers()).map(r => r.textContent ?? '')
    expect(rows[0]).toBe('cluster-aWhere this Studio runs')
    expect(rows[1]).toBe('cluster-b')
    expect(rows.join('|')).not.toMatch(/the Studio's cluster|remote computer/i)
    closeMenu()
    fireEvent.click(screen.getByRole('button', { name: 'About adding a library' }))
    expect(await screen.findByText(/is read as you, with your ACTIVATE sign-in/)).toBeInTheDocument()
  })

  it('beside this computer, says it is read with the Studio\'s account, and what to choose instead', async () => {
    stub()
    render(<AddLibrary options={{ buildHere: true, ssh: false, pw: true, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'About adding a library' }))
    expect(await screen.findByText(/is read with the account the Studio runs as.* To read it as your own account instead, choose cluster-a\./)).toBeInTheDocument()
  })

  it('in the apps, where every cluster is remote, the first running one is chosen', async () => {
    stub()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: true, workflow: false, space: true }} onAdded={() => {}} />)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('cluster-b'))
  })
})
