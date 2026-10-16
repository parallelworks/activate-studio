import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Vitest runs without globals here, so Testing Library's automatic
// unmount does not register itself; each test starts from an empty body.
afterEach(() => cleanup())

// jsdom's selector engine recurses until the stack runs out on the top-layer
// states that popup positioning asks about, so opening a menu, select or
// popover took seconds per frame; nothing in jsdom is ever in the top layer.
const nativeMatches = Element.prototype.matches
Element.prototype.matches = function (this: Element, selector: string) {
  if (/:(modal|popover-open|fullscreen)\b/.test(selector)) return false
  return nativeMatches.call(this, selector)
}
