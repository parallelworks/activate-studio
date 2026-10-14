// A CLI call whose credential was refused or missing is reported as a
// credential problem, naming whose credential, instead of the raw output the
// model would misread as a syntax or input error.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'cliauth-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'cliauth-ix-'))
const { cliAuthFailure } = await import('../dist/chat/tools.js')
const PROBLEM = JSON.stringify({ detail: 'Unauthorized', status: 401, title: 'Unauthorized', type: 'about:blank' }, null, 2)

test('a refused personal key is named as the user\'s', () => {
  assert.match(cliAuthFailure(PROBLEM, '', true), /refused the user's own key/)
})

test('no personal key and a refused or missing deployment credential say to add a key', () => {
  assert.match(cliAuthFailure(PROBLEM, '', false), /no personal ACTIVATE key.*refused by the platform/)
  assert.match(cliAuthFailure('', "[ERROR] no context configured; use 'pw auth' to authenticate", false), /missing/)
  assert.match(cliAuthFailure('', '[ERROR] could not extract platform host from credential', true), /own key/)
})

test('ordinary output, including output that mentions the word, is left alone', () => {
  assert.equal(cliAuthFailure('[{"name":"wf","description":"Unauthorized access audit"}]', '', false), null)
  assert.equal(cliAuthFailure('', 'Error: missing required field: account', true), null)
})
