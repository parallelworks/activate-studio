import type { FastifyInstance } from 'fastify'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { EXCLUDE_DIRS } from './config.js'
import { KbError, resolveKb } from './kb.js'
import { reindexForDir, startIndexJob } from './indexing.js'

const MAX_UPLOAD_BYTES = 200 * 1024 * 1024
const MAX_URL_BYTES = 50 * 1024 * 1024

function sanitizeName(name: string): string {
  const base = path.basename(name).replace(/[^\w.\- ()]/g, '_').replace(/\s+/g, ' ').trim()
  if (!base || base.startsWith('.')) throw new KbError(400, `unusable filename: ${name}`)
  return base
}

/** Directory drops send the filename as a relative path; sanitize per segment. */
function sanitizeRelPath(name: string): string {
  const segments = name.split('/').filter(Boolean)
  if (segments.length > 8) throw new KbError(400, 'path too deep')
  return segments.map(sanitizeName).join('/')
}

/** Resolve and create the target directory, refusing excluded trees. */
async function targetDir(rel: string): Promise<{ abs: string; rel: string }> {
  const cleaned = rel.replace(/^\/+|\/+$/g, '')
  const abs = resolveKb(cleaned)
  for (const part of cleaned.split('/')) {
    if (EXCLUDE_DIRS.has(part) || part.startsWith('.')) throw new KbError(400, `cannot upload into ${part}`)
  }
  await fsp.mkdir(abs, { recursive: true })
  return { abs, rel: cleaned }
}


function htmlToText(html: string): string {
  return html
    .replace(/<script[\s\S]*?<\/script>/gi, ' ')
    .replace(/<style[\s\S]*?<\/style>/gi, ' ')
    .replace(/<(br|\/p|\/div|\/h[1-6]|\/li|\/tr)[^>]*>/gi, '\n')
    .replace(/<[^>]+>/g, ' ')
    .replace(/&nbsp;/g, ' ').replace(/&amp;/g, '&').replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&#39;/g, "'")
    .split('\n').map(l => l.replace(/[ \t]+/g, ' ').trim()).filter(Boolean).join('\n')
}

function slugFromUrl(url: URL): string {
  const last = url.pathname.split('/').filter(Boolean).pop() ?? ''
  const host = url.hostname.replace(/^www\./, '')
  const base = (last || host).replace(/[^\w.-]/g, '-').slice(0, 80)
  return base || 'page'
}

export async function uploadRoutes(app: FastifyInstance): Promise<void> {
  // Create an empty directory in the knowledge base.
  app.post('/api/kb/mkdir', async req => {
    const body = req.body as { path?: string }
    const rel = String(body.path ?? '').trim()
    if (!rel) throw new KbError(400, 'path required')
    const { rel: cleaned } = await targetDir(sanitizeRelPath(rel))
    return { created: cleaned }
  })

  // Multipart file upload into a chosen KB directory, indexed before returning.
  app.post('/api/kb/upload', async (req, reply) => {
    const dir = String((req.query as { dir?: string }).dir ?? 'uploads')
    const { abs, rel } = await targetDir(dir)
    const saved: string[] = []
    for await (const part of req.files({ limits: { fileSize: MAX_UPLOAD_BYTES, files: 200 } })) {
      const name = sanitizeRelPath(part.filename ?? 'file')
      const dest = path.join(abs, name)
      await fsp.mkdir(path.dirname(dest), { recursive: true })
      await fsp.writeFile(dest, await part.toBuffer())
      saved.push(rel ? `${rel}/${name}` : name)
    }
    if (saved.length === 0) throw new KbError(400, 'no files in request')

    // index=0 returns as soon as the files are on disk, which is what a
    // bulk upload wants: the client sends its batches back to back and asks
    // for one indexing pass at the end, watching it through /api/index/job.
    if (String((req.query as { index?: string }).index ?? '') === '0') {
      return reply.send({ saved, indexed: false, deferred: true })
    }
    try {
      const { ms } = await reindexForDir(rel)
      return reply.send({ saved, indexed: true, indexMs: ms })
    } catch (e) {
      req.log.error(e, 'upload saved but indexing failed')
      return reply.send({
        saved,
        indexed: false,
        indexError: String((e as Error).message ?? e).slice(0, 300),
      })
    }
  })

  // Fetch a URL into the KB: PDFs saved raw, HTML reduced to text with
  // provenance header, other content saved as-is.
  app.post('/api/kb/upload-url', async (req, reply) => {
    const body = req.body as { url?: string; dir?: string }
    const rawUrl = String(body.url ?? '').trim()
    if (!/^https?:\/\//i.test(rawUrl)) throw new KbError(400, 'url must be http(s)')
    const url = new URL(rawUrl)
    const { abs, rel } = await targetDir(String(body.dir ?? 'uploads'))

    const res = await fetch(url, {
      redirect: 'follow',
      signal: AbortSignal.timeout(30_000),
      headers: { 'User-Agent': 'activate-studio-kb/1.0' },
    })
    if (!res.ok) throw new KbError(502, `fetch failed: ${res.status}`)
    const buf = Buffer.from(await res.arrayBuffer())
    if (buf.length > MAX_URL_BYTES) throw new KbError(413, 'response too large')

    const ctype = (res.headers.get('content-type') ?? '').toLowerCase()
    let name = sanitizeName(slugFromUrl(url))
    let data: Buffer | string = buf
    if (ctype.includes('pdf') || name.toLowerCase().endsWith('.pdf')) {
      if (!name.toLowerCase().endsWith('.pdf')) name += '.pdf'
    } else if (ctype.includes('html')) {
      const html = buf.toString('utf8')
      const title = /<title[^>]*>([^<]*)<\/title>/i.exec(html)?.[1]?.trim() ?? url.hostname
      data = `# ${title}\n\nSource: ${url.href}\nFetched: ${new Date().toISOString().slice(0, 10)}\n\n${htmlToText(html)}`
      name = name.replace(/\.(html?|php|aspx?)$/i, '')
      if (!name.endsWith('.md')) name += '.md'
    } else if (ctype.startsWith('text/') || ctype.includes('json') || ctype.includes('xml')) {
      if (!/\.[a-z0-9]{1,5}$/i.test(name)) name += '.txt'
    }
    const dest = path.join(abs, name)
    await fsp.writeFile(dest, data)
    const savedPath = rel ? `${rel}/${name}` : name
    const { ms } = await reindexForDir(rel)
    return reply.send({ saved: [savedPath], indexed: true, indexMs: ms })
  })
}
