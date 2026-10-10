import { describe, expect, it } from 'vitest'
import { frameVerdict, siteOf } from '../src/sessionFrame'

describe('whether a session can be framed', () => {
  const studio = { host: 'my-studio.sessions.example.com', topIsSelf: true }
  const own = { url: 'https://explorer.sessions.example.com/', ownHost: true }

  it('frames a session on its own host under the Studio\'s site', () => {
    expect(frameVerdict(own, studio)).toBe('frame')
  })
  it('sends a platform-address session to its own tab', () => {
    expect(frameVerdict({ url: 'https://platform.example.org/me/session/u/code', ownHost: false }, studio)).toBe('platform-address')
  })
  it('does not frame when the Studio itself is inside the platform page', () => {
    expect(frameVerdict(own, { ...studio, topIsSelf: false })).toBe('inside-platform')
  })
  it('does not frame across sites, or from a local address', () => {
    expect(frameVerdict(own, { host: 'studio.other.org', topIsSelf: true })).toBe('other-site')
    expect(frameVerdict(own, { host: '127.0.0.1', topIsSelf: true })).toBe('other-site')
    expect(frameVerdict(own, { host: 'localhost', topIsSelf: true })).toBe('other-site')
  })
  it('needs an address', () => {
    expect(frameVerdict({ url: null, ownHost: true }, studio)).toBe('no-address')
  })
  it('takes a host\'s last two labels as its site', () => {
    expect(siteOf('a.b.Example.COM')).toBe('example.com')
    expect(siteOf('localhost')).toBeNull()
    expect(siteOf('10.0.0.1')).toBeNull()
  })
})
