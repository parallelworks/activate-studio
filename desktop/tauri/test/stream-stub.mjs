// Streams for the shell's smoke test: GET /stream sends N chunks DELAY ms
// apart over fetch, GET /sse the same as server-sent events. Each chunk
// carries the time it was sent, so the page can tell whether the window
// delivered it at once or held it back. CORS is open: the page is the
// Studio's, on another port.
//
// Usage: node stream-stub.mjs PORT
import http from 'node:http'

const port = Number(process.argv[2] || 0)
const sleep = ms => new Promise(r => setTimeout(r, ms))

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://x')
  const n = Math.min(Number(url.searchParams.get('n') || 10), 100)
  const delay = Math.min(Number(url.searchParams.get('delay') || 200), 2000)
  const base = { 'Access-Control-Allow-Origin': '*', 'Cache-Control': 'no-cache' }
  if (url.pathname === '/stream') {
    const type = url.searchParams.get('type') || 'text/plain'
    const headers = { ...base, 'Content-Type': type.replace(';nosniff', '') + '; charset=utf-8' }
    if (type.endsWith(';nosniff')) headers['X-Content-Type-Options'] = 'nosniff'
    res.writeHead(200, headers)
    const sse = type.startsWith('text/event-stream')
    for (let i = 0; i < n; i++) {
      res.write(sse ? `data: chunk ${i} ${Date.now()}\n\n` : `chunk ${i} ${Date.now()}\n`)
      if (i < n - 1) await sleep(delay)
    }
    res.end()
    return
  }
  if (url.pathname === '/sse') {
    res.writeHead(200, { ...base, 'Content-Type': 'text/event-stream' })
    for (let i = 0; i < n; i++) {
      res.write(`id: ${i}\ndata: ${JSON.stringify({ i, t: Date.now() })}\n\n`)
      if (i < n - 1) await sleep(delay)
    }
    res.write('event: end\ndata: {}\n\n')
    res.end()
    return
  }
  res.writeHead(404, base)
  res.end()
})

server.listen(port, '127.0.0.1', () => console.log(`stream stub on ${server.address().port}`))
