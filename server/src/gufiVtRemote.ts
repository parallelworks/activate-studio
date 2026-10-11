/**
 * gufi_vt's remote command for a library on a user's cluster.
 *
 * gufi_vt runs `remote_cmd`, each `remote_arg`, then the whole gufi_query
 * command line as one final argument, with every SQL value wrapped in
 * double quotes and nothing escaped; a shell on the far side would expand
 * `$`, backticks, and backslashes inside them. So this program does not
 * hand the line to any shell. It splits the line the way gufi_vt built it
 * (double- or single-quoted values taken literally, other words split on
 * spaces), sends the arguments to the Studio server over loopback with the
 * location's token and the server's per-process secret, and the server
 * runs them on the cluster through the user's existing session, argv
 * encoded, without a shell. The rows come back as gufi_query wrote them
 * (gufi_vt reads its own length-prefixed stream) and go to stdout as is.
 *
 * Invoked as: node gufiVtRemote.js <token> "<gufi_query command line>"
 * Environment: STUDIO_GUFI_VT_URL and STUDIO_GUFI_VT_SECRET, set by the
 * Studio on the gufi_sqlite3 that runs this.
 *
 * It exits 0 even when the location fails, writing why to stderr: a
 * nonzero exit with no rows fails the whole gufi_vt statement, and the
 * Studio already knows which location failed and reports it.
 */

/** Splits a gufi_vt command line into arguments, without expansion. */
export function splitCommandLine(line: string): string[] {
  const out: string[] = []
  let i = 0
  while (i < line.length) {
    while (line[i] === ' ') i++
    if (i >= line.length) break
    const q = line[i]
    if (q === '"' || q === "'") {
      const end = line.indexOf(q, i + 1)
      if (end < 0) throw new Error('unterminated quote in the gufi_vt command line')
      out.push(line.slice(i + 1, end))
      i = end + 1
    } else {
      let end = line.indexOf(' ', i)
      if (end < 0) end = line.length
      out.push(line.slice(i, end))
      i = end
    }
  }
  return out
}

async function main(): Promise<void> {
  const [token, line] = process.argv.slice(2)
  const url = process.env.STUDIO_GUFI_VT_URL, secret = process.env.STUDIO_GUFI_VT_SECRET
  if (!token || !line || !url || !secret) { process.stderr.write('gufi_vt remote: missing token, command line, or Studio address\n'); return }
  let args: string[]
  try { args = splitCommandLine(line) } catch (e) { process.stderr.write(`gufi_vt remote: ${(e as Error).message}\n`); return }
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Studio-Secret': secret },
    body: JSON.stringify({ token, args }),
    signal: AbortSignal.timeout(Number(process.env.STUDIO_GUFI_VT_TIMEOUT_MS) || 300_000),
  }).catch((e: Error) => { process.stderr.write(`gufi_vt remote: ${e.message}\n`); return null })
  if (!res) return
  const body = Buffer.from(await res.arrayBuffer())
  if (!res.ok) { process.stderr.write(`gufi_vt remote: ${body.toString('utf8').slice(0, 500)}\n`); return }
  const err = res.headers.get('x-stderr')
  if (err) process.stderr.write(Buffer.from(err, 'base64'))
  await new Promise<void>(resolve => process.stdout.write(body, () => resolve()))
}

// Run only as a program, not when imported by the tests.
if (process.argv[1] && /gufiVtRemote\.js$/.test(process.argv[1])) void main()
