import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

// The session views (a pw code chat's and the Code page's) are the
// package's, drawn as PW Code on the web draws them. The Studio's restyles of
// the package's chat markup are for its own thread: one that reached in laid
// a tool step's output beside its call, cutting every line.

const GUARD = ':where(:not(.pwcode-session *, .code-page *))'

/** Every selector in the stylesheet, comments dropped, at-rules entered. */
function selectors(css: string): string[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: string[] = []
  const walk = (src: string) => {
    let i = 0
    while (i < src.length) {
      const open = src.indexOf('{', i)
      if (open < 0) break
      const head = src.slice(i, open).trim()
      let depth = 1, j = open + 1
      while (j < src.length && depth) { if (src[j] === '{') depth++; else if (src[j] === '}') depth--; j++ }
      if (head.startsWith('@')) walk(src.slice(open + 1, j - 1))
      else out.push(...head.split(/,(?![^(]*\))/).map(s => s.trim()))
      i = j
    }
  }
  walk(text)
  return out
}

const all = selectors(readFileSync(path.resolve(process.cwd(), 'src/styles.css'), 'utf8'))

// Selectors that name the package's chat markup: its test ids, streamdown's
// parts, its utility classes, its row and menu classes.
const PACKAGE = /\[data-testid|\[data-streamdown|\[class[*^]=|rounded-br-md|div\.top-full|chat-reasoning-tail|chat-prompt-pin/

// Those that may reach the package's markup anywhere: the conversations
// list (outside the session), the monospace font, a rule already kept out of
// the session, and the Transfers page.
const ALLOWED = new Set([
  '.chat-canvas > .flex.h-full > :first-child > div[class*="justify-between"]',
  '.chat-canvas aside[class*="bg-(--theme-muted-panel-bg)"]',
  '.chat-canvas li.group[class*=" bg-[color-mix"]',
  "[data-theme='dark'] .chat-canvas li.group[class*=\" bg-[color-mix\"]",
  '.chat-canvas li.group[class*=" bg-[color-mix"] span',
  "[data-theme='dark'] .chat-canvas li.group[class*=\" bg-[color-mix\"] span",
  "[data-streamdown='code-block']",
  "[data-streamdown='inline-code']",
  ".chat-canvas[data-engine='pwcode'] .chat-composer:not(.pwcode-session *) [class*='justify-end'] > :is(button[aria-haspopup='dialog'], span.cursor-default)",
  ".chat-composer:not(.pwcode-session *) [class*='shrink-[10]']:has(> .pwcode-new-model)",
  '.xfer-list > [class*="callout"]',
])

describe('the Studio\'s restyles of the package\'s chat', () => {
  it('stay out of the session views', () => {
    const reaching = all.filter(s => PACKAGE.test(s) && !s.includes(GUARD) && !ALLOWED.has(s))
    expect(reaching).toEqual([])
  })
  it('keep a tool step\'s output under its call there', () => {
    const toolRows = all.filter(s => s.includes('tool-call-part'))
    expect(toolRows.length).toBeGreaterThan(0)
    for (const s of toolRows) expect(s).toContain(GUARD)
  })
  it('leave the session thread\'s top to the band under the header buttons', () => {
    // The band sticks to the top of the thread's content box: padding on
    // the session's thread pushed it down off the buttons, so text ran
    // behind them and the band hid a line further down.
    const padded = all.filter(s => /pwcode-session[^,]*\.chat-thread$/.test(s))
    expect(padded).toEqual([])
  })
  it('leave the session its own pinned prompt off', () => {
    // The chat's header buttons float where the package pins the session's
    // first prompt, so the Studio does not ask for it (PWCodeSession.tsx).
    const src = ['src/pwcode/PWCodeSession.tsx', 'src/pwcode/session/SubagentView.tsx']
      .map(f => readFileSync(path.resolve(process.cwd(), f), 'utf8')).join('\n')
    expect(src).not.toMatch(/\bpinPrompt\b/)
  })
})
