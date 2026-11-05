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
const { listDir, resolveKb } = await import('../dist/kb.js')

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

test('hidden entries list only when asked for, and tool folders never do', async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-hidden-'))
  fs.writeFileSync(path.join(root, 'a.md'), 'a')
  fs.writeFileSync(path.join(root, '.env'), 'x')
  for (const d of ['.agents', '.config', '.git']) fs.mkdirSync(path.join(root, d))
  const names = async show => (await listDir('', root, show)).map(e => e.name).sort()
  assert.deepEqual(await names(false), ['.agents', 'a.md'])
  assert.deepEqual(await names(true), ['.agents', '.config', '.env', 'a.md'])
})
