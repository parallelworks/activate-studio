// The helper of deploy/linux/test_update_e2e.sh, run with the payload's own
// Node so that the test host needs no other tool. Nothing here publishes
// anything: it makes a throwaway signing key, stamps a copy of the payload
// as a version, and serves release archives on 127.0.0.1 in the shape of
// GitHub's releases API, for the Studio's update check.
//
//   keygen DIR                               DIR/update.key (PEM) and DIR/update.pub
//   stamp TREE VERSION PUBFILE               TREE (a Linux payload, or a macOS app's Contents/Resources)
//                                            becomes VERSION, verifying updates with PUBFILE
//   add-release DIR PORT TAG ARCHIVE KEY [corrupt]
//                                            put the archive, a manifest and a signature in DIR as
//                                            release TAG ("corrupt": the archive's bytes are changed
//                                            after the manifest was made, so its SHA-256 is wrong);
//                                            PRERELEASE=1 publishes it as a GitHub prerelease, and
//                                            CHANNEL=stable|preview puts that channel in the signed manifest
//   remove-release DIR TAG                   take release TAG off the list
//   serve DIR PORT                           the releases API and the downloads
import crypto from 'node:crypto'
import fs from 'node:fs'
import http from 'node:http'
import path from 'node:path'

const [cmd, ...a] = process.argv.slice(2)
const readJson = f => JSON.parse(fs.readFileSync(f, 'utf8'))
const writeJson = (f, v) => fs.writeFileSync(f, JSON.stringify(v, null, 1) + '\n')

if (cmd === 'keygen') {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519')
  fs.mkdirSync(a[0], { recursive: true })
  fs.writeFileSync(path.join(a[0], 'update.key'), privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 })
  fs.writeFileSync(path.join(a[0], 'update.pub'), publicKey.export({ type: 'spki', format: 'pem' }))
} else if (cmd === 'stamp') {
  const [tree, version, pub] = a
  const info = path.join(tree, 'app', 'build-info.json')
  writeJson(info, { ...readJson(info), version })
  // The Linux payload's manifest.json; the macOS app (TREE is its
  // Contents/Resources) has one only from the Go version on.
  const man = path.join(tree, 'manifest.json')
  if (fs.existsSync(man)) writeJson(man, { ...readJson(man), version })
  fs.copyFileSync(pub, path.join(tree, 'update-signing.pub'))
} else if (cmd === 'add-release') {
  const [dir, port, tag, archive, keyFile, corrupt] = a
  const asset = path.basename(archive)
  const manifestName = asset.replace(/\.tar\.gz$/, '.manifest.json')
  const bytes = fs.readFileSync(archive)
  // minGlibc is the payload's own (its manifest.json), which the caller passes as MIN_GLIBC.
  const manifest = Buffer.from(JSON.stringify({
    version: tag, asset, sha256: crypto.createHash('sha256').update(bytes).digest('hex'), size: bytes.length,
    minGlibc: process.env.MIN_GLIBC || '2.34', builtAt: new Date().toISOString().replace(/\.\d+Z$/, 'Z'),
    ...(process.env.CHANNEL ? { channel: process.env.CHANNEL } : {}),
  }, null, 1) + '\n')
  const sig = Buffer.from(crypto.sign(null, manifest, crypto.createPrivateKey(fs.readFileSync(keyFile))).toString('base64') + '\n')
  const shipped = Buffer.from(bytes)
  if (corrupt === 'corrupt') shipped[Math.floor(shipped.length / 2)] ^= 0xff
  const files = path.join(dir, 'files', tag)
  fs.mkdirSync(files, { recursive: true })
  const assets = []
  for (const [name, body] of [[asset, shipped], [manifestName, manifest], [`${manifestName}.sig`, sig]]) {
    fs.writeFileSync(path.join(files, name), body)
    assets.push({ name, size: body.length, browser_download_url: `http://127.0.0.1:${port}/files/${tag}/${name}` })
  }
  const list = path.join(dir, 'releases.json')
  const have = fs.existsSync(list) ? readJson(list) : []
  have.push({
    tag_name: tag, draft: false, prerelease: process.env.PRERELEASE === '1', published_at: '2026-11-03T12:00:00Z',
    body: `- A stand-in release ${tag} for the update check.`, html_url: `https://example.invalid/releases/${tag}`, assets,
  })
  writeJson(list, have)
} else if (cmd === 'remove-release') {
  const [dir, tag] = a
  const list = path.join(dir, 'releases.json')
  writeJson(list, (fs.existsSync(list) ? readJson(list) : []).filter(r => r.tag_name !== tag))
} else if (cmd === 'serve') {
  const [dir, port] = a
  http.createServer((req, res) => {
    const url = new URL(req.url, 'http://x')
    if (url.pathname === '/repos/parallelworks/activate-studio/releases') {
      const list = path.join(dir, 'releases.json')
      const body = fs.existsSync(list) ? fs.readFileSync(list) : Buffer.from('[]')
      res.writeHead(200, { 'content-type': 'application/json', 'content-length': body.length })
      res.end(body)
      return
    }
    const m = /^\/files\/(v[0-9.]+)\/([A-Za-z0-9._-]+)$/.exec(url.pathname)
    const file = m && path.join(dir, 'files', m[1], m[2])
    if (!file || !fs.existsSync(file)) { res.writeHead(404); res.end(); return }
    res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-length': fs.statSync(file).size })
    fs.createReadStream(file).pipe(res)
  }).listen(Number(port), '127.0.0.1')
} else {
  console.error('usage: update_e2e.mjs keygen|stamp|add-release|remove-release|serve ...')
  process.exit(2)
}
