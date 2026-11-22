import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionView } from '../src/components/SessionView'

let answer: { status: number; body: unknown }
const asked: string[] = []

beforeEach(() => {
  asked.length = 0
  answer = { status: 200, body: { name: 'desk', user: 'alex', type: 'endpoint', status: 'running', healthy: true, url: 'https://platform.example.com/me/session/alex/desk', ownHost: false, run: null, resource: null, createdAt: null } }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    asked.push(url)
    return new Response(JSON.stringify(answer.body), { status: answer.status, headers: { 'Content-Type': 'application/json' } })
  }))
  vi.useFakeTimers({ shouldAdvanceTime: true })
})
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('a session card', () => {
  it('keeps checking a running session, and says when it is gone', async () => {
    render(<SessionView user="alex" name="desk" />)
    expect(await screen.findByRole('button', { name: 'Open' })).toBeTruthy()
    answer = { status: 404, body: { error: 'No session desk is visible to you.' } }
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(await screen.findByText('Session desk is no longer listed on ACTIVATE. This card updates if it runs again.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Open' })).toBeNull()
    // Made again under the same name, it shows as running again.
    answer = { status: 200, body: { name: 'desk', user: 'alex', type: 'endpoint', status: 'running', healthy: true, url: 'https://platform.example.com/me/session/alex/desk', ownHost: false, run: null, resource: null, createdAt: null } }
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(await screen.findByRole('button', { name: 'Open' })).toBeTruthy()
  })

  it('shows a stopped session as stopped', async () => {
    render(<SessionView user="alex" name="desk" />)
    expect(await screen.findByRole('button', { name: 'Open' })).toBeTruthy()
    answer = { status: 200, body: { name: 'desk', user: 'alex', type: 'endpoint', status: 'stopped', healthy: null, url: null, ownHost: false, run: null, resource: null, createdAt: null } }
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(await screen.findByText('Session desk is stopped. This card updates when it is running.')).toBeTruthy()
  })

  it('a session not found at first is an error, not a gone session', async () => {
    answer = { status: 404, body: { error: 'No session desk is visible to you.' } }
    render(<SessionView user="alex" name="desk" />)
    expect(await screen.findByText('No session desk is visible to you.')).toBeTruthy()
    expect(screen.queryByText(/no longer listed/)).toBeNull()
  })
})
