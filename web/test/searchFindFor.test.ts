import { describe, expect, it } from 'vitest'
import { findFor } from '../src/views/SearchView'

describe('the find bar of a file opened from a search', () => {
  it('starts with the words searched for when the file was found by its words', () => {
    expect(findFor({ mode: 'fts' }, 'airplanes')).toBe('airplanes')
    expect(findFor({ mode: 'name', kinds: ['name', 'fts', 'vector'] }, 'airplanes')).toBe('airplanes')
  })

  it('starts empty when the file was found only by its meaning, name or folder', () => {
    expect(findFor({ mode: 'vector' }, 'airplanes')).toBeUndefined()
    expect(findFor({ mode: 'name', kinds: ['name', 'vector'] }, 'airplanes')).toBeUndefined()
    expect(findFor({ mode: 'path' }, 'airplanes')).toBeUndefined()
  })

  it('starts empty for a refined search by name alone', () => {
    expect(findFor({ mode: 'fts' }, '')).toBeUndefined()
  })
})
