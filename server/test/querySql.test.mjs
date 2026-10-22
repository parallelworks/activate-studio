// A query's SQL without running it (POST /api/query/sql), which the Query
// page shows for a saved query: the stages for each form a query takes, the
// command being the one a run records, and a library on a cluster's command
// naming its site. gufi_query is a stand-in that prints nothing.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createHash } from 'node:crypto'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'qsql-'))
const bin = path.join(dir, 'bin')
fs.mkdirSync(bin, { recursive: true })
fs.writeFileSync(path.join(bin, 'gufi_query'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
process.env.GUFI_BIN = bin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'
fs.mkdirSync(path.join(dir, 'kb'), { recursive: true })
const index = path.join(dir, 'ix', 'gufi', 'kb')
fs.mkdirSync(index, { recursive: true })

// The one user's library on a cluster.
const userDir = path.join(dir, 'ix', 'users', createHash('sha256').update('single').digest('hex').slice(0, 16))
fs.mkdirSync(userDir, { recursive: true })
fs.writeFileSync(path.join(userDir, 'owner'), 'single')
fs.writeFileSync(path.join(userDir, 'libraries.json'), JSON.stringify([{
  id: 'u-proj', label: 'proj', site: 'cluster-a', resource: 'pw://alex/cluster-a', sourceDir: '~/proj', sourceRoot: null,
  indexRoot: '/c/idx/proj', gufiBin: '/c/bin', state: 'ready', run: null, error: null, builtAt: null, directories: 1, files: 1, scheduler: null,
}]))

const { default: Fastify } = await import('fastify')
const { queryRoutes } = await import('../dist/query.js')
const app = Fastify()
await app.register(queryRoutes)
await app.ready()
test.after(async () => { await app.close(); fs.rmSync(dir, { recursive: true, force: true }) })

const sqlOf = async body => {
  const res = await app.inject({ method: 'POST', url: '/api/query/sql', payload: body })
  assert.equal(res.statusCode, 200, res.body)
  return res.json()
}
const builder = { source: 'files', filters: [{ field: 'extension', op: 'eq', value: '.PDF' }], groupBy: 'none', sort: 'size', dir: 'desc', limit: 100, subtree: 'reports' }

test('a builder query: its five stages, with the filter, order and limit it was saved with', async () => {
  const { stages, commands } = await sqlOf({ builder })
  assert.deepEqual(stages.map(s => s.flag), ['-I', '-K', '-E', '-J', '-G'])
  const sql = Object.fromEntries(stages.map(s => [s.flag, s.sql]))
  assert.equal(sql['-I'], 'CREATE TABLE t (path TEXT, size INT64, mtime INT64);')
  assert.match(sql['-E'], /^INSERT INTO t SELECT rpath\(sname, sroll\)\|\|'\/'\|\|vrpentries\.name, size, mtime FROM vrpentries WHERE lower\(.*\) = 'pdf' ORDER BY size DESC LIMIT 100;$/)
  assert.equal(sql['-G'], "SELECT path, size, datetime(mtime, 'unixepoch') FROM agg ORDER BY size DESC LIMIT 100;")
  assert.equal(commands.length, 1)
  assert.equal(commands[0].site, undefined)
  assert.ok(commands[0].command.startsWith(`${path.join(bin, 'gufi_query')} -n 8 -d $'\\x1e' -a 1 -I 'CREATE TABLE t`), commands[0].command)
  assert.ok(commands[0].command.endsWith(` ${path.join(index, 'reports')}`), commands[0].command)
})

test('a canned query and raw SQL', async () => {
  const canned = await sqlOf({ canned: 'big_directories', n: 7 })
  assert.equal(canned.stages.find(s => s.flag === '-G').sql, 'SELECT path, files, bytes FROM agg ORDER BY bytes DESC LIMIT 7;')
  const raw = await sqlOf({ sql: 'SELECT name, size FROM vrpentries WHERE size > 10;' })
  assert.deepEqual(raw.stages, [{ flag: '-E', sql: 'SELECT name, size FROM vrpentries WHERE size > 10;' }])
  assert.match(raw.commands[0].command, / -E 'SELECT name, size FROM vrpentries WHERE size > 10;' \S+$/)
  const refused = await app.inject({ method: 'POST', url: '/api/query/sql', payload: { sql: 'DELETE FROM vrpentries' } })
  assert.equal(refused.statusCode, 400)
})

test('the command shown is the command a run of the same query records', async () => {
  for (const body of [{ builder }, { canned: 'largest', n: 5 }, { sql: 'SELECT name FROM vrpentries;' }]) {
    const { commands } = await sqlOf(body)
    const ran = await app.inject({ method: 'POST', url: '/api/query', payload: body })
    assert.equal(ran.statusCode, 200, ran.body)
    assert.deepEqual(ran.json().gufi.map(c => c.command), commands.map(c => c.command))
  }
})

test('a library on a cluster: its own gufi_query and index, four threads, and the site it runs on', async () => {
  const { commands } = await sqlOf({ builder, library: 'u-proj' })
  assert.equal(commands.length, 1)
  assert.equal(commands[0].site, 'cluster-a')
  assert.ok(commands[0].command.startsWith("/c/bin/gufi_query -n 4 -d $'\\x1e' -a 1 "), commands[0].command)
  assert.ok(commands[0].command.endsWith(' /c/idx/proj/reports'), commands[0].command)
  // Every library at once: one command each.
  const all = await sqlOf({ canned: 'largest', n: 5, library: 'all' })
  assert.deepEqual(all.commands.map(c => c.site ?? 'here'), ['here', 'cluster-a'])
})
