import { type ReactNode, useEffect, useState } from 'react'
import { Loader } from '@parallelworks/ui'
import { api, type WorkbookSheet } from '../api'
import { SheetGrid } from './SheetGrid'

/** Workbooks the grid reads (server: kb.GridSuffixes). */
export const GRID_SUFFIXES = ['.xlsx', '.xlsm', '.xltx', '.xltm']

// The sheets read this visit, by file version and sheet, so going back to
// a sheet, or to the file, draws it at once.
const kept = new Map<string, WorkbookSheet>()
const keep = (k: string, s: WorkbookSheet) => {
  kept.delete(k)
  kept.set(k, s)
  while (kept.size > 12) kept.delete(kept.keys().next().value as string)
}

/**
 * A workbook as a spreadsheet: its sheets as tabs under the grid, in the
 * workbook's order and named as it names them, the one it opens on shown
 * first. Read from the file by the server; `onFail` says it could not be.
 */
export function SheetWorkbook({ path, version, tools, onFail }: {
  path: string
  /** The file's version (its modification time): a new one reads again. */
  version: number
  tools?: ReactNode
  onFail: (why: string) => void
}) {
  const [index, setIndex] = useState<number | undefined>(undefined)
  const key = `${path}@${version}@${index ?? 'first'}`
  const [sheet, setSheet] = useState<WorkbookSheet | null>(() => kept.get(key) ?? null)
  const [error, setError] = useState('')
  useEffect(() => {
    const had = kept.get(key)
    if (had) { setSheet(had); return }
    let gone = false
    setError('')
    api.sheet(path, index)
      .then(s => {
        if (gone) return
        keep(key, s)
        keep(`${path}@${version}@${s.index}`, s)
        setSheet(s)
      })
      .catch(e => {
        if (gone) return
        const why = String((e as Error).message ?? e)
        if (sheet) setError(why)
        else onFail(why)
      })
    return () => { gone = true }
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!sheet) return <div className="muted pad"><Loader inline size={16} label="Reading the workbook…" /></div>
  const hidden = sheet.sheets.filter(s => s.hidden).length
  const drawings = [
    sheet.charts ? `${sheet.charts} ${sheet.charts === 1 ? 'chart' : 'charts'}` : '',
    sheet.pictures ? `${sheet.pictures} ${sheet.pictures === 1 ? 'picture' : 'pictures'}` : '',
  ].filter(Boolean).join(' and ')
  const tabs = (
    <div className="sg-tabs" role="tablist" aria-label="Sheets">
      {sheet.sheets.map((s, i) => !s.hidden && (
        <button key={i} type="button" role="tab" aria-selected={i === sheet.index} className={'sg-tab' + (i === sheet.index ? ' on' : '')}
          onClick={() => { if (i !== sheet.index) setIndex(i) }}>{s.name}</button>
      ))}
    </div>
  )
  return (
    <SheetGrid
      data={sheet}
      label={`Sheet ${sheet.name}`}
      tabs={tabs}
      tools={tools}
      note={<>
        {error && <span role="alert">That sheet could not be read: {error} </span>}
        {drawings && `This sheet has ${drawings}, which Print view shows. `}
        {hidden > 0 && `${hidden} hidden ${hidden === 1 ? 'sheet is' : 'sheets are'} not shown.`}
      </>}
    />
  )
}
