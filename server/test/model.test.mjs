// The 3D preview path: a binary STL must survive the encode exactly and
// the wire format must decode the way the browser decodes it. This is the
// path that once produced a 452 MB JSON reply from a 57 MB file.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = idx
const { modelFile } = await import('../dist/model.js')

function makeStl(tri) {
  const buf = Buffer.alloc(84 + tri * 50)
  buf.writeUInt32LE(tri, 80)
  for (let i = 0; i < tri; i++) {
    const off = 84 + i * 50
    for (let k = 0; k < 9; k++) buf.writeFloatLE(i * 10 + k, off + 12 + k * 4)
  }
  return buf
}

function decode(buf) {
  const ab = buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength)
  const headerLength = new DataView(ab).getUint32(0, true)
  const header = JSON.parse(new TextDecoder().decode(new Uint8Array(ab, 4, headerLength)))
  const base = 4 + headerLength
  return { header, base, positions: new Float32Array(ab, base + header.meshes[0].positions.o, header.meshes[0].positions.n) }
}

test('binary STL round-trips exactly through the wire format', async () => {
  fs.writeFileSync(path.join(kb, 'part.stl'), makeStl(100))
  const out = fs.readFileSync(await modelFile('part.stl'))
  const { header, base, positions } = decode(out)
  assert.equal(header.format, 'stl')
  assert.equal(header.triangles, 100)
  assert.equal(base % 4, 0, 'arrays must start 4-byte aligned')
  assert.equal(positions.length, 900)
  assert.equal(positions[0], 0)
  assert.equal(positions[899], Math.fround(99 * 10 + 8))
  assert.ok(!('normals' in header.meshes[0] && header.meshes[0].normals), 'STL sends no normals')
  assert.ok(!header.meshes[0].indices, 'STL sends no identity index array')
})

test('ascii STL parses', async () => {
  const ascii = ['solid a', ' facet normal 0 0 1', '  outer loop',
    '   vertex 1 2 3', '   vertex 4 5 6', '   vertex 7 8 9',
    '  endloop', ' endfacet', 'endsolid a'].join('\n')
  fs.writeFileSync(path.join(kb, 'a.stl'), ascii)
  const { header, positions } = decode(fs.readFileSync(await modelFile('a.stl')))
  assert.equal(header.triangles, 1)
  assert.deepEqual([...positions], [1, 2, 3, 4, 5, 6, 7, 8, 9])
})

test('a second request is served from the cache file', async () => {
  const first = await modelFile('part.stl')
  const second = await modelFile('part.stl')
  assert.equal(first, second)
  assert.ok(first.endsWith('.mdl'))
})
