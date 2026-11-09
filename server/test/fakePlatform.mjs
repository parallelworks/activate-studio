// A loopback stand-in for the platform, GitHub and the pw CLI, for tests
// whose routes the Go server may answer (STUDIO_TEST_FRONT): a stub of
// globalThis.fetch or a set*Cli hook reaches only Node, while this reaches
// whichever server answers.
//
// Requests keep the addresses the tests were written against:
// https://platform.test for the platform, https://api.github.com and
// https://raw.githubusercontent.com for GitHub. The handler gets
// (url, init) as a fetch stub does and returns a Response. The fake CLI
// forwards its arguments and PW_API_KEY here, to the cli function.
//
// Call it before the modules under test load: it sets PW_GATEWAY_URL,
// PW_CLI and the GitHub overrides in process.env.
import http from 'node:http'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const ORIGINS = { '/gh-api': 'https://api.github.com', '/gh-raw': 'https://raw.githubusercontent.com' }

export async function fakePlatform({ fetch, cli } = {}) {
  const fake = {
    fetch: fetch ?? (async () => new Response('no', { status: 404 })),
    cli: cli ?? (async args => { throw new Error(`unexpected ${args.join(' ')}`) }),
  }
  const server = http.createServer((req, res) => {
    const chunks = []
    req.on('data', c => chunks.push(c))
    req.on('end', async () => {
      const body = Buffer.concat(chunks)
      try {
        if (req.url === '/__cli') {
          const { args, key } = JSON.parse(String(body))
          let out
          try { out = { stdout: String(await fake.cli(args, key)) } } catch (e) { out = { error: String(e?.message ?? e) } }
          res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(out))
          return
        }
        const prefix = Object.keys(ORIGINS).find(p => req.url.startsWith(`${p}/`))
        const url = prefix ? ORIGINS[prefix] + req.url.slice(prefix.length) : `https://platform.test${req.url}`
        const headers = {}
        for (const [k, v] of Object.entries(req.headers)) if (k === 'authorization') headers.Authorization = v; else headers[k] = v
        const r = await fake.fetch(url, { method: req.method, headers, ...(body.length ? { body } : {}) })
        const out = Buffer.from(await r.arrayBuffer())
        const h = {}
        r.headers.forEach((v, k) => { h[k] = v })
        res.writeHead(r.status, h).end(req.method === 'HEAD' ? undefined : out)
      } catch (e) {
        res.writeHead(500).end(String(e?.message ?? e))
      }
    })
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  server.unref()
  const origin = `http://127.0.0.1:${server.address().port}`
  const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'fake-pw-'))
  const pw = path.join(bin, 'pw')
  fs.writeFileSync(pw, `#!/usr/bin/env node
fetch(${JSON.stringify(`${origin}/__cli`)}, { method: 'POST', body: JSON.stringify({ args: process.argv.slice(2), key: process.env.PW_API_KEY ?? null }) })
  .then(r => r.json())
  .then(j => { if ('error' in j) { process.stderr.write(j.error); process.exit(1) } process.stdout.write(j.stdout) })
`, { mode: 0o755 })
  Object.assign(process.env, {
    PW_GATEWAY_URL: `${origin}/api/openai/v1`,
    PW_CLI: pw,
    STUDIO_GITHUB_API_URL: `${origin}/gh-api`,
    STUDIO_GITHUB_RAW_URL: `${origin}/gh-raw`,
  })
  fake.origin = origin
  fake.close = () => server.close()
  return fake
}
