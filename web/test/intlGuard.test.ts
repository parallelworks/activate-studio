import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The guard is the inline script in index.html, so what ships is what is
// tested: it is pulled out of the page and run.
const html = readFileSync(join(__dirname, '..', 'index.html'), 'utf8')
const source = /<script id="intl-guard">([\s\S]*?)<\/script>/.exec(html)![1]
const runGuard = () => new Function(source)()

const nativeNumberFormat = Intl.NumberFormat
const nativeToLocale = Number.prototype.toLocaleString
const nativeBigIntToLocale = BigInt.prototype.toLocaleString
const nativePluralRules = Intl.PluralRules
const nativeListFormat = (Intl as { ListFormat?: unknown }).ListFormat

// An engine whose Intl.NumberFormat cannot be built, as in the WebKitGTK 4.0
// that RHEL 9 and its rebuilds ship.
function breakNumberFormat() {
  const broken = function () { throw new TypeError('failed to initialize NumberFormat') }
  Object.defineProperty(Intl, 'NumberFormat', { value: broken, configurable: true, writable: true })
  const toLocale = function () { throw new TypeError('failed to initialize NumberFormat') }
  Object.defineProperty(Number.prototype, 'toLocaleString', { value: toLocale, configurable: true, writable: true })
  Object.defineProperty(BigInt.prototype, 'toLocaleString', { value: toLocale, configurable: true, writable: true })
}

// The same engine's PluralRules, and no ListFormat at all.
function breakPluralAndList() {
  const broken = function () { throw new TypeError('failed to initialize PluralRules') }
  Object.defineProperty(Intl, 'PluralRules', { value: broken, configurable: true, writable: true })
  Object.defineProperty(Intl, 'ListFormat', { value: undefined, configurable: true, writable: true })
}

beforeEach(() => { delete (window as { __intlGuard?: boolean }).__intlGuard })
afterEach(() => {
  Object.defineProperty(Intl, 'NumberFormat', { value: nativeNumberFormat, configurable: true, writable: true })
  Object.defineProperty(Number.prototype, 'toLocaleString', { value: nativeToLocale, configurable: true, writable: true })
  Object.defineProperty(BigInt.prototype, 'toLocaleString', { value: nativeBigIntToLocale, configurable: true, writable: true })
  Object.defineProperty(Intl, 'PluralRules', { value: nativePluralRules, configurable: true, writable: true })
  Object.defineProperty(Intl, 'ListFormat', { value: nativeListFormat, configurable: true, writable: true })
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
    breakNumberFormat()
    breakPluralAndList()
    runGuard()
    vi.resetModules()
    const ui = await import('@parallelworks/ui')
    expect(Object.keys(ui).length).toBeGreaterThan(0)
    const app = await import('../src/App')
    expect(app.default ?? app).toBeTruthy()
  })

  it('shows the failure without the guard, so the test above means something', async () => {
    breakNumberFormat()
    breakPluralAndList()
    vi.resetModules()
    await expect(import('@parallelworks/ui').then(() => import('../src/App'))).rejects.toThrow('failed to initialize')
  })
})
