import { readFileSync } from 'node:fs'
import { join } from 'node:path'

// An engine whose Intl cannot build what the guard in index.html replaces,
// as in the WebKitGTK 4.0 that RHEL 9 and its rebuilds ship (ICU 67):
// NumberFormat and PluralRules throw "failed to initialize", toLocaleString
// on a number fails, and ListFormat does not exist. Date formatting,
// collation, relative time, display names and segmentation work there and
// are left alone here.

/** The guard as shipped: the inline script in index.html. */
const html = readFileSync(join(__dirname, '..', 'index.html'), 'utf8')
export const guardSource = /<script id="intl-guard">([\s\S]*?)<\/script>/.exec(html)![1]
export const runGuard = () => new Function(guardSource)()

export const nativeIntl = {
  NumberFormat: Intl.NumberFormat,
  PluralRules: Intl.PluralRules,
  ListFormat: (Intl as { ListFormat?: unknown }).ListFormat,
  numberToLocale: Number.prototype.toLocaleString,
  bigintToLocale: BigInt.prototype.toLocaleString,
}

const define = (target: object, name: string, value: unknown) =>
  Object.defineProperty(target, name, { value, configurable: true, writable: true })

export function breakNumberFormat() {
  const broken = function () { throw new TypeError('failed to initialize NumberFormat') }
  define(Intl, 'NumberFormat', broken)
  define(Number.prototype, 'toLocaleString', broken)
  define(BigInt.prototype, 'toLocaleString', broken)
}

export function breakPluralAndList() {
  define(Intl, 'PluralRules', function () { throw new TypeError('failed to initialize PluralRules') })
  define(Intl, 'ListFormat', undefined)
}

export function breakIntl() {
  breakNumberFormat()
  breakPluralAndList()
}

export function restoreIntl() {
  define(Intl, 'NumberFormat', nativeIntl.NumberFormat)
  define(Intl, 'PluralRules', nativeIntl.PluralRules)
  define(Intl, 'ListFormat', nativeIntl.ListFormat)
  define(Number.prototype, 'toLocaleString', nativeIntl.numberToLocale)
  define(BigInt.prototype, 'toLocaleString', nativeIntl.bigintToLocale)
}
