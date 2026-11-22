import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

// Phase 4, T: at 1280 with Status open, the pw CLI contexts table scrolled
// sideways in a 438 px box with its Use buttons off screen. A context's
// name wraps, and a narrow section stacks each context with Use beside it.
describe('the pw CLI contexts fit a narrow section', () => {
  const css = readFileSync('src/styles.css', 'utf8')
  it('wraps a context name and stacks the rows when narrow', () => {
    expect(css).toMatch(/\.cli-contexts-wrap:has\(\.cli-contexts\) \{[^}]*container-type: inline-size/)
    expect(css).toMatch(/\.cli-contexts code \{[^}]*overflow-wrap: anywhere/)
    expect(css).toMatch(/@container \(max-width: 560px\) \{[^@]*\.cli-contexts tr \{[^}]*display: grid/)
  })
  // T2: a stacked cell kept the table's fixed h-8 height, so a long name
  // that wrapped ran over the platform line under it.
  it('lets a stacked cell grow with a wrapped name', () => {
    expect(css).toMatch(/@container \(max-width: 560px\) \{[^@]*\.cli-contexts td \{[^}]*height: auto/)
  })
})
