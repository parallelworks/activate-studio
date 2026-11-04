// Setup for the second test pass (vitest.rhel9.config.ts): the whole suite
// runs on an engine whose Intl cannot build NumberFormat, PluralRules or
// ListFormat, with the guard from index.html applied first, as the page
// does before its bundle loads. A module that builds one of these at load
// time, or a view that formats a number through something the guard does
// not stand in for, fails its test here and passes in the normal pass.
import { breakIntl, runGuard } from './intlEngine'

breakIntl()
runGuard()
if (!(window as { __intlGuard?: boolean }).__intlGuard) throw new Error('the guard did not engage on a broken Intl')
