import fs from 'node:fs/promises'
import path from 'node:path'
import { createRequire } from 'node:module'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, MODEL_SUFFIXES } from './config.js'
import { KbError, resolveKb } from './kb.js'

const MODEL_CACHE = path.join(INDEX_BASE, 'model-cache')
const MAX_MODEL_BYTES = 80 * 1024 * 1024

export interface ModelMesh {
  name?: string
  color?: [number, number, number]
  positions: number[]
  normals?: number[]
  indices: number[]
}

interface ModelPayload { format: 'stl' | 'step'; meshes: ModelMesh[]; triangles: number }

/* ---- STL: binary and ascii, no dependencies ---- */

function parseStlBinary(buf: Buffer): ModelMesh {
  const count = buf.readUInt32LE(80)
  const positions = new Array<number>(count * 9)
  const normals = new Array<number>(count * 9)
  const indices = new Array<number>(count * 3)
  for (let i = 0; i < count; i++) {
    const off = 84 + i * 50
    const nx = buf.readFloatLE(off), ny = buf.readFloatLE(off + 4), nz = buf.readFloatLE(off + 8)
    for (let v = 0; v < 3; v++) {
      const vo = off + 12 + v * 12
      const j = i * 9 + v * 3
      positions[j] = buf.readFloatLE(vo)
      positions[j + 1] = buf.readFloatLE(vo + 4)
      positions[j + 2] = buf.readFloatLE(vo + 8)
      normals[j] = nx; normals[j + 1] = ny; normals[j + 2] = nz
      indices[i * 3 + v] = i * 3 + v
    }
  }
  return { positions, normals, indices }
}

function parseStlAscii(text: string): ModelMesh {
  const positions: number[] = []
  const normals: number[] = []
  const indices: number[] = []
  let facetNormal: [number, number, number] = [0, 0, 1]
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (t.startsWith('facet normal')) {
      const p = t.split(/\s+/).slice(2).map(Number)
      facetNormal = [p[0] || 0, p[1] || 0, p[2] || 0]
    } else if (t.startsWith('vertex')) {
      const p = t.split(/\s+/).slice(1).map(Number)
      positions.push(p[0] || 0, p[1] || 0, p[2] || 0)
      normals.push(...facetNormal)
      indices.push(indices.length)
    }
  }
  return { positions, normals, indices }
}

function parseStl(buf: Buffer): ModelPayload {
  let mesh: ModelMesh
  const looksBinary = buf.length >= 84 && 84 + buf.readUInt32LE(80) * 50 === buf.length
  if (looksBinary) mesh = parseStlBinary(buf)
  else mesh = parseStlAscii(buf.toString('utf8'))
  if (!mesh.positions.length) throw new KbError(422, 'could not parse STL geometry')
  return { format: 'stl', meshes: [mesh], triangles: mesh.indices.length / 3 }
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
  const meshes: ModelMesh[] = result.meshes.map((m: any) => ({
    name: m.name || undefined,
    color: m.color ?? undefined,
    positions: Array.from(m.attributes.position.array as number[]),
    normals: m.attributes.normal ? Array.from(m.attributes.normal.array as number[]) : undefined,
    indices: Array.from(m.index.array as number[]),
  }))
  const triangles = meshes.reduce((n, m) => n + m.indices.length / 3, 0)
  return { format: 'step', meshes, triangles }
}

/* ---- cache keyed by source mtime, like the pdf-preview cache ---- */

function cachePath(rel: string): string {
  return path.join(MODEL_CACHE, rel + '.json')
}

export async function modelFor(rel: string): Promise<ModelPayload> {
  const abs = resolveKb(rel)
  const suffix = path.extname(abs).toLowerCase()
  if (!MODEL_SUFFIXES.has(suffix)) throw new KbError(415, 'not a supported 3D model type')
  const st = await fs.stat(abs)
  if (st.size > MAX_MODEL_BYTES) throw new KbError(413, 'model too large for inline preview')
  const cached = cachePath(rel)
  const cst = await fs.stat(cached).catch(() => null)
  if (cst && cst.mtimeMs > st.mtimeMs) {
    try { return JSON.parse(await fs.readFile(cached, 'utf8')) } catch { /* rebuild below */ }
  }
  const buf = await fs.readFile(abs)
  const payload = suffix === '.stl' ? parseStl(buf) : await parseStep(buf)
  await fs.mkdir(path.dirname(cached), { recursive: true })
  await fs.writeFile(cached, JSON.stringify(payload))
  return payload
}

/** Cache path for a parsed 3D model, so a move can carry it. */
export function modelCachePath(rel: string): string { return cachePath(rel) }

export async function removeModelCache(rel: string): Promise<void> {
  await fs.rm(cachePath(rel), { force: true }).catch(() => {})
}

export function modelCacheDir(): string { return MODEL_CACHE }

export async function modelRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/model', async req => {
    const { path: rel = '' } = req.query as { path?: string }
    if (!rel) throw new KbError(400, 'path required')
    return modelFor(rel)
  })
}
