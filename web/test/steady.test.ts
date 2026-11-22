import { describe, expect, it } from 'vitest'
import { CONFIRM_MS, keepMissing, steadyCount } from '../src/steady'
import { steadyModels } from '../src/components/StatusFooter'

const s = (...names: string[]) => names.map(name => ({ name }))
const names = (l: { name: string }[]) => l.map(x => x.name).sort().join(',')

describe('a list the platform flaps', () => {
  it('keeps an item one read left out, and drops it when the next read agrees', () => {
    const missed = new Set<string>()
    let shown = keepMissing(null, s('a', 'b', 'c'), x => x.name, missed)
    shown = keepMissing(shown, s('a', 'b'), x => x.name, missed)
    expect(names(shown)).toBe('a,b,c')
    shown = keepMissing(shown, s('a', 'b'), x => x.name, missed)
    expect(names(shown)).toBe('a,b')
  })
  it('holds steady through a flap, 17, 16, 17', () => {
    const missed = new Set<string>()
    let shown = keepMissing(null, s('a', 'b'), x => x.name, missed)
    for (const read of [s('a'), s('a', 'b'), s('a'), s('a', 'b')]) {
      shown = keepMissing(shown, read, x => x.name, missed)
      expect(names(shown)).toBe('a,b')
    }
  })
  it('shows a new item at once', () => {
    const missed = new Set<string>()
    const shown = keepMissing(s('a'), s('a', 'b'), x => x.name, missed)
    expect(names(shown)).toBe('a,b')
  })
})

describe('a count the platform flaps', () => {
  it('rises at once, and falls when two reads in a row, a poll apart, are lower', () => {
    const low: { last: number | null; at?: number } = { last: null }
    let t = 0
    const read = (shown: number | null, n: number) => steadyCount(shown, n, low, (t += 60_000))
    expect(read(null, 23)).toBe(23)
    expect(read(23, 24)).toBe(24)
    expect(read(24, 23)).toBe(24)
    expect(read(24, 24)).toBe(24)
    expect(read(24, 23)).toBe(24)
    expect(read(24, 22)).toBe(22)
  })
  it('does not take two reads a moment apart as two looks', () => {
    const low: { last: number | null; at?: number } = { last: null }
    expect(steadyCount(24, 23, low, 1_000)).toBe(24)
    expect(steadyCount(24, 23, low, 1_500)).toBe(24)
    expect(steadyCount(24, 23, low, 1_000 + CONFIRM_MS)).toBe(23)
  })
  it('in the footer\'s models line, except a Re-check shows what it read', () => {
    const ok = (models: number) => ({ status: 'ok', models }) as never
    expect(steadyModels(ok(24), false, 0).models).toBe(24)
    expect(steadyModels(ok(23), false, 60_000).models).toBe(24)
    expect(steadyModels(ok(23), false, 60_500).models).toBe(24)
    expect(steadyModels(ok(24), false, 120_000).models).toBe(24)
    expect(steadyModels(ok(23), true).models).toBe(23)
    expect(steadyModels({ status: 'offline', models: 0 } as never).models).toBe(0)
  })
})
