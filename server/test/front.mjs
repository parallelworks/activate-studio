// Loaded before every test file in the Go front run (npm run test:front;
// docs/GO-PORT.md, "Tests against a server URL"). With STUDIO_TEST_FRONT
// naming the Go server's binary, every Fastify app a test builds answers
// app.inject over HTTP through that binary: the app listens on a loopback
// port, the Go server starts in front of it (STUDIO_NODE_URL, identity
// passed through as headers), and the request goes to the Go server. The
// test's process, modules and stubs stay as they are, so the files run
// unchanged. Without STUDIO_TEST_FRONT this does nothing.
import { registerHooks } from 'node:module'

if (process.env.STUDIO_TEST_FRONT) {
  // The Go server and the test's Node app share the bridge secret
  // (src/frontBridge.ts), so Go routes can ask Node for what it still holds.
  process.env.STUDIO_BRIDGE_SECRET ??= (await import('node:crypto')).randomBytes(24).toString('hex')
  const shim = new URL('./frontFastify.mjs', import.meta.url).href
  registerHooks({
    resolve(specifier, context, next) {
      const parent = context.parentURL ?? ''
      if (specifier === 'fastify' && parent !== shim && !parent.includes('/node_modules/')) {
        return { url: shim, format: 'module', shortCircuit: true }
      }
      return next(specifier, context)
    },
  })
}
