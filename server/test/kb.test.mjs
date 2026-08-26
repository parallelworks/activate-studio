// Path safety: every file route funnels through resolveKb, so an escape
// here is an arbitrary read everywhere.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
const { resolveKb } = await import('../dist/kb.js')

test('a normal relative path resolves under the root', () => {
  assert.ok(resolveKb('docs/a.md').startsWith(kb))
})

test('traversal out of the corpus is refused', () => {
  for (const attempt of ['../etc/passwd', 'docs/../../etc/passwd', '..']) {
    assert.throws(() => resolveKb(attempt), undefined, `should refuse: ${attempt}`)
  }
})

test('an absolute path is treated as root-relative, not honored', () => {
  // Leading slashes are stripped, so /etc/passwd cannot leave the corpus;
  // it names <root>/etc/passwd instead.
  assert.equal(resolveKb('/etc/passwd'), path.join(kb, 'etc', 'passwd'))
})
