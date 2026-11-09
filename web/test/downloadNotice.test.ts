import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as ui from '@parallelworks/ui'
import { downloadedMessage, installDownloadNotices, savedMessage, SAVED_EVENT } from '../src/downloadNotice'

let stop = () => {}
let success: ReturnType<typeof vi.spyOn>
let error: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  success = vi.spyOn(ui.toast, 'success').mockImplementation(() => '' as never)
  error = vi.spyOn(ui.toast, 'error').mockImplementation(() => '' as never)
  stop = installDownloadNotices()
})
afterEach(() => {
  stop()
  vi.restoreAllMocks()
  delete (window as unknown as Record<string, unknown>).__studioShell
  delete (window as unknown as Record<string, unknown>).webkit
  delete (window as unknown as Record<string, unknown>).wails
})

/** What the chat's Download does: a link to a blob, clicked and removed. */
function exportLike(name: string) {
  const a = document.createElement('a')
  a.href = 'blob:http://127.0.0.1/0f1e'
  a.download = name
  a.addEventListener('click', e => e.preventDefault()) // jsdom does not navigate
  document.body.appendChild(a)
  a.click()
  a.remove()
}

describe('a notice for each file saved', () => {
  it('says where the app window saved it, and why it could not', () => {
    expect(savedMessage({ name: 'budget.md', path: '/home/me/Downloads/budget (2).md' })).toEqual({ ok: true, text: 'Saved budget (2).md to /home/me/Downloads.' })
    expect(savedMessage({ name: 'budget.md', path: 'C:\\Users\\me\\Downloads\\budget.md' }).text).toBe('Saved budget.md to C:\\Users\\me\\Downloads.')
    expect(savedMessage({ name: 'budget.md', error: 'permission denied' })).toEqual({ ok: false, text: 'Could not save budget.md: permission denied.' })
    expect(downloadedMessage('budget.md', false)).toBe("Downloaded budget.md to your browser's downloads folder.")
    expect(downloadedMessage('budget.md', true)).toBe('Saved budget.md to Downloads.')
  })

  it('in a browser, confirms a conversation exported from the chat list', () => {
    exportLike('budget-review.md')
    expect(success).toHaveBeenCalledWith("Downloaded budget-review.md to your browser's downloads folder.")
    // A plain link to a page is not a save.
    const a = document.createElement('a'); a.href = '/api/x'; a.addEventListener('click', e => e.preventDefault())
    document.body.appendChild(a); a.click(); a.remove()
    expect(success).toHaveBeenCalledTimes(1)
  })

  it('in the app window, waits for the shell to say where the file went', () => {
    Object.assign(window, { __studioShell: {} })
    exportLike('budget-review.md')
    expect(success).not.toHaveBeenCalled()
    window.dispatchEvent(new CustomEvent(SAVED_EVENT, { detail: { name: 'budget-review.md', path: '/home/me/Downloads/budget-review.md' } }))
    expect(success).toHaveBeenCalledWith('Saved budget-review.md to /home/me/Downloads.')
    window.dispatchEvent(new CustomEvent(SAVED_EVENT, { detail: { name: 'budget-review.md', error: 'disk full' } }))
    expect(error).toHaveBeenCalledWith('Could not save budget-review.md: disk full.')
  })

  it('the Linux and Windows shell reports the path the bridge wrote, or its error', async () => {
    // The shell's script, run in this page with Wails' message channel faked.
    const answers: Record<string, { result?: string; error?: string }> = {
      SaveDownload: { result: '/home/me/Downloads/notes.md' },
    }
    const calls: string[] = []
    Object.assign(window, { webkit: { messageHandlers: { external: { postMessage(s: string) {
      const m = JSON.parse(s.slice(1)) as { name: string; callbackID: string }
      const method = m.name.replace('main.Bridge.', '')
      calls.push(method)
      const a = answers[method] ?? { error: 'no such call' }
      queueMicrotask(() => (window as unknown as { wails: { Callback(j: string): void } }).wails.Callback(JSON.stringify({ callbackid: m.callbackID, ...a })))
    } } } } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response('# notes')))
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    const shim = readFileSync('../desktop/wails/shell/shim.js', 'utf8').replace('__STUDIO_SHELL_CONFIG__', '{}')
    new Function(shim)()
    expect('__studioShell' in window).toBe(true)
    const a = document.createElement('a'); a.href = 'blob:http://127.0.0.1/1'; a.download = 'notes.md'
    document.body.appendChild(a); a.click(); a.remove()
    await vi.waitFor(() => expect(success).toHaveBeenCalledWith('Saved notes.md to /home/me/Downloads.'))
    expect(calls).toContain('SaveDownload')
    answers.SaveDownload = { error: 'permission denied' }
    const b = document.createElement('a'); b.href = 'blob:http://127.0.0.1/2'; b.download = 'notes.md'
    vi.spyOn(console, 'error').mockImplementation(() => {})
    document.body.appendChild(b); b.click(); b.remove()
    await vi.waitFor(() => expect(error).toHaveBeenCalledWith('Could not save notes.md: permission denied.'))
  })
})
