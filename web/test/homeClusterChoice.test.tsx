import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary } from '../src/components/AddLibrary'

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
const where = (name: string) => screen.queryByRole('button', { name })

describe('the cluster a Studio runs on, in Add a library', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('on a hosted Studio, says what each choice reads as, lists its cluster first, and chooses none', async () => {
    stub()
    render(<AddLibrary options={{ buildHere: true, ssh: false, pw: true, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    // This computer is the Studio's account; the clusters are the person's.
    expect(where('This computer')).toBeInTheDocument()
    expect(where('Remote computer')).toBeNull()
    expect(screen.getByText(/read with the Studio's account, not yours/)).toBeInTheDocument()
    expect(screen.getByText(/To read it as your own account instead, choose Your clusters, then cluster-a, where this Studio runs\./)).toBeInTheDocument()
    fireEvent.click(where('Your clusters')!)
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveTextContent('Choose a cluster'))
    expect(screen.getByTestId('read-as')).toHaveTextContent("Read as you, with your ACTIVATE sign-in, never the Studio's own account.")
    fireEvent.click(screen.getByRole('combobox'))
    const options = (await screen.findAllByRole('option')).map(o => o.textContent ?? '')
    expect(options[0]).toContain('cluster-a, where this Studio runs')
    expect(options[1]).toContain('cluster-b')
    expect(options.join('|')).not.toMatch(/the Studio's cluster|remote computer/i)
  })

  it('in the apps, where every cluster is remote, is as before: Remote computer, the first running one chosen', async () => {
    stub()
    render(<AddLibrary options={{ buildHere: true, ssh: true, pw: true, workflow: false, space: true }} onAdded={() => {}} />)
    expect(where('Your clusters')).toBeNull()
    expect(screen.getByText(/read with the Studio's account/)).not.toHaveTextContent(/choose Your clusters/)
    fireEvent.click(where('Remote computer')!)
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveTextContent('cluster-b'))
    expect(screen.getByTestId('read-as')).toHaveTextContent('Read as you on cluster-b, with your ACTIVATE sign-in')
  })
})
