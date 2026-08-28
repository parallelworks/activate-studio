// The sealed key cookie is the only durable copy of a personal key, so its
// attributes decide whether a key survives a restart. A deployment is
// reached through a sessions domain that differs from the platform's own,
// which makes the app a cross-site context: a Strict cookie is set there
// and then never sent back, and the key looks lost on every restart.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

const { keyCookieJar } = await import('../dist/chat/routes.js')
const attrs = h => h.split(';').slice(1).map(s => s.trim())
const named = (jar, prefix) => jar.filter(h => new RegExp(`^${prefix}\\d+=`).test(h))

test('the cookie can cross sites, or it is never sent back', () => {
  const [first] = keyCookieJar(['blob-a'])
  assert.ok(attrs(first).includes('SameSite=None'), 'Strict and Lax are never sent in a cross-site frame')
  assert.ok(attrs(first).includes('Secure'), 'SameSite=None requires Secure')
  assert.ok(attrs(first).includes('HttpOnly'), 'script must not be able to read the sealed key')
})

test('both a partitioned and an unpartitioned copy are written', () => {
  // Neither form alone covers both ways the app is opened: partitioned is
  // required where third-party cookies are blocked and the app runs inside
  // the platform, unpartitioned is what arrives when it is opened directly.
  const jar = keyCookieJar(['blob-a'])
  const plain = named(jar, 'ade_mk')
  const part = named(jar, 'ade_mkp')
  assert.ok(plain.length, 'an unpartitioned copy is written')
  assert.ok(part.length, 'a partitioned copy is written')
  assert.ok(!attrs(plain[0]).includes('Partitioned'))
  assert.ok(attrs(part[0]).includes('Partitioned'))
  assert.equal(plain[0].split(';')[0], 'ade_mk0=blob-a')
  assert.equal(part[0].split(';')[0], 'ade_mkp0=blob-a')
})

test('every chunk is written, and each carries the same policy', () => {
  const jar = keyCookieJar(['a', 'b', 'c'])
  const plain = named(jar, 'ade_mk')
  for (let i = 0; i < 3; i++) {
    assert.ok(plain[i].startsWith(`ade_mk${i}=${'abc'[i]};`), `chunk ${i} is written in order`)
    assert.ok(attrs(plain[i]).includes('SameSite=None'))
  }
})

test('no header expires a cookie another header in the same response sets', () => {
  // Identity is name, domain and path; SameSite is not part of it. A stray
  // expiry for the old attributes would match and delete the fresh value.
  const jar = named(keyCookieJar(['a', 'b']), 'ade_mk')
  const set = new Set()
  const cleared = new Set()
  for (const h of jar) {
    const name = h.slice(0, h.indexOf('='))
    ;(/Max-Age=0(;|$)/.test(h) ? cleared : set).add(name)
  }
  assert.deepEqual([...set], ['ade_mk0', 'ade_mk1'])
  for (const n of set) assert.ok(!cleared.has(n), `${n} is both set and cleared in one response`)
})

test('a shorter key expires the chunks it no longer uses', () => {
  const names = named(keyCookieJar(['a']), 'ade_mk').map(h => h.slice(0, h.indexOf('=')))
  assert.ok(names.includes('ade_mk1') && names.includes('ade_mk2'), 'trailing chunks are expired')
})

test('clearing expires every chunk of both copies', () => {
  const jar = keyCookieJar([])
  assert.equal(named(jar, 'ade_mk').length, 4)
  assert.equal(named(jar, 'ade_mkp').length, 4, 'the partitioned copy must be cleared too')
  for (const h of jar) {
    assert.match(h, /Max-Age=0/)
    assert.ok(attrs(h).includes('SameSite=None'), 'clearing must match the policy it was set with')
  }
})
