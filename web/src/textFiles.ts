/** Making and editing plain text files in the Library. */

/** The largest text file the Library edits (kb.MaxEditBytes). */
export const TEXT_MAX = 1024 * 1024
export const TEXT_TOO_LARGE = 'This file is larger than 1 MB, the most that can be edited here.'
/** The base of a save that makes a new file (kb.NoBase). */
export const NO_BASE = 'none'

/** A save that was not made: the file changed (`version` is it now), is not
 *  there any more, or a new file's name is taken. */
export class TextConflict extends Error {
  constructor(readonly state: 'changed' | 'missing' | 'exists', message: string, readonly version?: string) { super(message) }
}

/** What a text field does not keep of a file: its line endings and its byte
 *  order mark, put back on a save. */
export interface TextForm { bom: boolean; crlf: boolean }

/** A file's text as a text field holds it, or null when its lines end in
 *  more than one way, which a save could not give back as it was. */
export function toEdit(content: string): { text: string; form: TextForm } | null {
  const bom = content.startsWith('﻿')
  const body = bom ? content.slice(1) : content
  const crlf = body.includes('\r\n')
  const text = crlf ? body.replace(/\r\n/g, '\n') : body
  if (text.includes('\r') || (crlf && /(^|[^\r])\n/.test(body))) return null
  return { text, form: { bom, crlf } }
}

export function toFile(text: string, form: TextForm): string {
  return (form.bom ? '﻿' : '') + (form.crlf ? text.replace(/\n/g, '\r\n') : text)
}

export function textBytes(s: string): number {
  // A UTF-16 unit is at most 3 bytes, so most text needs no counting.
  return s.length * 3 <= TEXT_MAX ? s.length : new TextEncoder().encode(s).length
}

/** The n-th name for a copy of a file, beside it: "plan (copy).md", then
 *  "plan (copy 2).md". */
export function copyName(path: string, n: number): string {
  const at = path.lastIndexOf('/') + 1
  const name = path.slice(at)
  const dot = name.lastIndexOf('.')
  const stem = dot > 0 ? name.slice(0, dot) : name
  return `${path.slice(0, at)}${stem} (copy${n > 1 ? ` ${n}` : ''})${dot > 0 ? name.slice(dot) : ''}`
}
