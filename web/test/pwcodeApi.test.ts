import { describe, expect, it } from 'vitest'
import { feedURL, sessionMachine } from '../src/pwcode/api'

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
})
