// A raw SELECT's result columns are named as SQLite names them: the alias
// after AS, else the column's own name, else the expression as written.
// The cases are shared with the Go server's internal/gufi columns_test.go.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'

const Q = await import('../dist/query.js')
const cases = JSON.parse(fs.readFileSync(new URL('../../server-go/internal/gufi/testdata/select_columns.json', import.meta.url), 'utf8'))

test('names a raw SELECT\'s columns by their aliases', () => {
  for (const c of cases) assert.deepEqual(Q.selectColumnNames(c.sql), c.names, c.sql)
})

test('falls back to col1..colN when the names do not fit the rows', () => {
  const sql = 'SELECT name AS file, size AS bytes FROM vrpentries;'
  assert.deepEqual(Q.rawColumns(sql, 2, true), ['file', 'bytes'])
  assert.deepEqual(Q.rawColumns(sql, 1, false), ['file', 'bytes'])
  assert.deepEqual(Q.rawColumns(sql, 3, true), ['col1', 'col2', 'col3'])
  assert.deepEqual(Q.rawColumns('SELECT * FROM vrpentries', 1, false), ['col1'])
})
