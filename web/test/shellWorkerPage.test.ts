import { describe, expect, it } from 'vitest'
import { inDesktopWindow, pageEntries, runsBuild, shellWorkerWanted } from '../src/shellWorker'

const win = (o: Record<string, unknown> = {}) =>
  ({ navigator: { serviceWorker: {} }, isSecureContext: true, ...o }) as unknown as Window

describe('where the app shell is kept', () => {
  it('in a production page in a browser, over HTTPS or on this computer', () => {
    expect(shellWorkerWanted(win(), true)).toBe(true)
    expect(shellWorkerWanted(win(), false)).toBe(false)
    expect(shellWorkerWanted(win({ isSecureContext: false }), true)).toBe(false)
    expect(shellWorkerWanted(win({ navigator: {} }), true)).toBe(false)
  })

  it('never in the desktop apps’ windows, whose proxy writes into each page', () => {
    expect(inDesktopWindow(win({ __studioShell: {} }))).toBe(true)
    expect(inDesktopWindow(win({ webkit: { messageHandlers: { studioShell: {} } } }))).toBe(true)
    expect(shellWorkerWanted(win({ __studioShell: {} }), true)).toBe(false)
    expect(inDesktopWindow(win({ webkit: { messageHandlers: {} } }))).toBe(false)
  })

  it('tells whether the page runs a build from its entry files', () => {
    document.head.innerHTML = '<script type="module" crossorigin src="/assets/index-AAAAAAAA.js"></script><link rel="stylesheet" href="/assets/index-BBBBBBBB.css"><link rel="stylesheet" href="https://fonts.example/x.css">'
    expect(pageEntries()).toEqual(['/assets/index-AAAAAAAA.js', '/assets/index-BBBBBBBB.css'])
    expect(runsBuild(['/assets/index-AAAAAAAA.js', '/assets/index-BBBBBBBB.css'])).toBe(true)
    expect(runsBuild(['/assets/index-CCCCCCCC.js', '/assets/index-BBBBBBBB.css'])).toBe(false)
    expect(runsBuild([])).toBe(false)
  })
})
