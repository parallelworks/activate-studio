// Sizes of the shell and its bundles, and a pass/fail summary of the smoke
// test's results. Exits 1 when a check failed (after writing everything).
//
// Usage: node report.mjs TARGET_RELEASE_DIR SMOKE_OUT_DIR
import fs from 'node:fs'
import path from 'node:path'

const [release, out] = process.argv.slice(2)
fs.mkdirSync(out, { recursive: true })
const read = f => { try { return JSON.parse(fs.readFileSync(path.join(out, f), 'utf8')) } catch { return null } }
const mb = n => Math.round((n / 1048576) * 100) / 100

const sizes = {}
for (const name of ['activate-studio-shell', 'activate-studio-shell.exe']) {
  const f = path.join(release, name)
  if (fs.existsSync(f)) sizes[name] = { bytes: fs.statSync(f).size, mb: mb(fs.statSync(f).size) }
}
const bundle = path.join(release, 'bundle')
if (fs.existsSync(bundle)) {
  for (const kind of fs.readdirSync(bundle)) {
    for (const f of fs.readdirSync(path.join(bundle, kind))) {
      const p = path.join(bundle, kind, f)
      if (fs.statSync(p).isFile() && /\.(deb|rpm|AppImage|exe|msi)$/.test(f)) sizes[`${kind}/${f}`] = { bytes: fs.statSync(p).size, mb: mb(fs.statSync(p).size) }
    }
  }
}
fs.writeFileSync(path.join(out, 'sizes.json'), JSON.stringify(sizes, null, 2))

const r1 = read('results-1.json') || {}
const r2 = read('results-2.json') || {}
const orphans = read('orphans.json') || {}
const killT = read('kill-T.json')
const killK = read('kill-K.json')
const ok = k => !!(r1[k] && r1[k].ok)
const checks = {
  '1 start: server answered, Studio drawn': typeof r1.coldStartMs === 'number' && typeof r1.serverAnsweredMs === 'number',
  '2 exit 76: restarted and reloaded': ok('restart_76'),
  '2 other exit: error page, Try Again': ok('exit_other_retry'),
  '2 close: server stopped, then exit': !!(r2.stop && r2.stop.how === 'window-close'),
  '2 no orphan processes': orphans.ok === true,
  ...(killT ? { '2 TERM to the shell: nothing left': killT.shellExited && killT.alive.length === 0 } : {}),
  '3 bridge: postMessage returns a Promise': ok('bridge'),
  '3 chooseFolder (test mode)': ok('chooseFolder_testMode'),
  '3 chooseFolder (real dialog, cancelled)': ok('chooseFolder_realDialog'),
  '3 studioMoved (port change)': ok('port_change') && ok('studio_moved'),
  '3 relaunch: a new copy came up': r2.instance === 2 && typeof r2.coldStartMs === 'number',
  '4 fetch stream (text/event-stream) incremental': !!(r1.stream_fetch_eventstream && r1.stream_fetch_eventstream.incremental),
  '4 EventSource incremental': !!(r1.stream_eventsource && r1.stream_eventsource.incremental),
  '5 blob download saved': ok('download'),
  '5 outside links to the default browser': ok('external_links') && ok('external_links_click'),
}
const info = {
  killTerm: killT,
  killForced: killK,
  bridgeHow: r1.bridge && r1.bridge.how,
  webgl: r1.webgl,
  streamPlainNosniff: r1.stream_fetch_plain_nosniff,
  streamPlain: r1.stream_fetch_plain,
  coldStartMs: r1.coldStartMs,
  relaunchColdStartMs: r2.coldStartMs,
  memory: r1.memory1,
  memoryRelaunched: r2.memory2,
  stopFirst: r1.stop,
  stopSecond: r2.stop,
  webviewVersion: r1.webviewVersion,
  sizes,
}
const failed = Object.entries(checks).filter(([, v]) => !v).map(([k]) => k)
fs.writeFileSync(path.join(out, 'summary.json'), JSON.stringify({ checks, failed, info }, null, 2))
for (const [k, v] of Object.entries(checks)) console.log(`${v ? 'PASS' : 'FAIL'}  ${k}`)
console.log(JSON.stringify(info, null, 2))
process.exit(failed.length ? 1 : 0)
