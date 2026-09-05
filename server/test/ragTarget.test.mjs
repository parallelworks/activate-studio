import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
delete process.env.PW_CONTEXT
delete process.env.PW_PLATFORM_HOST
const { explicitPlatformTarget, startRagEndpoint, ragEndpointStatus } = await import('../dist/ragEndpoint.js')

test('no explicit target means no registration, with the reason recorded', () => {
  assert.equal(explicitPlatformTarget(), null)
  const st = startRagEndpoint()
  assert.equal(st.state, 'error')
  assert.match(st.detail, /PW_CONTEXT or PW_PLATFORM_HOST/)
  assert.equal(ragEndpointStatus().name, null, 'nothing was named, so nothing was spawned')
})

test('PW_CONTEXT becomes an explicit --context on the child', () => {
  process.env.PW_CONTEXT = 'activate'
  assert.deepEqual(explicitPlatformTarget(), { args: ['--context', 'activate'], detail: 'context activate' })
  delete process.env.PW_CONTEXT
})

test('PW_PLATFORM_HOST alone is an explicit target the CLI honors by itself', () => {
  process.env.PW_PLATFORM_HOST = 'activate.example.org'
  assert.deepEqual(explicitPlatformTarget(), { args: [], detail: 'platform activate.example.org' })
  delete process.env.PW_PLATFORM_HOST
})
