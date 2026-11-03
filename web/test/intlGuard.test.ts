import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { breakIntl, breakNumberFormat, breakPluralAndList, nativeIntl, restoreIntl, runGuard } from './intlEngine'

const nativeNumberFormat = nativeIntl.NumberFormat
const nativeToLocale = nativeIntl.numberToLocale

beforeEach(() => { delete (window as { __intlGuard?: boolean }).__intlGuard })
afterEach(() => {
  restoreIntl()
  vi.resetModules()
})

describe('number formatting guard in index.html', () => {
  it('changes nothing when Intl.NumberFormat works', () => {
    runGuard()
    expect(Intl.NumberFormat).toBe(nativeNumberFormat)
    expect(Number.prototype.toLocaleString).toBe(nativeToLocale)
    expect((window as { __intlGuard?: boolean }).__intlGuard).toBeUndefined()
  })

  it('stands in for a NumberFormat that throws', () => {
    breakNumberFormat()
    expect(() => new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 })).toThrow()
    runGuard()
    expect((window as { __intlGuard?: boolean }).__intlGuard).toBe(true)
    const f = (o?: Intl.NumberFormatOptions) => new Intl.NumberFormat(undefined, o)
    expect(f().format(1234.5)).toBe('1,234.5')
    expect(f().format(1234567)).toBe('1,234,567')
    expect(f({ maximumFractionDigits: 2 }).format(1536 / 1024)).toBe('1.5')
    expect(f({ maximumFractionDigits: 2 }).format(0)).toBe('0')
    expect(f({ minimumFractionDigits: 1 }).format(3)).toBe('3.0')
    expect(f({ style: 'percent' }).format(0.5)).toBe('50%')
    expect(f({ style: 'currency', currency: 'USD' }).format(1234.5)).toBe('$1,234.50')
    expect(f({ notation: 'compact', maximumFractionDigits: 1 }).format(1234)).toBe('1.2K')
    expect(f({ useGrouping: false }).format(12345)).toBe('12345')
    expect(f().format(-1234.5)).toBe('-1,234.5')
    expect(f().format(NaN)).toBe('NaN')
    expect(f().formatToParts(5)).toEqual([{ type: 'literal', value: '5' }])
    expect(f().resolvedOptions().locale).toBe('en-US')
    expect(Intl.NumberFormat.supportedLocalesOf('en-US')).toEqual([])
    // Called without new, as Intl allows.
    expect((Intl.NumberFormat as unknown as (l?: string) => Intl.NumberFormat)('en-US').format(1000)).toBe('1,000')
  })

  it('stands in for PluralRules that throws and a missing ListFormat', () => {
    breakPluralAndList()
    runGuard()
    const L = (Intl as unknown as { ListFormat: new (l?: string, o?: object) => { format(v: string[]): string } }).ListFormat
    const plural = (o?: Intl.PluralRulesOptions) => new Intl.PluralRules('en', o)
    expect(plural().select(1)).toBe('one')
    expect(plural().select(2)).toBe('other')
    expect(plural().select(0)).toBe('other')
    expect(plural({ type: 'ordinal' }).select(21)).toBe('one')
    expect(plural({ type: 'ordinal' }).select(22)).toBe('two')
    expect(plural({ type: 'ordinal' }).select(23)).toBe('few')
    expect(plural({ type: 'ordinal' }).select(11)).toBe('other')
    expect(new L('en').format(['a'])).toBe('a')
    expect(new L('en').format(['a', 'b'])).toBe('a and b')
    expect(new L('en').format(['a', 'b', 'c'])).toBe('a, b, and c')
    expect(new L('en', { type: 'disjunction' }).format(['a', 'b', 'c'])).toBe('a, b, or c')
  })

  it('makes toLocaleString on numbers and bigints work', () => {
    breakNumberFormat()
    expect(() => (1234.5).toLocaleString()).toThrow()
    runGuard()
    expect((1234567.891).toLocaleString()).toBe('1,234,567.891')
    expect((1234.5).toLocaleString(undefined, { maximumFractionDigits: 0 })).toBe('1,235')
    expect((12345n).toLocaleString()).toBe('12,345')
  })

  it('lets the UI library and the app load when Intl.NumberFormat throws', async () => {
    breakIntl()
    runGuard()
    vi.resetModules()
    const ui = await import('@parallelworks/ui')
    expect(Object.keys(ui).length).toBeGreaterThan(0)
    const app = await import('../src/App')
    expect(app.default ?? app).toBeTruthy()
  }, 60_000)

  it('shows the failure without the guard, so the test above means something', async () => {
    breakIntl()
    vi.resetModules()
    await expect(import('@parallelworks/ui').then(() => import('../src/App'))).rejects.toThrow('failed to initialize')
  }, 60_000)

  // The regression check for the next library update or new module: every
  // module of the app and every package it depends on, including the ones
  // the page loads late (the Markdown renderer, the 3D viewer, the code
  // editor), is loaded on the broken engine with the guard in place. One
  // that builds an Intl formatter the guard does not replace, at load time,
  // fails here instead of leaving a RHEL 9 desktop on the loading screen.
  describe('on a broken engine with the guard', () => {
    const appModules = import.meta.glob('../src/**/*.{ts,tsx}')
    const entry = new Set(['../src/main.tsx'])

    it('loads every module of the app', async () => {
      breakIntl()
      runGuard()
      vi.resetModules()
      const names = Object.keys(appModules).filter(n => !entry.has(n))
      expect(names.length).toBeGreaterThan(50)
      const failed: string[] = []
      for (const n of names) {
        try { await appModules[n]() } catch (e) { failed.push(`${n}: ${String((e as Error).message ?? e).slice(0, 200)}`) }
      }
      expect(failed).toEqual([])
    }, 120_000)

    it.each(['@parallelworks/ui', '@parallelworks/workflow-parser', 'streamdown', 'three', 'yaml', 'react-hook-form'])(
      'loads %s', async pkg => {
        breakIntl()
        runGuard()
        vi.resetModules()
        await expect(import(/* @vite-ignore */ pkg)).resolves.toBeTruthy()
      }, 60_000)

    it('loads the code editor', async () => {
      breakIntl()
      runGuard()
      vi.resetModules()
      await expect(import('monaco-editor')).resolves.toBeTruthy()
    }, 60_000)
  })
})
