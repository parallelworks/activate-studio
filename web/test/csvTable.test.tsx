import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CsvTable, detectDelimiter, parseDelimited } from '../src/components/CsvTable'

describe('delimited files', () => {
  it('reads quoted fields, doubled quotes and line breaks inside a field', () => {
    const { rows } = parseDelimited('name,note\r\n"Smith, J","said ""ok""\nthen left"\n\nplain,x\n', ',')
    expect(rows).toEqual([['name', 'note'], ['Smith, J', 'said "ok"\nthen left'], ['plain', 'x']])
  })
  it('drops the row a truncated preview cut in half', () => {
    expect(parseDelimited('a,b\n1,2\n3,"unfinished', ',', true)).toEqual({ rows: [['a', 'b'], ['1', '2']], partial: true })
  })
  it('finds the delimiter', () => {
    expect(detectDelimiter('a;b;c\n1;2;3\n', '.csv')).toBe(';')
    expect(detectDelimiter('a,b\n1,2\n', '.csv')).toBe(',')
    expect(detectDelimiter('a,b\tc\n', '.tsv')).toBe('\t')
  })
  it('shows the file in the grid, with its header frozen, and sorts a numeric column as numbers and filters rows', () => {
    render(<CsvTable text={'item,count\nbolt,10\nnut,9\nwasher,100\n'} suffix=".csv" />)
    const col = (c: number) => [...document.querySelectorAll(`.sg-layer .sg-cell[data-c="${c}"]`)]
      .filter(e => Number((e as HTMLElement).dataset.r) > 0)
      .sort((a, b) => parseFloat((a as HTMLElement).style.top) - parseFloat((b as HTMLElement).style.top))
      .map(e => e.textContent)
    expect(col(0)).toEqual(['bolt', 'nut', 'washer'])
    expect(screen.getByText('count').closest('.sg-cell')).toHaveStyle({ fontWeight: '600' })
    // Sort by the selected column: count, numerically.
    fireEvent.mouseDown(screen.getByText('10').closest('.sg-cell')!, { button: 0 })
    fireEvent.click(screen.getByRole('button', { name: 'Sort' }))
    expect(col(0)).toEqual(['nut', 'bolt', 'washer'])
    fireEvent.change(screen.getByLabelText('Filter rows'), { target: { value: 'wash' } })
    expect(col(0)).toEqual(['washer'])
    expect(screen.getByText(/1 of 3 rows, sorted/)).toBeTruthy()
    expect(screen.getByText(/3 rows, 2 columns, comma separated/)).toBeTruthy()
  })
})
