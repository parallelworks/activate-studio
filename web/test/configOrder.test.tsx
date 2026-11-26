import { act, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { hasActivate, reloadAppConfig, useAppConfig } from '../src/config'

function Nav() {
  const cfg = useAppConfig()
  return <p data-testid="nav">{cfg.loaded ? (hasActivate(cfg) ? 'Code' : 'no Code') : 'loading'}</p>
}
afterEach(() => vi.unstubAllGlobals())

// Right after an ACTIVATE sign-in, a read of the configuration begun just
// before it finished (on returning to the tab, say) can be answered after
// the read the sign-in asked for. The older answer must not put back the
// navigation without Code.
it('keeps the newest answer when an older read of the configuration answers last', async () => {
  let platform = false
  const held: (() => void)[] = []
  let hold = false
  vi.stubGlobal('fetch', vi.fn(async () => {
    const body = JSON.stringify({ platform })
    if (hold) await new Promise<void>(r => held.push(r))
    return new Response(body, { status: 200 })
  }))
  const wait = () => act(async () => { await new Promise(r => setTimeout(r, 10)) })
  render(<Nav />)
  await wait()
  expect(screen.getByTestId('nav')).toHaveTextContent('no Code')
  // A read begun before the sign-in finished, still out.
  hold = true
  act(() => reloadAppConfig())
  await wait()
  // The sign-in finishes; the read it asks for is answered at once.
  hold = false; platform = true
  act(() => reloadAppConfig())
  await wait()
  expect(screen.getByTestId('nav')).toHaveTextContent('Code')
  // The older read answers last, with what was so before the sign-in.
  held.forEach(r => r())
  await wait()
  expect(screen.getByTestId('nav')).toHaveTextContent(/^Code$/)
  // A part that mounts now takes the newest answer too.
  render(<Nav />)
  await wait()
  expect(screen.getAllByTestId('nav').map(n => n.textContent)).toEqual(['Code', 'Code'])
})
