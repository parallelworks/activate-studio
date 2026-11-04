import { defineConfig, mergeConfig } from 'vitest/config'
import base from './vitest.config'

// The suite again, on an engine whose Intl cannot build NumberFormat,
// PluralRules or ListFormat (WebKitGTK 4.0 on RHEL 9), with the guard from
// index.html in place. See test/intlBroken.setup.ts.
export default mergeConfig(base, defineConfig({
  test: {
    // Merged with the base's list, which already has test/setup.ts.
    setupFiles: ['./test/intlBroken.setup.ts'],
    // Breaks and restores Intl itself, so it cannot run on an already
    // broken one.
    exclude: ['test/intlGuard.test.ts', 'node_modules/**'],
  },
}))
