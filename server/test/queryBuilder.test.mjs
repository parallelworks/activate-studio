// The Query page's builder: conditions joined by AND or OR, the operators
// each field takes, sizes with units, dates worked out by the server (GUFI's
// own strftime takes seconds, so the SQL cannot parse a date), paths
// relative to the library, owners, and the presets' global top N. The SQL
// tests need nothing installed; the run tests need GUFI (GUFI_BIN, else
// /opt/gufi/bin) and skip without it. They index two small trees, the
// Studio's own library and one added beside it, and run each query on one
// and across both.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(gufiBin, 'gufi_query')) && fs.existsSync(path.join(gufiBin, 'gufi_dir2index'))

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'qb-'))
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))

const day = s => Date.parse(`${s}T00:00:00Z`) / 1000
const write = (f, bytes, mtime) => {
  fs.mkdirSync(path.dirname(f), { recursive: true })
  fs.writeFileSync(f, Buffer.alloc(bytes, 97))
  if (mtime) fs.utimesSync(f, mtime, mtime)
}
// Sizes and dates chosen so the overall order interleaves the directories
// and the two libraries.
const now = Math.floor(Date.now() / 1000)
write(path.join(dir, 'kb', 'reports', 'q1.pdf'), 1000, day('2019-06-01'))
write(path.join(dir, 'kb', 'reports', 'q2.pdf'), 2000, now - 3600)
write(path.join(dir, 'kb', 'reports', 'deep', 'big.pdf'), 50_000, now - 2 * 86400)
write(path.join(dir, 'kb', 'notes', 'old.txt'), 20, day('2020-01-01') + 3600)
write(path.join(dir, 'other', 'data', 'huge.bin'), 70_000, now - 20 * 86400)
write(path.join(dir, 'other', 'data', 'tiny.txt'), 10, day('2018-03-03'))
fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
fs.mkdirSync(path.join(dir, 'other-idx'), { recursive: true })
if (haveGufi) {
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'other'), path.join(dir, 'other-idx')], { stdio: 'ignore' })
}
fs.writeFileSync(path.join(dir, 'ix', 'libraries.json'), JSON.stringify([{ id: 'other', label: 'other', indexRoot: path.join(dir, 'other-idx', 'other') }]))

const Q = await import('../dist/query.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(Q.queryRoutes)
await app.ready()
test.after(() => app.close())

const B = (filters, extra = {}) => ({ builder: { source: 'files', filters, groupBy: 'none', sort: 'size', dir: 'desc', limit: 50, subtree: '', ...extra } })
const post = async (url, body) => {
  const res = await app.inject({ method: 'POST', url, payload: body })
  return { status: res.statusCode, body: res.json() }
}
const sqlE = async body => {
  const r = await post('/api/query/sql', body)
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return r.body.stages.find(s => s.flag === '-E').sql
}
const paths = r => r.body.rows.map(row => row[row.length - 3])

test('sizes take bytes or a unit, in powers of 1024', () => {
  assert.equal(Q.parseSize('2000'), 2000)
  assert.equal(Q.parseSize('10 MB'), 10 * 1024 ** 2)
  assert.equal(Q.parseSize('1.5g'), 1.5 * 1024 ** 3)
  assert.equal(Q.parseSize('500k'), 500 * 1024)
  assert.throws(() => Q.parseSize('lots'), /size takes a number of bytes or a size such as 10 MB/)
})

test('a modified value is a day, a minute, a second, or a moment some time ago', () => {
  assert.deepEqual(Q.parseWhen('2026-01-01'), { start: day('2026-01-01'), end: day('2026-01-02') })
  assert.deepEqual(Q.parseWhen('2026-01-01 09:30'), { start: day('2026-01-01') + 9.5 * 3600, end: day('2026-01-01') + 9.5 * 3600 + 60 })
  assert.deepEqual(Q.parseWhen('2026-01-01T09:30:15'), { start: day('2026-01-01') + 9.5 * 3600 + 15, end: day('2026-01-01') + 9.5 * 3600 + 16 })
  assert.deepEqual(Q.parseWhen('7 days', 1_000_000), { start: 1_000_000 - 7 * 86400, end: 1_000_000 - 7 * 86400 })
  assert.deepEqual(Q.parseWhen('2 weeks ago', 2_000_000), { start: 2_000_000 - 14 * 86400, end: 2_000_000 - 14 * 86400 })
  assert.deepEqual(Q.parseWhen('12 hours', 100_000), { start: 100_000 - 12 * 3600, end: 100_000 - 12 * 3600 })
  for (const bad of ['2021-02-30', 'yesterday', '2026/01/01', '']) assert.throws(() => Q.parseWhen(bad), /modified takes a date such as 2026-01-01/, bad)
})

test('the SQL: dates and sizes as numbers, never strftime; conditions in parentheses, joined by AND or OR', async () => {
  const e = await sqlE(B([{ field: 'modified', op: 'eq', value: '2020-01-01' }, { field: 'size', op: 'gt', value: '1 KB' }]))
  assert.ok(e.includes(`WHERE ((mtime >= ${day('2020-01-01')} AND mtime < ${day('2020-01-02')})) AND (size > 1024)`), e)
  assert.ok(!e.includes('strftime'), e)
  const any = await sqlE(B([{ field: 'name', op: 'eq', value: 'a.pdf' }, { field: 'size', op: 'lt', value: '10' }], { match: 'any' }))
  assert.ok(any.includes("WHERE (vrpentries.name = 'a.pdf') OR (size < 10)"), any)
  assert.ok((await sqlE(B([{ field: 'modified', op: 'gt', value: '2021-01-01' }]))).includes(`(mtime >= ${day('2021-01-02')})`))
  assert.ok((await sqlE(B([{ field: 'modified', op: 'lt', value: '2021-01-01' }]))).includes(`(mtime < ${day('2021-01-01')})`))
})

test('text is a subquery on the words table, so it can stand in an OR; a quote in a value stays in the string', async () => {
  const e = await sqlE(B([{ field: 'text', op: 'match', value: 'quarterly report' }, { field: 'name', op: 'contains', value: 'q' }], { match: 'any' }))
  assert.ok(e.includes(`(vrpentries.inode IN (SELECT CAST(tinode AS TEXT) FROM words WHERE wordf MATCH '"quarterly" AND "report"')) OR (vrpentries.name LIKE '%q%')`), e)
  assert.ok(!/JOIN words/.test(e), e)
  const q = await sqlE(B([{ field: 'name', op: 'eq', value: "o'brien'; DROP TABLE t; --" }]))
  assert.ok(q.includes("vrpentries.name = 'o''brien''; DROP TABLE t; --'"), q)
})

test('each field takes its own conditions, and says which when given another', async () => {
  for (const [field, op, msg] of [
    ['size', 'contains', /size takes equals, greater than or less than; "contains" does not apply/],
    ['modified', 'not_contains', /modified takes equals, greater than or less than/],
    ['text', 'eq', /text takes matches/],
    ['path', 'gt', /path takes contains, does not contain or equals/],
    ['label', 'gt', /label takes equals or contains/],
  ]) {
    const r = await post('/api/query/sql', B([{ field, op, value: '1' }]))
    assert.equal(r.status, 400, field)
    assert.match(r.body.message, msg)
  }
  const dirs = await post('/api/query/sql', B([{ field: 'path', op: 'contains', value: 'x' }], { source: 'directories' }))
  assert.equal(dirs.status, 400)
  assert.match(dirs.body.message, /directories take name, owner, size and modified conditions/)
})

test('directories: conditions on their own columns, so a value that looks like a column name stays a value', async () => {
  const e = await sqlE(B([{ field: 'name', op: 'contains', value: 'size' }, { field: 'size', op: 'gt', value: '1 MB' }], { source: 'directories', sort: 'bytes' }))
  assert.ok(e.includes("WHERE (vrsummary.name LIKE '%size%') AND (totsize > 1048576)"), e)
})

test('the starter queries are builder forms, and none repeats a preset', async () => {
  const saved = (await app.inject({ method: 'GET', url: '/api/query/saved' })).json()
  assert.deepEqual(saved.map(s => s.id), ['starter-large', 'starter-pdfs'])
  for (const s of saved) assert.ok(s.payload.builder, s.name)
})

for (const library of [undefined, 'all']) {
  const scope = library ? { library } : {}
  const on = library ? 'across both libraries' : 'on one library'

  test(`run ${on}: equals on a path and on a day, which matched nothing before`, { skip: !haveGufi }, async () => {
    let r = await post('/api/query', { ...B([{ field: 'path', op: 'eq', value: 'reports/q2.pdf' }]), ...scope })
    assert.deepEqual(paths(r), ['reports/q2.pdf'])
    r = await post('/api/query', { ...B([{ field: 'modified', op: 'eq', value: '2020-01-01' }]), ...scope })
    assert.deepEqual(paths(r), ['notes/old.txt'])
    // The index's own folder is not part of a path: nothing contains its name.
    r = await post('/api/query', { ...B([{ field: 'path', op: 'contains', value: path.basename(dir) }]), ...scope })
    assert.deepEqual(r.body.rows, [])
    r = await post('/api/query', { ...B([{ field: 'path', op: 'contains', value: 'reports/deep' }]), ...scope })
    assert.deepEqual(paths(r), ['reports/deep/big.pdf'])
  })

  test(`run ${on}: before, after, and a time ago`, { skip: !haveGufi }, async () => {
    let r = await post('/api/query', { ...B([{ field: 'modified', op: 'lt', value: '2021-01-01' }], { sort: 'modified', dir: 'asc' }), ...scope })
    assert.deepEqual(paths(r), library ? ['data/tiny.txt', 'reports/q1.pdf', 'notes/old.txt'] : ['reports/q1.pdf', 'notes/old.txt'])
    r = await post('/api/query', { ...B([{ field: 'modified', op: 'gt', value: '7 days' }]), ...scope })
    assert.deepEqual(paths(r).sort(), ['reports/deep/big.pdf', 'reports/q2.pdf'])
    r = await post('/api/query', { ...B([{ field: 'modified', op: 'gt', value: '2021-01-01' }, { field: 'size', op: 'gt', value: '10 KB' }]), ...scope })
    assert.deepEqual(paths(r), library ? ['data/huge.bin', 'reports/deep/big.pdf'] : ['reports/deep/big.pdf'])
  })

  test(`run ${on}: any of several conditions`, { skip: !haveGufi }, async () => {
    const r = await post('/api/query', { ...B([{ field: 'name', op: 'eq', value: 'old.txt' }, { field: 'size', op: 'eq', value: '2000' }], { match: 'any' }), ...scope })
    assert.deepEqual(paths(r).sort(), ['notes/old.txt', 'reports/q2.pdf'])
  })

  test(`run ${on}: the presets are the overall top N, not each directory's`, { skip: !haveGufi }, async () => {
    let r = await post('/api/query', { ...B([], { sort: 'size', dir: 'desc', limit: 3 }), ...scope })
    assert.deepEqual(paths(r), library ? ['data/huge.bin', 'reports/deep/big.pdf', 'reports/q2.pdf'] : ['reports/deep/big.pdf', 'reports/q2.pdf', 'reports/q1.pdf'])
    r = await post('/api/query', { ...B([], { sort: 'modified', dir: 'asc', limit: 2 }), ...scope })
    assert.deepEqual(paths(r), library ? ['data/tiny.txt', 'reports/q1.pdf'] : ['reports/q1.pdf', 'notes/old.txt'])
    r = await post('/api/query', { ...B([], { source: 'directories', sort: 'bytes', dir: 'desc', limit: 2 }), ...scope })
    assert.deepEqual(r.body.rows.map(row => row[row.length - 3]), library ? ['data', 'reports/deep'] : ['reports/deep', 'reports'])
    r = await post('/api/query', { ...B([], { sort: 'name', dir: 'asc', limit: 2 }), ...scope })
    assert.deepEqual(paths(r), library ? ['data/huge.bin', 'data/tiny.txt'] : ['notes/old.txt', 'reports/deep/big.pdf'])
  })

  test(`run ${on}: totals by owner`, { skip: !haveGufi }, async () => {
    const r = await post('/api/query', { ...B([], { groupBy: 'owner', sort: 'bytes' }), ...scope })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    assert.deepEqual(r.body.columns.slice(-3), ['owner', 'files', 'bytes'])
    const row = r.body.rows[0]
    assert.equal(row[row.length - 3], os.userInfo().username)
    assert.equal(Number(row[row.length - 2]), library ? 6 : 4)
    const owner = await post('/api/query', { ...B([{ field: 'owner', op: 'eq', value: os.userInfo().username }]), ...scope })
    assert.equal(owner.body.rows.length, library ? 6 : 4)
  })
}

test('the canned query for recent changes counts days back from now', { skip: !haveGufi }, async () => {
  const r = await post('/api/query', { canned: 'recent', n: 50, days: 7 })
  assert.deepEqual(paths(r).sort(), ['reports/deep/big.pdf', 'reports/q2.pdf'])
})

test('run on the added library: a label the Studio keeps for it holds under match any and all', { skip: !haveGufi }, async () => {
  const LL = await import('../dist/libraryLabels.js')
  const { getLibrary } = await import('../dist/libraries.js')
  const other = getLibrary('other')
  assert.equal(other.writable, false)
  LL.setLibraryLabels(other, 'data/tiny.txt', ['keep'])
  try {
    const scope = { library: 'other' }
    const filters = [{ field: 'label', op: 'eq', value: 'keep' }, { field: 'size', op: 'gt', value: '50 KB' }]
    let r = await post('/api/query', { ...B(filters, { match: 'any' }), ...scope })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    assert.deepEqual(paths(r).sort(), ['data/huge.bin', 'data/tiny.txt'])
    r = await post('/api/query', { ...B(filters), ...scope })
    assert.deepEqual(paths(r), [])
    r = await post('/api/query', { ...B([filters[0]]), ...scope })
    assert.deepEqual(paths(r), ['data/tiny.txt'])
  } finally {
    LL.setLibraryLabels(other, 'data/tiny.txt', [])
  }
})
