import fsp from 'node:fs/promises'
import path from 'node:path'
import { createReadStream } from 'node:fs'
import type { FastifyInstance } from 'fastify'
import { KbError, readFileContent, resolveKb } from './kb.js'
import { mimeFor, removePdfPreview } from './preview.js'
import { extractPath } from './kb.js'
import { reindexForDir } from './indexing.js'
import { removeModelCache } from './model.js'

/**
 * Chat attachments are ordinary knowledge-base files under ATTACH_DIR: the
 * upload lands in the library, gets indexed (which also extracts text and
 * captions images), and the chat context reads that extraction. The
 * attachment id is the base64url-encoded KB-relative path.
 */

const ATTACH_DIR = 'uploads/chat'
const MAX_ATTACH_BYTES = 100 * 1024 * 1024
const CONTEXT_CAP = 6000

const encodeId = (rel: string): string => Buffer.from(rel, 'utf8').toString('base64url')
const decodeId = (id: string): string => {
  const rel = Buffer.from(String(id), 'base64url').toString('utf8')
  if (!rel.startsWith(ATTACH_DIR + '/') || rel.includes('..')) throw new KbError(400, 'invalid attachment id')
  return rel
}

function sanitizeName(name: string): string {
  const base = path.basename(name).replace(/[^\w.\- ()]/g, '_').replace(/\s+/g, ' ').trim()
  if (!base || base.startsWith('.')) throw new KbError(400, `unusable filename: ${name}`)
  return base
}

interface AttachmentRef {
  id: string
  filename: string
  contentType: string
  size: number
  uploadedAt: string
  conversationId?: string
}

async function refFor(rel: string): Promise<AttachmentRef | null> {
  const abs = resolveKb(rel)
  const st = await fsp.stat(abs).catch(() => null)
  if (!st?.isFile()) return null
  return {
    id: encodeId(rel),
    filename: path.basename(rel),
    contentType: mimeFor(abs),
    size: st.size,
    uploadedAt: new Date(st.mtimeMs).toISOString(),
  }
}

/** Inline text for the model: the indexer's extraction (documents and OCR +
 *  caption for images), or a pointer when nothing textual is stored. */
export async function attachmentContext(ids: string[]): Promise<string> {
  const blocks: string[] = []
  for (const id of ids.slice(0, 8)) {
    let rel: string
    try { rel = decodeId(id) } catch { continue }
    const fc = await readFileContent(rel).catch(() => null)
    if (!fc) { blocks.push(`[Attachment not found: ${id}]`); continue }
    const head = `[Attached file: ${path.basename(rel)}; stored in the knowledge base at ${rel}]`
    if (fc.content?.trim()) {
      const text = fc.content.length > CONTEXT_CAP ? fc.content.slice(0, CONTEXT_CAP) + '\n…(truncated; read_kb_file can fetch more)' : fc.content
      blocks.push(`${head}\n${text}`)
    } else {
      blocks.push(`${head}\n(No extracted text yet; use read_kb_file or search_kb on that path if needed.)`)
    }
  }
  return blocks.join('\n\n')
}

export async function attachmentRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/chat/attachments', async req => {
    const part = await req.file({ limits: { fileSize: MAX_ATTACH_BYTES } })
    if (!part) throw new KbError(400, 'no file in request')
    const dirAbs = resolveKb(ATTACH_DIR)
    await fsp.mkdir(dirAbs, { recursive: true })
    let name = sanitizeName(part.filename ?? 'attachment')
    // Never overwrite an existing library file on name collision.
    if (await fsp.stat(path.join(dirAbs, name)).then(() => true).catch(() => false)) {
      const ext = path.extname(name)
      name = `${path.basename(name, ext)}-${Date.now().toString(36)}${ext}`
    }
    const rel = `${ATTACH_DIR}/${name}`
    await fsp.writeFile(path.join(dirAbs, name), await part.toBuffer())
    // Index synchronously so extraction (and image captions) exist by the
    // time the message is sent.
    await reindexForDir(ATTACH_DIR).catch(() => {})
    const ref = await refFor(rel)
    if (!ref) throw new KbError(500, 'attachment write failed')
    return ref
  })

  app.get('/api/chat/attachments', async req => {
    const { limit = '50', offset = '0' } = req.query as { limit?: string; offset?: string }
    const dirAbs = resolveKb(ATTACH_DIR)
    const names = await fsp.readdir(dirAbs).catch(() => [] as string[])
    const refs = (await Promise.all(names.map(n => refFor(`${ATTACH_DIR}/${n}`)))).filter(Boolean) as AttachmentRef[]
    refs.sort((a, b) => b.uploadedAt.localeCompare(a.uploadedAt))
    const off = Number(offset) || 0
    return { attachments: refs.slice(off, off + (Number(limit) || 50)), total: refs.length }
  })

  app.get('/api/chat/attachments/:id/download', async (req, reply) => {
    const rel = decodeId((req.params as { id: string }).id)
    const abs = resolveKb(rel)
    reply.header('Content-Type', mimeFor(abs))
    reply.header('Content-Disposition', `attachment; filename="${path.basename(abs)}"`)
    return reply.send(createReadStream(abs))
  })

  app.delete('/api/chat/attachments/:id', async req => {
    const rel = decodeId((req.params as { id: string }).id)
    const abs = resolveKb(rel)
    await fsp.rm(abs, { force: true })
    await fsp.rm(extractPath(rel), { force: true }).catch(() => {})
    await removePdfPreview(rel)
    await removeModelCache(rel)
    await reindexForDir(ATTACH_DIR).catch(() => {})
    return { deleted: rel }
  })
}
