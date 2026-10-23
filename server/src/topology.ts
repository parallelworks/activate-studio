import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { GUFI_INDEX, gufiAvailable } from './config.js'
import { queryPerDir } from './gufi.js'
import { semanticMapRoutes } from './semanticMap.js'

/**
 * Corpus topology, one aggregate query over the per-directory summaries,
 * and the routes of the semantic map (semanticMap.ts).
 */

function kbRel(p: string): string {
  const idx = path.resolve(GUFI_INDEX)
  let rel = path.resolve(p).startsWith(idx) ? path.resolve(p).slice(idx.length) : p
  rel = rel.replace(/^\/+/, '')
  return rel
}

export async function topology(): Promise<{ available: boolean; dirs?: { dir: string; files: number; bytes: number }[] }> {
  if (!gufiAvailable()) return { available: false }
  const rows = await queryPerDir(`SELECT rpath(sname, sroll), totfiles, totsize FROM vrsummary;`)
  const dirs = rows
    .map(r => ({ dir: kbRel(r[0] ?? ''), files: Number(r[1]) || 0, bytes: Number(r[2]) || 0 }))
    .filter(d => d.dir !== '')
  return { available: true, dirs }
}

export async function topologyRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/kb/topology', async () => topology())
  await semanticMapRoutes(app)
}
