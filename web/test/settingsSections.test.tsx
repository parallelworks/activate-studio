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

  it('shows Accounts and Model access where the deployment signs users in, and on a single-user install', () => {
    const ids = (o: Parameters<typeof settingsSections>[0]) => settingsSections(o).map(s => s.id)
    // A deployment that keeps no personal credentials: neither.
    expect(ids({ authEnabled: false })).not.toContain('accounts')
    expect(ids({ authEnabled: false })).not.toContain('access')
    expect(ids({ authEnabled: true })).toEqual(expect.arrayContaining(['accounts', 'access']))
    // Sign-in off with personal keys allowed: the one user's accounts.
    expect(ids({ authEnabled: false, single: true })).toEqual(expect.arrayContaining(['accounts', 'access']))
  })

  it('shows Workflows when the Studio reaches the platform, or where Accounts can sign in to it', () => {
    expect(settingsSections({ authEnabled: false }).map(s => s.id)).not.toContain('workflows')
    expect(settingsSections({ authEnabled: false, platform: true }).map(s => s.id)).toContain('workflows')
    // Before anyone signs in, it shows to say so.
    expect(settingsSections({ authEnabled: true }).map(s => s.id)).toContain('workflows')
    expect(settingsSections({ authEnabled: false, single: true }).map(s => s.id)).toContain('workflows')
  })
})

describe('the settings rail groups', () => {
  it('groups the Studio and Assistant sections, and leaves External access on its own', () => {
    const rows = settingsSections({ authEnabled: true, platform: true })
    const by = (g: string | null) => rows.filter(r => r.group === g).map(r => r.id)
    expect(by('Studio')).toEqual(['general', 'indexing', 'workflows', 'accounts'])
    expect(by('Assistant')).toEqual(['access', 'tools', 'previews', 'ext'])
    expect(by(null)).toEqual(['rag'])
  })
})
