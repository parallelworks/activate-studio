import { describe, expect, it } from 'vitest'
import { buildOpenHash, parseOpenHash } from '../src/nav'

describe('library hash', () => {
  it('round-trips a file with slashes readable and spaces encoded', () => {
    const d = { kind: 'file' as const, target: 'proposals/inprocess/my file.md' }
    const h = buildOpenHash(d)
    expect(h).toBe('#open=file:proposals/inprocess/my%20file.md')
    expect(parseOpenHash(h)).toEqual(d)
  })
  it('carries a search query after the path', () => {
    const d = { kind: 'file' as const, target: 'CLAUDE.md', q: 'TIN OR EIN' }
    const h = buildOpenHash(d)
    expect(h).toBe('#open=file:CLAUDE.md&q=TIN%20OR%20EIN')
    expect(parseOpenHash(h)).toEqual(d)
  })
  it('parses a workflow DAG and rejects anything else', () => {
    expect(parseOpenHash('#open=workflow_dag:cfd-training')).toEqual({ kind: 'workflow_dag', target: 'cfd-training' })
    expect(parseOpenHash('#view=agents')).toBeNull()
    expect(parseOpenHash('')).toBeNull()
  })
})

describe('library in a deep link', () => {
  it('is omitted for the primary and carried for any other library, both ways', () => {
    expect(buildOpenHash({ kind: 'file', target: 'a/b.md', lib: 'kb' })).toBe('#open=file:a/b.md')
    expect(buildOpenHash({ kind: 'file', target: 'a/b.md', q: 'x y', lib: 'scratch' })).toBe('#open=file:a/b.md&q=x%20y&lib=scratch')
    expect(parseOpenHash('#open=file:a/b.md&lib=scratch')).toEqual({ kind: 'file', target: 'a/b.md', lib: 'scratch' })
    expect(parseOpenHash('#open=file:a/b.md&q=x%20y&lib=scratch')).toEqual({ kind: 'file', target: 'a/b.md', q: 'x y', lib: 'scratch' })
    expect(parseOpenHash('#open=file:a/b.md')).toEqual({ kind: 'file', target: 'a/b.md' })
  })
})
