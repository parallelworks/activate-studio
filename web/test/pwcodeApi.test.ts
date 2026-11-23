import { describe, expect, it } from 'vitest'
import { feedURL, pwClient, pwCode, sessionMachine } from '../src/pwcode/api'

describe('pw code chats over @parallelworks/code', () => {
  it('opens the feed the package names at the Studio, on the page origin', () => {
    expect(feedURL('/ws/code/machines/m2/sessions/s1/events?after=7&heartbeat=10', 'https://studio.example'))
      .toBe('wss://studio.example/api/pwcode-feed/machines/m2/sessions/s1/events?after=7&heartbeat=10')
    expect(feedURL('/ws/code/sessions?heartbeat=10', 'http://127.0.0.1:4000'))
      .toBe('ws://127.0.0.1:4000/api/pwcode-feed/sessions?heartbeat=10')
  })
  it('shows a relayed session under its machine and a direct one under the Studio', () => {
    expect(sessionMachine({ relay: true, machine: 'm2' })).toBe('m2')
    expect(sessionMachine({ relay: false, machine: 'm1' })).toBe('studio')
    expect(sessionMachine({})).toBe('studio')
  })
  it('never sends a typed command as a message when the session\'s commands could not be read', async () => {
    // The session not taken up yet: the Studio answers the palette with its
    // own reason, and the command fails with it instead of going out as text.
    const notYet = async () => new Response(JSON.stringify({ detail: 'The Studio is still connecting to this session. Try again in a moment.' }),
      { status: 409, headers: { 'Content-Type': 'application/problem+json' } })
    const loadPalette = () => pwClient.GET('/api/code/machines/{machine}/sessions/{id}/palette',
      { params: { path: { machine: 'studio', id: 's1' } }, fetch: notYet }).then(({ data }) => data ?? null)
    const deps = { machine: 'studio', sessionId: 's1', palette: null, loadPalette } as unknown as Parameters<typeof pwCode.resolveTyped>[1]
    await expect(pwCode.resolveTyped('/plan write plan28.txt', deps)).rejects.toThrow('still connecting')
    // Plain text still goes as a message without reading the palette.
    await expect(pwCode.resolveTyped('hello', deps)).resolves.toBe('hello')
    // Another route's refusal is the package's to show, as before.
    const other = await pwClient.GET('/api/code/machines/{machine}/sessions/{id}/subagents',
      { params: { path: { machine: 'studio', id: 's1' } }, fetch: notYet })
    expect(other.response.status).toBe(409)
  })
})
