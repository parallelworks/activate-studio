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
    expect(parseOpenHash('#open=workflow_dag:training-flow')).toEqual({ kind: 'workflow_dag', target: 'training-flow' })
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

describe('a link that cannot name a file', () => {
  // A pw code session's image address read as base64, as a chat attachment
  // once was: the Library opened "Cannot open this file" on the garbage.
  const garbage = atob('/api/code/machines/studio/sessions/6f1c2a7e+5b9d+4e3a+8c21+000000000002/images/9f86d081884c7d650000000000000001')
  it('opens nothing for control characters or bytes that were not text', () => {
    expect(parseOpenHash(`#open=file:${encodeURIComponent(garbage)}`)).toBeNull()
    expect(parseOpenHash('#open=file:notes%00.md')).toBeNull()
    expect(parseOpenHash('#open=file:notes%EF%BF%BD.md')).toBeNull()
  })
  it('opens nothing for a path that is not percent-encoded right', () => {
    expect(parseOpenHash('#open=file:notes%E0%A4%A.md')).toBeNull()
  })
  it('still opens names with spaces and letters past ASCII', () => {
    expect(parseOpenHash('#open=file:Team%20Knowledge%20Base/R%C3%A9sum%C3%A9.pdf')).toEqual({ kind: 'file', target: 'Team Knowledge Base/Résumé.pdf' })
  })
})
