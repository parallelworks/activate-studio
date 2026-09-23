import { describe, expect, it } from 'vitest'
import { settingsSections } from '../src/views/SettingsView'

describe('the settings rail', () => {
  it('offers Feature previews as its own section, not a heading inside another', () => {
    const ids = settingsSections({ authEnabled: false }).map(s => s.id)
    expect(ids).toContain('previews')
    // It sits with the other capability sections, after the assistant's
    // tools and before extensions.
    expect(ids.indexOf('previews')).toBeGreaterThan(ids.indexOf('tools'))
    expect(ids.indexOf('previews')).toBeLessThan(ids.indexOf('ext'))
  })

  it('shows Model access only where the deployment signs users in', () => {
    expect(settingsSections({ authEnabled: false }).map(s => s.id)).not.toContain('access')
    expect(settingsSections({ authEnabled: true }).map(s => s.id)).toContain('access')
  })
})
