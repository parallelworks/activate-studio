import { spawn, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import readline from 'node:readline'
import { INDEX_BASE, PROJECT_ROOT, PYTHON_BIN, indexerEnv } from './config.js'

/**
 * Query vectors from the ONNX embedding model, for search by meaning over
 * indexes embedded with it (indexer/embed_onnx.py; a directory's gvecmodel
 * table names the model). One Python process (indexer/embed_query.py),
 * started at the first such search and kept, so the model loads once: it
 * answers JSON lines on its stdin and stdout. It is stopped after ten idle
 * minutes, and ends with the server, whose exit closes its input. A worker
 * that cannot start (the packages or the model missing) is not tried again
 * for five minutes; searches meanwhile skip the indexes that need it.
 */

export const ONNX_MODEL_ID = 'minilm-l6-mean-int8'
export const ONNX_DIR = path.join(INDEX_BASE, 'models', 'minilm-l6')

/** Whether the ONNX model's files are where the worker and embed.py look. */
export function onnxModelPresent(): boolean {
  return fs.existsSync(path.join(ONNX_DIR, 'model.onnx')) && fs.existsSync(path.join(ONNX_DIR, 'tokenizer.json'))
}

const START_MS = 60_000
const REQUEST_MS = 20_000
const IDLE_MS = 10 * 60_000
const RETRY_MS = 5 * 60_000

interface Pending { resolve: (v: Buffer[]) => void; reject: (e: Error) => void; timer: NodeJS.Timeout }
interface Worker { child: ChildProcess; ready: Promise<void>; pending: Map<number, Pending> }

let worker: Worker | null = null
let nextId = 1
let idleTimer: NodeJS.Timeout | null = null
let down: { until: number; reason: string } | null = null
let command: { cmd: string; args: string[] } | null = null

/** For tests: run another program in place of embed_query.py, or the real one again (null). */
export function setQueryWorkerCommand(c: { cmd: string; args: string[] } | null): void {
  stopQueryWorker()
  command = c
  down = null
}

/** Why the worker is not running, when it failed to start lately. */
export function queryWorkerDown(): string | null {
  return down && down.until > Date.now() ? down.reason : null
}

export function stopQueryWorker(): void {
  if (idleTimer) { clearTimeout(idleTimer); idleTimer = null }
  const w = worker
  worker = null
  if (!w) return
  for (const p of w.pending.values()) { clearTimeout(p.timer); p.reject(new Error('the query embedder stopped')) }
  w.pending.clear()
  try { w.child.stdin?.end() } catch { /* gone */ }
  try { w.child.kill() } catch { /* gone */ }
}
process.once('exit', stopQueryWorker)

function start(): Worker {
  const c = command ?? { cmd: PYTHON_BIN, args: [path.join(PROJECT_ROOT, 'indexer', 'embed_query.py'), '--onnx', ONNX_DIR] }
  const child = spawn(c.cmd, c.args, {
    stdio: ['pipe', 'pipe', 'pipe'],
    env: indexerEnv({ TOKENIZERS_PARALLELISM: 'false', OMP_NUM_THREADS: '1', OPENBLAS_NUM_THREADS: '1' }),
  })
  const pending = new Map<number, Pending>()
  let stderr = ''
  child.stderr?.on('data', d => { stderr = (stderr + String(d)).slice(-2000) })
  let settle: { ok: () => void; fail: (e: Error) => void } | null = null
  const ready = new Promise<void>((ok, fail) => { settle = { ok, fail } })
  const startTimer = setTimeout(() => settle?.fail(new Error('the query embedder did not start in time')), START_MS)
  const w: Worker = { child, ready, pending }
  readline.createInterface({ input: child.stdout! }).on('line', line => {
    let msg: { ready?: boolean; error?: string; id?: number; vectors?: string[] }
    try { msg = JSON.parse(line) } catch { return }
    if (msg.ready !== undefined) {
      clearTimeout(startTimer)
      if (msg.ready) settle?.ok()
      else settle?.fail(new Error(msg.error || 'the query embedder could not start'))
      settle = null
      return
    }
    const p = typeof msg.id === 'number' ? pending.get(msg.id) : undefined
    if (!p) return
    pending.delete(msg.id!)
    clearTimeout(p.timer)
    if (msg.error || !Array.isArray(msg.vectors)) p.reject(new Error(msg.error || 'no vectors'))
    else p.resolve(msg.vectors.map(v => Buffer.from(v, 'base64')))
  })
  const gone = (why: string) => {
    clearTimeout(startTimer)
    settle?.fail(new Error(why))
    settle = null
    for (const p of pending.values()) { clearTimeout(p.timer); p.reject(new Error(why)) }
    pending.clear()
    if (worker === w) worker = null
  }
  child.on('error', e => gone(`the query embedder could not run: ${e.message}`))
  child.on('exit', code => gone(`the query embedder stopped (exit ${code})${stderr.trim() ? `: ${stderr.trim().split('\n').slice(-1)[0]}` : ''}`))
  return w
}

/** One vector per text, as 384 little-endian float32 values (1,536 bytes). */
export async function embedQueries(texts: string[]): Promise<Buffer[]> {
  const blocked = queryWorkerDown()
  if (blocked) throw new Error(blocked)
  if (!worker) worker = start()
  const w = worker
  try {
    await w.ready
  } catch (e) {
    down = { until: Date.now() + RETRY_MS, reason: (e as Error).message }
    if (worker === w) stopQueryWorker()
    throw e
  }
  if (idleTimer) clearTimeout(idleTimer)
  idleTimer = setTimeout(stopQueryWorker, IDLE_MS)
  idleTimer.unref?.()
  const id = nextId++
  return new Promise<Buffer[]>((resolve, reject) => {
    const timer = setTimeout(() => {
      w.pending.delete(id)
      reject(new Error('the query embedder did not answer in time'))
    }, REQUEST_MS)
    w.pending.set(id, { resolve, reject, timer })
    w.child.stdin?.write(JSON.stringify({ id, texts }) + '\n')
  })
}
