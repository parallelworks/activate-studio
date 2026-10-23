import fs from 'node:fs'
import path from 'node:path'

/**
 * What a launcher that keeps the Studio running offers it: a restart, and a
 * settings file the launcher reads before each start. The macOS app sets
 * STUDIO_RESTARTABLE=1 and STUDIO_CONFIG_FILE; a deployment without a
 * launcher sets neither, and a change that needs a restart says so instead.
 */

/** Exit status that asks the launcher to start the server again. */
export const RESTART_EXIT_CODE = 75
/** Exit status that asks a run loop the macOS app started itself to run the
 *  app's own run loop again, from the copy an update put in place
 *  (deploy/mac/studio-run.sh). */
export const RELAUNCH_EXIT_CODE = 76

/** The macOS app started the server's run loop itself, without a
 *  LaunchAgent, as where ~/Library/LaunchAgents is read-only. */
export function runsWithoutAgent(): boolean {
  return process.env.STUDIO_SUPERVISOR === 'detached'
}

export function canRestart(): boolean {
  return process.env.STUDIO_RESTARTABLE === '1'
}

let stopper: ((code: number) => void) | null = null
/** main.ts hands over its graceful stop, so a restart drains like a rollout. */
export function setStopper(fn: (code: number) => void): void { stopper = fn }

/** Restart soon, after the current response has gone out; `code` is
 *  RELAUNCH_EXIT_CODE after an update replaced the macOS app. */
export function requestRestart(code = RESTART_EXIT_CODE): boolean {
  if (!canRestart() || !stopper) return false
  const stop = stopper
  setTimeout(() => stop(code), 300).unref?.()
  return true
}

/** The launcher's settings file (KEY=value lines), or null without a launcher. */
export function launcherConfigFile(): string | null {
  return process.env.STUDIO_CONFIG_FILE || null
}

/** Settings the launcher passes in at the next start. */
export function readLauncherSettings(): Record<string, string> {
  const file = launcherConfigFile()
  const out: Record<string, string> = {}
  if (!file) return out
  let text = ''
  try { text = fs.readFileSync(file, 'utf8') } catch { return out }
  for (const line of text.split('\n')) {
    const m = /^([A-Z][A-Z0-9_]*)=(.*)$/.exec(line.trim())
    if (m) out[m[1]] = unquote(m[2])
  }
  return out
}

/** Set one setting for the next start, keeping the others. */
export function saveLauncherSetting(key: string, value: string): void {
  const file = launcherConfigFile()
  if (!file) throw new Error('no launcher settings file (STUDIO_CONFIG_FILE)')
  if (!/^[A-Z][A-Z0-9_]*$/.test(key)) throw new Error(`bad setting name ${key}`)
  if (/[\n\r]/.test(value)) throw new Error('a setting is one line')
  const settings = { ...readLauncherSettings(), [key]: value }
  const body = Object.entries(settings).map(([k, v]) => `${k}=${quote(v)}`).join('\n') + '\n'
  fs.mkdirSync(path.dirname(file), { recursive: true })
  const tmp = `${file}.${process.pid}.tmp`
  fs.writeFileSync(tmp, body, { mode: 0o600 })
  fs.renameSync(tmp, file)
}

// Values are single-quoted for a POSIX shell to source; a single quote
// inside is closed, escaped and reopened.
const quote = (v: string) => `'${v.replace(/'/g, `'\\''`)}'`
function unquote(v: string): string {
  if (!v.startsWith("'")) return v
  return v.slice(1, -1).replace(/'\\''/g, "'")
}
