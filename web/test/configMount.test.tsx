import { act, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { hasActivate, reloadAppConfig, useAppConfig } from '../src/config'

function Part({ id }: { id: string }) {
  return <p data-testid={id}>{String(hasActivate(useAppConfig()))}</p>
}
afterEach(() => vi.unstubAllGlobals())

// The chat page mounts its parts again as a sign-in changes, after the
// event that has the configuration read again.
it('a part that mounts while the configuration is read again takes the new one', async () => {
  let platform = true
  let go: (() => void) | null = null
  let held = false
  vi.stubGlobal('fetch', vi.fn(async () => {
    const body = JSON.stringify({ platform })
    if (held) await new Promise<void>(r => { const had = go; go = () => { had?.(); r() } })
    return new Response(body, { status: 200 })
  }))
  const wait = () => act(async () => { await new Promise(r => setTimeout(r, 10)) })
  const { rerender } = render(<Part id="there" />)
  await wait()
  expect(screen.getByTestId('there')).toHaveTextContent('true')
  platform = false; held = true
  act(() => reloadAppConfig())
  rerender(<><Part id="there" /><Part id="late" /></>)
  await wait()
  go!()
  await wait()
  expect(screen.getByTestId('there')).toHaveTextContent('false')
  expect(screen.getByTestId('late')).toHaveTextContent('false')
})
