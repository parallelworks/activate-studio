// An image whose caption request fails is not cached, so the next pass
// asks again; an empty cached result is retried while a vision model is
// configured. Runs the real indexer/enrich.py against a stand-in gateway.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import http from 'node:http'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'

const run = promisify(execFile)

const ROOT = path.resolve(import.meta.dirname, '..', '..')
const ENRICH = path.join(ROOT, 'indexer', 'enrich.py')
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'caption-'))
const kb = path.join(work, 'kb'), index = path.join(work, 'index'), cache = path.join(work, 'extract')
fs.mkdirSync(kb); fs.mkdirSync(index)
// Over the 8 KB floor below which images are skipped as icons.
fs.copyFileSync(path.join(ROOT, 'web', 'public', 'icon-512.png'), path.join(kb, 'picture.png'))
fs.writeFileSync(path.join(index, 'db.db'), '')
const cacheFile = path.join(cache, 'picture.png.txt')

let mode = 'refuse'
let calls = 0
const gateway = http.createServer((req, res) => {
  calls++
  req.resume()
  if (mode === 'refuse') { res.writeHead(403); res.end('forbidden'); return }
  res.writeHead(200, { 'content-type': 'text/event-stream' })
  res.end('data: {"type":"response.output_text.delta","delta":"A navy tile with a white letter S."}\n\ndata: [DONE]\n\n')
})
await new Promise(r => gateway.listen(0, '127.0.0.1', r))
const env = {
  ...process.env,
  PW_API_KEY: 'test-key',
  PW_GATEWAY_URL: `http://127.0.0.1:${gateway.address().port}`,
  ADE_VISION_MODEL: 'test-vision',
}
// Asynchronous: the stand-in gateway runs in this process and has to answer
// while the indexer waits on it.
const enrich = (extra = {}) => run('python3', [ENRICH, '--kb-root', kb, '--index', index, '--extract-cache', cache],
  { env: { ...env, ...extra }, timeout: 60_000 })

test('a refused caption is not cached, and the next pass asks again', async () => {
  await enrich()
  assert.equal(calls, 1)
  assert.equal(fs.existsSync(cacheFile), false)
  mode = 'answer'
  await enrich()
  assert.equal(calls, 2)
  assert.match(fs.readFileSync(cacheFile, 'utf8'), /Image description:\nA navy tile with a white letter S\./)
})

test('a good cached caption is reused without asking again', async () => {
  await enrich()
  assert.equal(calls, 2)
})

test('an empty cached entry is retried while a vision model is configured', async () => {
  fs.writeFileSync(cacheFile, '')
  const later = new Date(Date.now() + 60_000)
  fs.utimesSync(cacheFile, later, later)
  await enrich()
  assert.equal(calls, 3)
  assert.match(fs.readFileSync(cacheFile, 'utf8'), /A navy tile/)
})

test('with captioning off, an empty entry stands', async () => {
  fs.writeFileSync(cacheFile, '')
  const later = new Date(Date.now() + 120_000)
  fs.utimesSync(cacheFile, later, later)
  await enrich({ ADE_VISION_MODEL: '' })
  assert.equal(calls, 3)
  assert.equal(fs.readFileSync(cacheFile, 'utf8'), '')
})

test.after(() => { gateway.close(); fs.rmSync(work, { recursive: true, force: true }) })
