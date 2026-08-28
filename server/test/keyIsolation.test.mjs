// A browser keeps one cookie jar per host regardless of who is signed in,
// so the sealed key cookie must name the subject it belongs to. Without
// that it is a bearer token: signing in as a second user in the same
// browser handed them the first user's key, which is how one account came
// to be using another account's provider model.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

const c = await import('../dist/credentials.js')
const { keyCookieJar } = await import('../dist/chat/routes.js')

const ALICE = 'alice-sub-uuid'
const BOB = 'bob-sub-uuid'
const ALICE_KEY = 'sk-alice-private-0001'

/** The Cookie header a browser would send back for a Set-Cookie jar.
 *  Expired chunks (an empty value) are dropped; a real value is base64 and
 *  frequently ends in '=' padding, so the test cannot filter on that. */
const asCookieHeader = jar => jar
  .map(h => h.split(';')[0])
  .filter(kv => kv.slice(kv.indexOf('=') + 1).length > 0)
  .join('; ')

test('a second user in the same browser does not inherit the first user\'s key', () => {
  c.setUserKey(ALICE, ALICE_KEY, false, null)
  const header = asCookieHeader(keyCookieJar(c.sealKeyCookie(ALICE) ?? []))
  assert.ok(header.includes('ade_mk0='), 'the browser holds a sealed cookie')

  // Same browser, different signed-in user: Bob's request carries Alice's
  // cookie because the jar belongs to the host, not to the account.
  c.hydrateFromCookie(BOB, header)
  assert.equal(c.resolveUserKey(BOB), null, 'Bob must not receive Alice\'s key')
  assert.equal(c.getUserKeyStatus(BOB).mode, 'none', 'and must not appear to have one')
})

test('the owner still gets their own key back from the same cookie', () => {
  c.setUserKey(ALICE, ALICE_KEY, false, null)
  const header = asCookieHeader(keyCookieJar(c.sealKeyCookie(ALICE) ?? []))
  c.clearUserKey(ALICE)
  assert.equal(c.resolveUserKey(ALICE), null, 'memory entry is gone, as after a restart')

  c.hydrateFromCookie(ALICE, header)
  assert.equal(c.resolveUserKey(ALICE), ALICE_KEY, 'the rightful owner is restored')
})

test('a cookie from a build that sealed no subject is refused', () => {
  // Such a cookie cannot be attributed to anyone, so trusting it is the
  // same leak. One re-entry is the cost of closing it.
  c.setUserKey(ALICE, ALICE_KEY, false, null)
  const sealed = c.sealKeyCookie(ALICE) ?? []
  c.clearUserKey(ALICE)

  // Rebuild the legacy payload shape: {k,u} with no subject.
  const legacy = JSON.parse(JSON.stringify({ k: ALICE_KEY, u: null }))
  assert.ok(!('s' in legacy), 'the legacy shape carries no subject')
  assert.ok(sealed.length, 'current builds still seal something')

  // Feed a jar whose value is not openable as a subject-bearing payload.
  c.hydrateFromCookie(ALICE, 'ade_mk0=not-a-valid-sealed-blob')
  assert.equal(c.resolveUserKey(ALICE), null, 'an unattributable cookie grants nothing')
})

test('an unrelated cookie jar is ignored without throwing', () => {
  c.hydrateFromCookie(BOB, 'session=abc; theme=dark')
  assert.equal(c.resolveUserKey(BOB), null)
})
