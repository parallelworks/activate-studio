#!/usr/bin/env node
// pnpm start: the Go server over a clone, as the apps and the deployment
// workflow run it (STUDIO_NODE=off), serving web/dist from `pnpm build`.
// It is built first with the Go toolchain, which only rebuilds what
// changed. `pnpm start:node` runs the Node reference server instead.
import { spawn, spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const go = process.env.GO || 'go'
const bin = join(root, 'server-go', 'bin', process.platform === 'win32' ? 'studio-server.exe' : 'studio-server')

if (!existsSync(join(root, 'web', 'dist', 'index.html'))) {
  console.error('No web/dist/index.html: run pnpm build first.')
  process.exit(1)
}
const built = spawnSync(go, ['build', '-o', bin, './cmd/studio-server'], { cwd: join(root, 'server-go'), stdio: 'inherit' })
if (built.error?.code === 'ENOENT') {
  console.error(`pnpm start builds the Go server and needs Go (${go} was not found; https://go.dev/dl, the version server-go/go.mod names or newer). pnpm start:node runs the Node server instead.`)
  process.exit(1)
}
if (built.status !== 0) process.exit(built.status ?? 1)

const env = { ...process.env }
env.STUDIO_NODE ??= 'off'
env.STUDIO_PROJECT_ROOT ??= root
const server = spawn(bin, process.argv.slice(2), { cwd: root, env, stdio: 'inherit' })
for (const s of ['SIGINT', 'SIGTERM', 'SIGHUP']) process.on(s, () => server.kill(s))
server.on('exit', (code, signal) => process.exit(code ?? (signal ? 1 : 0)))
