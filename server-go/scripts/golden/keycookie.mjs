// Seals a personal key with server/dist/credentials.js, or opens one,
// for internal/keycookie's tests. INDEX_BASE holds the secret.
// node keycookie.mjs <repo> seal <sub> <key> <baseUrl|-> <options JSON|->
// node keycookie.mjs <repo> open <sub> <cookie header>
import path from 'node:path'
const [, , repo, op, sub, ...rest] = process.argv
const c = await import(path.join(path.resolve(repo), 'server/dist/credentials.js'))
if (op === 'seal') {
  const [key, base, opts] = rest
  c.setUserKey(sub, key, true, base === '-' ? null : base, opts === '-' ? null : JSON.parse(opts))
  console.log(JSON.stringify(c.sealKeyCookie(sub)))
} else {
  c.hydrateFromCookie(sub, rest[0])
  console.log(JSON.stringify(c.resolveUserCred(sub)))
}
