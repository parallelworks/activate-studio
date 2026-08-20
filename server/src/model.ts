import fs from 'node:fs/promises'
import { createReadStream } from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, MODEL_SUFFIXES } from './config.js'
import { KbError, resolveKb } from './kb.js'

/**
 * 3D previews: STL and STEP are meshed here and the geometry goes to the
 * browser as binary, not as JSON.
 *
 * The distinction matters more than it looks. A mesh sent as JSON numbers
 * costs roughly eight times the file on disk, because every coordinate is
 * printed as a decimal string and the whole reply has to exist as one
 * JavaScript string before a byte of it is sent. A 57 MB STL measured at a
 * 452 MB reply and just over 1 GB resident, and at 76 MB the reply exceeds
 * V8's maximum string length and throws. Binary costs 12 bytes per vertex
 * whatever the coordinates look like, so the reply is smaller than the file
 * that produced it and nothing large is held as a string.
 *
 * Wire format, little endian throughout:
 *
 *     uint32   header length in bytes
 *     bytes    that many bytes of UTF-8 JSON, padded to a multiple of 4
 *     bytes    the arrays, back to back, in the order the header names them
 *
 * The header carries the format, the triangle count, and one entry per mesh
 * with a byte offset and element count per array. Offsets are relative to
 * the end of the header, which is what lets the header be written after the
 * offsets are known without the two definitions chasing each other. Every
 * array starts on a 4-byte boundary so the browser can read it in place.
 */

const MODEL_CACHE = path.join(INDEX_BASE, 'model-cache')
// The cost is now about one byte in memory per byte on disk, so this is a
// limit on what is reasonable to push down a link and draw, not a limit
// imposed by how the parse behaves.
const MAX_MODEL_BYTES = 80 * 1024 * 1024

export interface ModelMesh {
  name?: string
  color?: [number, number, number]
  positions: Float32Array
  normals?: Float32Array
  indices?: Uint32Array
}

interface ModelPayload { format: 'stl' | 'step'; meshes: ModelMesh[]; triangles: number }

/* ---- STL: binary and ascii, no dependencies ---- */

/**
 * STL has no shared vertices: every triangle carries its own three, so the
 * index array a naive reader builds is the sequence 0, 1, 2, 3 and adds
 * nothing. Facet normals are equally redundant, because normals computed
 * from a non-indexed mesh are per-face by construction and reproduce the
 * same flat shading. Sending neither halves the payload and drops an array
 * that was pure overhead.
 */
function parseStlBinary(buf: Buffer): ModelMesh {
  const count = buf.readUInt32LE(80)
  const positions = new Float32Array(count * 9)
  for (let i = 0; i < count; i++) {
    const off = 84 + i * 50
    for (let v = 0; v < 3; v++) {
      const vo = off + 12 + v * 12
      const j = i * 9 + v * 3
      positions[j] = buf.readFloatLE(vo)
      positions[j + 1] = buf.readFloatLE(vo + 4)
      positions[j + 2] = buf.readFloatLE(vo + 8)
    }
  }
  return { positions }
}

function parseStlAscii(text: string): ModelMesh {
  const lines = text.split('\n')
  // Counting first means one exact allocation instead of a growing array
  // that reallocates and leaves the old copies to be collected.
  let verts = 0
  for (const line of lines) if (line.trimStart().startsWith('vertex')) verts++
  const positions = new Float32Array(verts * 3)
  let j = 0
  for (const line of lines) {
    const t = line.trim()
    if (!t.startsWith('vertex')) continue
    const p = t.split(/\s+/)
    positions[j++] = Number(p[1]) || 0
    positions[j++] = Number(p[2]) || 0
    positions[j++] = Number(p[3]) || 0
  }
  return { positions }
}

function parseStl(buf: Buffer): ModelPayload {
  const looksBinary = buf.length >= 84 && 84 + buf.readUInt32LE(80) * 50 === buf.length
  const mesh = looksBinary ? parseStlBinary(buf) : parseStlAscii(buf.toString('utf8'))
  if (!mesh.positions.length) throw new KbError(422, 'could not parse STL geometry')
  return { format: 'stl', meshes: [mesh], triangles: mesh.positions.length / 9 }
}

/* ---- STEP: OpenCascade WASM (occt-import-js) meshes the BREP ---- */

const require = createRequire(import.meta.url)
let occtPromise: Promise<any> | null = null
function occt(): Promise<any> {
  occtPromise ??= (require('occt-import-js') as () => Promise<any>)()
  return occtPromise
}

async function parseStep(buf: Buffer): Promise<ModelPayload> {
  const o = await occt()
  const result = o.ReadStepFile(new Uint8Array(buf), null)
  if (!result?.success || !result.meshes?.length) throw new KbError(422, 'could not convert STEP geometry')
  // A STEP body is genuinely indexed and its normals describe curvature
  // that cannot be recovered from the triangles, so both are kept here.
  const meshes: ModelMesh[] = result.meshes.map((m: any) => ({
    name: m.name || undefined,
    color: m.color ?? undefined,
    positions: new Float32Array(m.attributes.position.array),
    normals: m.attributes.normal ? new Float32Array(m.attributes.normal.array) : undefined,
    indices: new Uint32Array(m.index.array),
  }))
  const triangles = meshes.reduce((n, m) => n + (m.indices?.length ?? 0) / 3, 0)
  return { format: 'step', meshes, triangles }
}

/* ---- binary encoding ---- */

interface Span { o: number; n: number }

function encodeModel(p: ModelPayload): Buffer {
  const blobs: Buffer[] = []
  let at = 0
  const put = (a: Float32Array | Uint32Array): Span => {
    const b = Buffer.from(a.buffer, a.byteOffset, a.byteLength)
    blobs.push(b)
    const span = { o: at, n: a.length }
    at += b.length
    return span
  }
  const meshes = p.meshes.map(m => ({
    name: m.name,
    color: m.color,
    positions: put(m.positions),
    normals: m.normals ? put(m.normals) : undefined,
    indices: m.indices ? put(m.indices) : undefined,
  }))
  const json = Buffer.from(JSON.stringify({ format: p.format, triangles: p.triangles, meshes }), 'utf8')
  // Pad so the arrays begin 4-byte aligned; a typed array cannot be read in
  // place from a misaligned offset.
  const pad = (4 - (json.length % 4)) % 4
  const head = Buffer.alloc(4 + json.length + pad, 0x20)
  head.writeUInt32LE(json.length + pad, 0)
  json.copy(head, 4)
  return Buffer.concat([head, ...blobs])
}

/* ---- cache keyed by source mtime, like the pdf-preview cache ---- */

function cachePath(rel: string): string {
  return path.join(MODEL_CACHE, rel + '.mdl')
}

/**
 * Build the preview if it is missing or stale, and hand back the file. The
 * caller streams it, so a second viewing of the same part never parses
 * anything and never holds the geometry in memory at all.
 */
export async function modelFile(rel: string): Promise<string> {
  const abs = resolveKb(rel)
  const suffix = path.extname(abs).toLowerCase()
  if (!MODEL_SUFFIXES.has(suffix)) throw new KbError(415, 'not a supported 3D model type')
  const st = await fs.stat(abs)
  if (st.size > MAX_MODEL_BYTES) {
    const mb = (n: number) => `${Math.round(n / (1024 * 1024))} MB`
    throw new KbError(413, `this model is ${mb(st.size)} and the inline preview stops at ${mb(MAX_MODEL_BYTES)}`)
  }
  const cached = cachePath(rel)
  const cst = await fs.stat(cached).catch(() => null)
  if (cst && cst.mtimeMs > st.mtimeMs) return cached
  const buf = await fs.readFile(abs)
  const payload = suffix === '.stl' ? parseStl(buf) : await parseStep(buf)
  await fs.mkdir(path.dirname(cached), { recursive: true })
  // Write beside the target and rename, so a reader never opens a file that
  // is still being written.
  const tmp = `${cached}.${process.pid}.tmp`
  await fs.writeFile(tmp, encodeModel(payload))
  await fs.rename(tmp, cached)
  return cached
}

/** Cache path for a parsed 3D model, so a move can carry it. */
export function modelCachePath(rel: string): string { return cachePath(rel) }

export async function removeModelCache(rel: string): Promise<void> {
  await fs.rm(cachePath(rel), { force: true }).catch(() => {})
}

export function modelCacheDir(): string { return MODEL_CACHE }

export async function modelRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/model', async (req, reply) => {
    const { path: rel = '' } = req.query as { path?: string }
    if (!rel) throw new KbError(400, 'path required')
    const file = await modelFile(rel)
    return reply.type('application/octet-stream').send(createReadStream(file))
  })
}
