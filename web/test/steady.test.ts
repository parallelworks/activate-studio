import { describe, expect, it } from 'vitest'
import { keepMissing, steadyCount } from '../src/steady'
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
  it('rises at once, and falls when two reads in a row are lower', () => {
    const low = { last: null as number | null }
    expect(steadyCount(null, 23, low)).toBe(23)
    expect(steadyCount(23, 24, low)).toBe(24)
    expect(steadyCount(24, 23, low)).toBe(24)
    expect(steadyCount(24, 24, low)).toBe(24)
    expect(steadyCount(24, 23, low)).toBe(24)
    expect(steadyCount(24, 22, low)).toBe(22)
  })
  it('in the footer\'s models line, except a Re-check shows what it read', () => {
    const ok = (models: number) => ({ status: 'ok', models }) as never
    expect(steadyModels(ok(24)).models).toBe(24)
    expect(steadyModels(ok(23)).models).toBe(24)
    expect(steadyModels(ok(24)).models).toBe(24)
    expect(steadyModels(ok(23), true).models).toBe(23)
    expect(steadyModels({ status: 'offline', models: 0 } as never).models).toBe(0)
  })
})
