// Loaded before every test file (--import in the test scripts). Tests do
// not reach the network: a fetch to anything but a loopback address fails
// at once, unless the test has replaced fetch with its own stub. Without
// this a test that forgot to stub fetch would call the live platform with
// whatever credential the host has, and pass or fail on the network.
const loopback = new Set(['127.0.0.1', 'localhost', '::1', '[::1]'])
const passthrough = globalThis.fetch
globalThis.fetch = async (input, init) => {
  const href = typeof input === 'string' ? input : input instanceof URL ? input.href : input?.url
  let host = ''
  try { host = new URL(String(href)).hostname } catch { /* a relative URL is not a network call */ }
  if (host && !loopback.has(host)) throw new TypeError(`network call blocked in tests: ${host}`)
  return passthrough(input, init)
}
