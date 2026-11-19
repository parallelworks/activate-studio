// indexer/fetch_embed.py --ort-library: the ONNX Runtime C library the Go
// server's embedder loads, taken out of the onnxruntime wheel pinned for a
// platform (the build scripts' source since Microsoft's release archives
// left GitHub). Run on a copy of the script beside a made-up embed_deps.json
// whose wheel is already in the cache, so nothing is downloaded.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync, spawnSync } from 'node:child_process'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const PY = process.env.PYTHON_BIN || 'python3'

/** A wheel (a zip) with these members, written by Python's zipfile. */
function wheel(file, members) {
  execFileSync(PY, ['-I', '-c', `import json, sys, zipfile
z = zipfile.ZipFile(sys.argv[1], 'w')
for name, body in json.loads(sys.argv[2]).items():
    z.writestr(name, body)
z.close()`, file, JSON.stringify(members)])
}

/** A copy of fetch_embed.py whose pins name one onnxruntime wheel, cached. */
function setup(members, { version = '1.31.0' } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ort-'))
  const whl = path.join(dir, 'onnxruntime-1.31.0-cp312-cp312-manylinux_2_28_x86_64.whl')
  wheel(whl, members)
  const sha = crypto.createHash('sha256').update(fs.readFileSync(whl)).digest('hex')
  const cache = path.join(dir, 'cache')
  fs.mkdirSync(cache)
  fs.renameSync(whl, path.join(cache, `${sha.slice(0, 16)}-${path.basename(whl)}`))
  const script = path.join(dir, 'indexer', 'fetch_embed.py')
  fs.mkdirSync(path.dirname(script))
  fs.copyFileSync(path.join(root, 'indexer', 'fetch_embed.py'), script)
  fs.writeFileSync(path.join(dir, 'indexer', 'embed_deps.json'), JSON.stringify({
    packages: { onnxruntime: version },
    wheels: { 'linux-x86_64-cp312': { onnxruntime: { url: `http://127.0.0.1:9/${path.basename(whl)}`, sha256: sha, size: 1 } } },
  }))
  const run = (...args) => spawnSync(PY, ['-I', script, '--platform', 'linux-x86_64-cp312', '--cache', cache, ...args], { encoding: 'utf8' })
  return { dir, run }
}

const LIBRARY = {
  'onnxruntime/capi/libonnxruntime.so.1.31.0': 'the library',
  'onnxruntime/capi/libonnxruntime_providers_shared.so': 'a provider',
  'onnxruntime/capi/onnxruntime_pybind11_state.so': 'python',
  'onnxruntime/LICENSE': 'MIT License',
  'onnxruntime/ThirdPartyNotices.txt': 'notices',
}

test('--ort-library writes the wheel\'s library, executable, and its two license files', () => {
  const { dir, run } = setup(LIBRARY)
  const lib = path.join(dir, 'app', 'lib', 'libonnxruntime.so.1')
  const lic = path.join(dir, 'licenses')
  const r = run('--ort-library', lib, '--ort-licenses', lic, '--ort-version', '1.31.0')
  assert.equal(r.status, 0, r.stderr)
  assert.equal(fs.readFileSync(lib, 'utf8'), 'the library')
  assert.equal(fs.statSync(lib).mode & 0o777, 0o755)
  assert.equal(fs.readFileSync(path.join(lic, 'onnxruntime-LICENSE.txt'), 'utf8'), 'MIT License')
  assert.equal(fs.readFileSync(path.join(lic, 'onnxruntime-ThirdPartyNotices.txt'), 'utf8'), 'notices')
  assert.deepEqual(fs.readdirSync(path.dirname(lib)), ['libonnxruntime.so.1'], 'nothing else is left beside it')
})

test('--ort-library takes the macOS wheel\'s dylib by the same rule', () => {
  const { dir, run } = setup({ 'onnxruntime/capi/libonnxruntime.1.31.0.dylib': 'the dylib', 'onnxruntime/LICENSE': 'MIT', 'onnxruntime/ThirdPartyNotices.txt': 'n' })
  const lib = path.join(dir, 'libonnxruntime.dylib')
  const r = run('--ort-library', lib)
  assert.equal(r.status, 0, r.stderr)
  assert.equal(fs.readFileSync(lib, 'utf8'), 'the dylib')
})

test('--ort-library refuses a version other than the pinned one, a wheel without exactly one library, and a platform with no pin', () => {
  const { dir, run } = setup(LIBRARY)
  const lib = path.join(dir, 'lib.so')
  let r = run('--ort-library', lib, '--ort-version', '1.30.0')
  assert.equal(r.status, 1)
  assert.match(r.stderr, /the build wants ONNX Runtime 1\.30\.0, but embed_deps\.json pins onnxruntime 1\.31\.0/)
  assert.equal(fs.existsSync(lib), false)
  const none = setup({ 'onnxruntime/__init__.py': '' })
  r = none.run('--ort-library', path.join(none.dir, 'lib.so'))
  assert.equal(r.status, 1)
  assert.match(r.stderr, /has 0 ONNX Runtime libraries, not one/)
  r = spawnSync(PY, ['-I', path.join(dir, 'indexer', 'fetch_embed.py'), '--platform', 'macos-arm64-cp312', '--ort-library', lib], { encoding: 'utf8' })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /no pinned onnxruntime wheel for macos-arm64-cp312/)
})
