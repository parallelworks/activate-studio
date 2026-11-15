import { describe, expect, it } from 'vitest'
import { fileRows, fmtBytes } from '../src/views/OverviewView'

describe('the Stats file lists', () => {
  it('read one library\'s rows by column name', () => {
    const rows = fileRows({ columns: ['path', 'bytes', 'modified'], rows: [['notes/a.md', '2048', '2026-10-18 09:00:00']] })
    expect(rows[0]).toMatchObject({ path: 'notes/a.md', bytes: 2048, modified: '2026-10-18 09:00:00', library: null, open: 'notes/a.md' })
  })

  it('read rows across libraries past the leading library column, and open them in their library', () => {
    const rows = fileRows({
      columns: ['library', 'path', 'bytes', 'modified'],
      rows: [['Knowledge base', 'notes/a.md', '2048', '2026-10-18 09:00:00'], ['Cluster scratch', 'runs/b.log', '10', '2026-10-17 08:00:00']],
      rowLibraries: ['kb', 'scratch'],
    })
    expect(rows.map(r => [r.path, r.bytes, r.modified.slice(0, 10), r.library, r.open])).toEqual([
      ['notes/a.md', 2048, '2026-10-18', 'Knowledge base', 'kb/notes/a.md'],
      ['runs/b.log', 10, '2026-10-17', 'Cluster scratch', 'scratch/runs/b.log'],
    ])
  })
})

describe('sizes on Stats', () => {
  it('step up to TB and PB so a large library fits its tile', () => {
    expect(fmtBytes(512)).toBe('512 B')
    expect(fmtBytes(5 * 1024 ** 3)).toBe('5.00 GB')
    expect(fmtBytes(3576601920278)).toBe('3.25 TB')
    expect(fmtBytes(2 * 1024 ** 5)).toBe('2.00 PB')
  })
})
