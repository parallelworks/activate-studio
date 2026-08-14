import fs from 'node:fs/promises'
import path from 'node:path'
import {
  KB_ROOT, EXTRACT_CACHE, EXCLUDE_DIRS, EXCLUDE_FILE_SUFFIXES,
  TEXT_SUFFIXES, EXTRACTED_SUFFIXES, IMAGE_SUFFIXES, MODEL_SUFFIXES, MAX_PREVIEW_BYTES,
} from './config.js'

export interface KbEntry {
  name: string
  path: string // relative to KB_ROOT, '' for root
  type: 'dir' | 'file'
  size: number
  mtime: number
  kind: 'text' | 'extracted' | 'image' | 'model' | 'binary' | 'dir'
}

export class KbError extends Error {
  constructor(public status: number, message: string) { super(message) }
}

/** Resolve a client-supplied relative path safely inside KB_ROOT. */
export function resolveKb(rel: string): string {
  const cleaned = rel.replace(/^\/+/, '')
  const abs = path.resolve(KB_ROOT, cleaned)
  if (abs !== KB_ROOT && !abs.startsWith(KB_ROOT + path.sep)) {
    throw new KbError(400, 'path escapes the knowledge base root')
  }
  return abs
}

export function classify(name: string, isDir: boolean): KbEntry['kind'] {
  if (isDir) return 'dir'
  const ext = path.extname(name).toLowerCase()
  if (TEXT_SUFFIXES.has(ext)) return 'text'
  if (EXTRACTED_SUFFIXES.has(ext)) return 'extracted'
  if (IMAGE_SUFFIXES.has(ext)) return 'image'
  if (MODEL_SUFFIXES.has(ext)) return 'model'
  return 'binary'
}

export function excluded(name: string, isDir: boolean): boolean {
  if (isDir) return EXCLUDE_DIRS.has(name)
  return EXCLUDE_FILE_SUFFIXES.some(s => name.toLowerCase().endsWith(s))
}

export async function listDir(rel: string): Promise<KbEntry[]> {
  const abs = resolveKb(rel)
  const dirents = await fs.readdir(abs, { withFileTypes: true })
  const out: KbEntry[] = []
  for (const d of dirents) {
    if (d.name.startsWith('.') && d.name !== '.') continue
    if (excluded(d.name, d.isDirectory())) continue
    const p = path.join(abs, d.name)
    let st
    try { st = await fs.stat(p) } catch { continue }
    out.push({
      name: d.name,
      path: path.relative(KB_ROOT, p),
      type: d.isDirectory() ? 'dir' : 'file',
      size: st.size,
      mtime: Math.floor(st.mtimeMs / 1000),
      kind: classify(d.name, d.isDirectory()),
    })
  }
  out.sort((a, b) => (a.type === b.type ? a.name.localeCompare(b.name) : a.type === 'dir' ? -1 : 1))
  return out
}

/** Extracted-text cache path for a KB-relative file path. */
export function extractPath(rel: string): string {
  return path.join(EXTRACT_CACHE, rel + '.txt')
}

export interface FileContent {
  path: string
  kind: KbEntry['kind']
  size: number
  mtime: number
  truncated: boolean
  content: string | null // null when binary/image (client uses download URL)
  source: 'raw' | 'extracted' | 'none'
}

export async function readFileContent(rel: string): Promise<FileContent> {
  const abs = resolveKb(rel)
  const st = await fs.stat(abs)
  if (st.isDirectory()) throw new KbError(400, 'path is a directory')
  const kind = classify(abs, false)
  const base = { path: rel, kind, size: st.size, mtime: Math.floor(st.mtimeMs / 1000) }
  if (kind === 'text') {
    const truncated = st.size > MAX_PREVIEW_BYTES
    const fh = await fs.open(abs, 'r')
    try {
      const len = Math.min(st.size, MAX_PREVIEW_BYTES)
      const buf = Buffer.alloc(len)
      await fh.read(buf, 0, len, 0)
      return { ...base, truncated, content: buf.toString('utf8'), source: 'raw' }
    } finally { await fh.close() }
  }
  if (kind === 'extracted' || kind === 'image') {
    // Extracted text for documents; OCR plus vision caption for images.
    try {
      const text = await fs.readFile(extractPath(rel), 'utf8')
      if (text.trim()) {
        const truncated = text.length > MAX_PREVIEW_BYTES
        return { ...base, truncated, content: text.slice(0, MAX_PREVIEW_BYTES), source: 'extracted' }
      }
    } catch { /* no cache entry */ }
    return { ...base, truncated: false, content: null, source: 'none' }
  }
  if (kind === 'binary') {
    // Unknown suffixes are often text anyway (logs, .bak copies, dotless
    // files); sniff the head and serve as text when the bytes read as text.
    const fh = await fs.open(abs, 'r')
    try {
      const len = Math.min(st.size, 65536)
      const head = Buffer.alloc(len)
      await fh.read(head, 0, len, 0)
      if (looksLikeText(head)) {
        const previewLen = Math.min(st.size, MAX_PREVIEW_BYTES)
        const buf = previewLen === len ? head : Buffer.alloc(previewLen)
        if (previewLen !== len) await fh.read(buf, 0, previewLen, 0)
        return { ...base, kind: 'text', truncated: st.size > MAX_PREVIEW_BYTES, content: buf.toString('utf8'), source: 'raw' }
      }
    } finally { await fh.close() }
  }
  return { ...base, truncated: false, content: null, source: 'none' }
}

/** True when a buffer looks like readable text: no NUL bytes and only a
 *  trace of control characters outside tab/newline/carriage return. */
function looksLikeText(buf: Buffer): boolean {
  if (buf.length === 0) return true
  let control = 0
  for (const b of buf) {
    if (b === 0) return false
    if (b < 32 && b !== 9 && b !== 10 && b !== 13) control++
  }
  return control / buf.length < 0.02
}
