import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect } from 'vitest'
// The folder menu's parts load with the form. They are many, so on a busy
// machine a test that waited for them reached its time limit: they are
// loaded with the tests instead.
import '../src/components/addLibraryFolder'

/** Steps of Add a library, as a person takes them. */

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })

/** The folder menu's route, for a fetch stand-in: whatever is asked is there. */
export function folderRoute(url: string): Response | undefined {
  if (!/^\/api\/(user-libraries|libraries)\/folders\?/.test(url)) return undefined
  const path = new URLSearchParams(url.split('?')[1]).get('path') || '/home/ada'
  return json({ path, home: '/home/ada', entries: [{ name: 'papers' }] })
}

/** The computers the menu lists, as it reads them. */
export async function computers(): Promise<HTMLElement[]> {
  fireEvent.click(await screen.findByTestId('add-library-computer'))
  return waitFor(() => {
    const rows = [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')]
    expect(rows.length).toBeGreaterThan(0)
    return rows
  })
}
export const closeMenu = () => fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' })

export async function chooseComputer(name: string | RegExp) {
  const row = (await computers()).find(e => (typeof name === 'string' ? (e.textContent ?? '').startsWith(name) : name.test(e.textContent ?? '')))
  if (!row) throw new Error(`no computer ${name}`)
  fireEvent.click(row)
}

/** A host or a cluster typed by name. */
export async function nameComputer(kind: 'host' | 'cluster', name: string) {
  await chooseComputer(kind === 'host' ? 'Another host…' : 'Another cluster…')
  const field = await screen.findByRole('textbox', { name: kind === 'host' ? 'Host' : 'Cluster' })
  fireEvent.change(field, { target: { value: name } })
  fireEvent.keyDown(field, { key: 'Enter' })
}

/** The folder menu of the computer chosen, open. */
export async function openFolders(): Promise<HTMLInputElement> {
  // The menu's parts load with the form: until then its button opens nothing.
  await waitFor(() => expect(screen.getAllByTestId('add-library-folder')[0]).toHaveAttribute('aria-haspopup'), { timeout: 4000 })
  fireEvent.click(screen.getAllByTestId('add-library-folder')[0])
  return waitFor(() => {
    const field = document.querySelector<HTMLInputElement>('.code-path-field input')
    expect(field).not.toBeNull()
    return field!
  })
}

export async function chooseFolder(path: string) {
  const field = await openFolders()
  fireEvent.change(field, { target: { value: `${path}/` } })
  await waitFor(() => expect([...document.querySelectorAll('[role="option"]')].some(o => o.textContent?.startsWith(path))).toBe(true))
  fireEvent.keyDown(field, { key: 'Enter' })
  await waitFor(() => expect(screen.getAllByTestId('add-library-folder').some(b => b.textContent === path.replace(/^~(?=\/|$)/, '/home/ada') || b.textContent === path)).toBe(true))
}
/** The check ran by itself, and is done. */
export async function checked() {
  await act(async () => { await new Promise(r => setTimeout(r, 400)) })
  await waitFor(() => expect(screen.getByTestId('folder-check')).toHaveAttribute('aria-busy', 'false'))
}

/** Opens a picker and chooses an option. Its list draws only the rows that
 *  fit its height, which jsdom measures as zero, so layout is given a size
 *  while it is open; `absent` are options that must not be offered. */
export async function pickOption(name: string, label: string, absent: string[] = []) {
  const rect = Element.prototype.getBoundingClientRect
  const sizes = ['offsetHeight', 'clientHeight', 'offsetWidth', 'clientWidth'] as const
  for (const k of sizes) Object.defineProperty(HTMLElement.prototype, k, { configurable: true, get: () => 400 })
  Element.prototype.getBoundingClientRect = () => ({ x: 0, y: 0, top: 0, left: 0, width: 400, height: 400, right: 400, bottom: 400, toJSON() {} }) as DOMRect
  try {
    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name }))
    const option = await screen.findByRole('option', { name: new RegExp(`^${label}`) })
    for (const a of absent) expect(screen.queryByRole('option', { name: new RegExp(`^${a}`) })).toBeNull()
    await u.click(option)
  } finally {
    Element.prototype.getBoundingClientRect = rect
    for (const k of sizes) delete (HTMLElement.prototype as unknown as Record<string, unknown>)[k]
  }
}
