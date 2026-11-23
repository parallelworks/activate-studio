import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { colName, type GridSheetData, SheetGrid } from '../src/components/SheetGrid'
import { SheetWorkbook } from '../src/components/SheetWorkbook'

const budget: GridSheetData = {
  name: 'Summary', rows: 5, cols: 4, totalRows: 5, totalCols: 4,
  colWidths: [120, 80, 90, 70], frozenRows: 2,
  merges: [[0, 0, 0, 3]],
  styles: [{ b: true, fill: '#305496', color: '#ffffff', h: 'center' }],
  cells: [
    { r: 0, c: [[0, 'Example budget', 0]] },
    { r: 1, c: [[0, 'Item', 0], [1, 'Start', 0], [2, 'Budget', 0], [3, 'Used', 0]] },
    { r: 2, c: [[0, 'Compute'], [1, 'Jan 3, 2026', -1, '', '', 'd'], [2, '$1,200.00', -1, '', '1200', 'n'], [3, '53.4%', -1, '=C3/C5', '0.534', 'n']] },
    { r: 3, c: [[0, 'A note long enough to run well past the width of its column, so it ends in an ellipsis'], [2, '$800.00', -1, '', '800', 'n']] },
    { r: 4, c: [[0, 'Total', 0], [2, '$2,000.00', -1, '=SUM(C3:C4)', '2000', 'n']] },
  ],
}
const cell = (text: string) => {
  const hit = [...document.querySelectorAll<HTMLElement>('.sg-cell')].filter(e => e.textContent === text)
  if (hit.length !== 1) throw new Error(`${hit.length} cells read ${text}`)
  return hit[0]
}

describe('the spreadsheet grid', () => {
  it('names columns as a spreadsheet does', () => {
    expect([0, 25, 26, 27, 701, 702].map(colName)).toEqual(['A', 'Z', 'AA', 'AB', 'ZZ', 'AAA'])
  })

  it('draws column letters, row numbers, the file\'s widths, formats and styles, and a merged cell across its columns', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    for (const l of ['A', 'B', 'C', 'D']) expect(screen.getByText(l)).toBeTruthy()
    expect(screen.getByText('$1,200.00')).toBeTruthy()
    expect(cell('$1,200.00')).toHaveStyle({ textAlign: 'right', width: '90px' })
    expect(cell('Jan 3, 2026')).toHaveStyle({ textAlign: 'right' })
    expect(cell('Item')).toHaveStyle({ fontWeight: '600', textAlign: 'center' })
    // The title spans the four columns it is merged across.
    expect(cell('Example budget')).toHaveStyle({ width: `${120 + 80 + 90 + 70}px` })
    // Long text is cut with an ellipsis and says all of itself on hover.
    expect(cell('A note long enough to run well past the width of its column, so it ends in an ellipsis').title).toMatch(/ends in an ellipsis/)
  })

  it('shows a selected cell\'s value and formula, and a range\'s count and sum', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    fireEvent.mouseDown(cell('53.4%'), { button: 0 })
    expect(screen.getByLabelText('Selected cells').textContent).toBe('D3')
    expect(screen.getByTitle('Formula').textContent).toBe('=C3/C5')
    expect(screen.getByTitle('The value as stored').textContent).toBe('0.534')
    // A long value is shown whole in the bar.
    fireEvent.mouseDown(cell('A note long enough to run well past the width of its column, so it ends in an ellipsis'), { button: 0 })
    expect(document.querySelector('.sg-value')?.textContent).toMatch(/so it ends in an ellipsis$/)
    fireEvent.mouseDown(cell('$1,200.00'), { button: 0 })
    fireEvent.mouseDown(cell('$2,000.00'), { button: 0, shiftKey: true })
    expect(screen.getByLabelText('Selected cells').textContent).toBe('C3:C5')
    expect(screen.getByText(/3 values, sum 4,000, average 1,333/)).toBeTruthy()
  })

  it('copies a range as tab separated text', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    fireEvent.mouseDown(cell('Compute'), { button: 0 })
    fireEvent.mouseDown(cell('$800.00'), { button: 0, shiftKey: true })
    const data = new Map<string, string>()
    const ev = new Event('copy', { bubbles: true, cancelable: true }) as Event & { clipboardData: { setData: (t: string, v: string) => void } }
    ev.clipboardData = { setData: (t, v) => { data.set(t, v) } }
    act(() => { document.querySelector('.sg-scroll')!.dispatchEvent(ev) })
    expect(data.get('text/plain')).toBe('Compute\tJan 3, 2026\t$1,200.00\nA note long enough to run well past the width of its column, so it ends in an ellipsis\t\t$800.00')
  })

  it('moves the selection with the keyboard, past the frozen rows', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    fireEvent.mouseDown(cell('Compute'), { button: 0 })
    fireEvent.keyDown(document.querySelector('.sg-scroll')!, { key: 'ArrowDown' })
    fireEvent.keyDown(document.querySelector('.sg-scroll')!, { key: 'ArrowRight', shiftKey: true })
    expect(screen.getByLabelText('Selected cells').textContent).toBe('A4:B4')
  })

  it('widens the selection on a drag, but not when the grid moves under a still pointer', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    fireEvent.mouseDown(cell('Compute'), { button: 0, clientX: 100, clientY: 100 })
    fireEvent.mouseOver(cell('Example budget'), { clientX: 100, clientY: 100 })
    expect(screen.getByLabelText('Selected cells').textContent).toBe('A3')
    fireEvent.mouseOver(cell('Total'), { clientX: 100, clientY: 144 })
    fireEvent.mouseUp(window)
    expect(screen.getByLabelText('Selected cells').textContent).toBe('A3:A5')
  })

  it('filters the rows under the frozen ones, keeps the frozen merge, and moves a hidden selection to a row shown', () => {
    render(<SheetGrid data={budget} label="Sheet" />)
    fireEvent.mouseDown(cell('Compute'), { button: 0 })
    fireEvent.change(screen.getByLabelText('Filter rows'), { target: { value: 'total' } })
    expect(cell('Total')).toBeTruthy()
    expect(document.querySelectorAll('.sg-cell[data-r="2"]').length).toBe(0)
    expect(cell('Example budget')).toHaveStyle({ width: `${120 + 80 + 90 + 70}px` })
    expect(screen.getByLabelText('Selected cells').textContent).toBe('A5')
    expect(screen.getByText(/1 of 3 rows/)).toBeTruthy()
  })

  it('draws only the rows in view of a long sheet, and says when the sheet was cut', () => {
    const rows = 20000
    const long: GridSheetData = {
      name: 'Log', rows, cols: 2, totalRows: 30001, totalCols: 2, colWidths: [60, 60],
      cells: Array.from({ length: rows }, (_, r) => ({ r, c: [[0, `row ${r + 1}`], [1, String(r), -1, '', '', 'n']] })),
    }
    render(<SheetGrid data={long} label="Sheet" />)
    expect(document.querySelectorAll('.sg-cell').length).toBeLessThan(200)
    expect(screen.getByText(/The first 20,000 of 30,001 rows are shown; use Download/)).toBeTruthy()
    // Filter reaches every row, drawn or not.
    fireEvent.change(screen.getByLabelText('Filter rows'), { target: { value: 'row 19999' } })
    expect(cell('row 19999')).toBeTruthy()
  })
})

describe('a workbook in the grid', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('has a tab per sheet in the workbook\'s order, hides hidden sheets, and reads a sheet when its tab is chosen', async () => {
    const asked: string[] = []
    const sheets = [{ name: 'Summary' }, { name: 'Quarterly' }, { name: 'Lookup', hidden: true }]
    vi.stubGlobal('fetch', vi.fn(async (u: string) => {
      asked.push(String(u))
      const i = Number(new URL(String(u), 'http://x').searchParams.get('sheet') ?? 0)
      const body = { ...budget, sheets, index: i, name: sheets[i].name, charts: i === 0 ? 1 : 0, cells: [{ r: 2, c: [[0, `on ${sheets[i].name}`]] }] }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<SheetWorkbook path="books/budget.xlsx" version={1} onFail={() => {}} />)
    expect(await screen.findByText('on Summary')).toBeTruthy()
    expect(screen.getAllByRole('tab').map(t => t.textContent)).toEqual(['Summary', 'Quarterly'])
    expect(screen.getByText(/This sheet has 1 chart, which Print view shows/)).toBeTruthy()
    expect(screen.getByText(/1 hidden sheet is not shown/)).toBeTruthy()
    fireEvent.click(screen.getByRole('tab', { name: 'Quarterly' }))
    expect(await screen.findByText('on Quarterly')).toBeTruthy()
    expect(asked.map(u => u.replace(/^.*\/api/, '/api'))).toEqual(['/api/kb/sheet?path=books%2Fbudget.xlsx', '/api/kb/sheet?path=books%2Fbudget.xlsx&sheet=1'])
    // Back to the first sheet: kept, not read again.
    fireEvent.click(screen.getByRole('tab', { name: 'Summary' }))
    await waitFor(() => expect(screen.getByText('on Summary')).toBeTruthy())
    expect(asked).toHaveLength(2)
  })

  it('says it could not read a workbook', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'This file could not be read as a workbook' }), { status: 422, headers: { 'Content-Type': 'application/json' } })))
    const fail = vi.fn()
    render(<SheetWorkbook path="books/broken.xlsx" version={2} onFail={fail} />)
    await waitFor(() => expect(fail).toHaveBeenCalled())
    expect(String(fail.mock.calls[0][0])).toMatch(/could not be read/)
  })
})
