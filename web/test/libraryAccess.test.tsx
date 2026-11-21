import { describe, expect, it } from 'vitest'
import { libraryAccess, libraryName } from '../src/components/LibraryScope'

const base = { label: 'Runs', site: null, host: undefined, writable: false }

describe('a library\'s access, as pickers and lists name it', () => {
  it('says Read/write, Read only or Shared, and where it is', () => {
    expect(libraryAccess({ ...base, writable: true, access: { where: 'system', system: 'cluster-a', via: 'pw', readAs: 'you', write: true, index: 'built' } }))
      .toBe('Read/write (on cluster-a)')
    expect(libraryAccess({ ...base, access: { where: 'system', system: 'ada@box', via: 'ssh', readAs: 'you', write: false, index: 'connected' } }))
      .toBe('Read only (on ada@box)')
    expect(libraryAccess({ ...base, writable: true, access: { where: 'system', system: 'cluster-a', readAs: 'you', write: true, sharedFrom: 'bob', index: 'built' } }))
      .toBe('Shared (on cluster-a)')
    expect(libraryAccess({ ...base, access: { where: 'bucket', readAs: 'studio', write: false, index: 'built' } })).toBe('Read only')
    expect(libraryAccess({ ...base, host: 'cluster-a', writable: true, access: { where: 'here', readAs: 'studio', write: true, index: 'built' } }))
      .toBe('Read/write (on cluster-a)')
    expect(libraryName({ ...base, writable: true })).toBe('Runs · Read/write')
  })
})
