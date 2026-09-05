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
    expect(parseOpenHash('#open=workflow_dag:dewd-training')).toEqual({ kind: 'workflow_dag', target: 'dewd-training' })
    expect(parseOpenHash('#view=agents')).toBeNull()
    expect(parseOpenHash('')).toBeNull()
  })
})
