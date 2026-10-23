// enrich.py's readers where the computer gets in the way: a pdftotext on
// the PATH that cannot run is no reader, so pypdf takes the PDF rather than
// the file failing; and Office lock files are never read as documents.
// Runs enrich.py's own functions in Python.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const indexer = path.join(root, 'indexer')
// By its full path, so a test can give Python a PATH of its own.
const python = execFileSync('sh', ['-c', `command -v ${process.env.PYTHON_BIN || 'python3'}`], { encoding: 'utf8' }).trim()
const py = (code, env = {}) => JSON.parse(execFileSync(python, ['-c', `import sys, json; sys.path.insert(0, ${JSON.stringify(indexer)}); import enrich; ${code}`],
  { encoding: 'utf8', env: { ...process.env, ...env } }))
const hasPypdf = (() => { try { execFileSync(python, ['-c', 'import pypdf']); return true } catch { return false } })()

test('Office and LibreOffice lock files are not documents', () => {
  const names = ['~$RevisedBOM.xlsx', '.~lock.report.odt#', 'report.docx', '~notes.txt', 'budget~$.xlsx']
  assert.deepEqual(py(`print(json.dumps([enrich.office_lock_file(n) for n in ${JSON.stringify(names)}]))`), [true, true, false, false, false])
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'locks-'))
  const lock = path.join(dir, '~$Budget.xlsx')
  fs.writeFileSync(lock, Buffer.from('owner name', 'utf8'))
  assert.equal(py(`from pathlib import Path; print(json.dumps(enrich.extract(Path(${JSON.stringify(lock)}))))`), null)
})

test('a pdftotext on the PATH that cannot run leaves the PDF to pypdf', { skip: !hasPypdf && 'pypdf is not installed' }, () => {
  const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'nopdftotext-'))
  fs.writeFileSync(path.join(bin, 'pdftotext'), '#!/bin/sh\necho never\n', { mode: 0o644 })
  const pdf = path.join(root, 'testdata', 'corpus', 'docs', 'report.pdf')
  const text = py(`from pathlib import Path; enrich.OPTIONS['ocr'] = False; print(json.dumps(enrich.extract_pdf(Path(${JSON.stringify(pdf)}))))`,
    // Only the copy that cannot run: as on a Mac with no poppler, where an
    // unreadable PATH entry made the old call fail with EACCES.
    { PATH: bin })
  assert.ok(text.trim().length > 20, `pypdf read the text: ${JSON.stringify(text.slice(0, 80))}`)
})
