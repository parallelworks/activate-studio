import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { cpSync, createReadStream, existsSync, readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { fillShell, shellOf } from './shell/build'
import { dirname, join, normalize, resolve } from 'node:path'
import { createRequire } from 'node:module'

// pdf.js (PdfJsPages) loads its standard fonts, character maps, colour
// profiles and image decoders from beside the app, at /pdfjs/<dir>/.
// The build copies them from pdfjs-dist; dev serves them from there.
const PDFJS_DIRS = ['cmaps', 'standard_fonts', 'iccs', 'wasm']
const pdfjsRoot = dirname(createRequire(import.meta.url).resolve('pdfjs-dist/package.json'))
function pdfjsAssets(): Plugin {
  let outDir = 'dist'
  return {
    name: 'pdfjs-assets',
    configResolved(c) { outDir = c.build.outDir },
    configureServer(server) {
      server.middlewares.use('/pdfjs/', (req, res, next) => {
        const rel = normalize(decodeURIComponent((req.url ?? '').split('?')[0])).replace(/^([/\\])+/, '')
        const file = join(pdfjsRoot, rel)
        if (!PDFJS_DIRS.includes(rel.split(/[/\\]/)[0]) || !file.startsWith(pdfjsRoot) || !existsSync(file) || !statSync(file).isFile()) return next()
        createReadStream(file).pipe(res)
      })
    },
    closeBundle() {
      cpSync(join(pdfjsRoot, 'LICENSE'), join(outDir, 'pdfjs', 'LICENSE'))
      for (const d of PDFJS_DIRS) {
        cpSync(join(pdfjsRoot, d), join(outDir, 'pdfjs', d), {
          recursive: true,
          // The scripting engine is for PDF forms' scripts, which the viewer does not run.
          filter: src => !/quickjs/.test(src),
        })
      }
    },
  }
}

// noVNC's RFB client (the desktop viewer's chunk) is MPL 2.0, with pako
// (MIT) inside it; the build puts their license texts in novnc/ beside it.
// The package exports core/rfb.js only, so its folder is two up from it.
const novncRoot = dirname(dirname(createRequire(import.meta.url).resolve('@novnc/novnc')))
function novncLicenses(): Plugin {
  let outDir = 'dist'
  return {
    name: 'novnc-licenses',
    apply: 'build',
    configResolved(c) { outDir = resolve(c.root, c.build.outDir) },
    closeBundle() {
      const files: [string, string][] = [
        ['LICENSE.txt', 'LICENSE.txt'], ['AUTHORS', 'AUTHORS'], ['docs/LICENSE.MPL-2.0', 'LICENSE.MPL-2.0'], ['vendor/pako/LICENSE', 'pako-LICENSE'],
      ]
      for (const [from, to] of files) cpSync(join(novncRoot, from), join(outDir, 'novnc', to))
    },
  }
}

/** Writes web/shell/sw.js into the build as sw.js, with this build's shell
 *  (shellOf) in place of its SHELL placeholder. */
function shellWorker(): Plugin {
  let outDir = 'dist'
  return {
    name: 'shell-worker',
    apply: 'build',
    configResolved(c) { outDir = resolve(c.root, c.build.outDir) },
    closeBundle() {
      const html = readFileSync(join(outDir, 'index.html'), 'utf8')
      const icons = readdirSync(join(__dirname, 'public')).filter(f => /\.(png|svg|ico)$/.test(f)).sort()
      const src = readFileSync(join(__dirname, 'shell', 'sw.js'), 'utf8')
      writeFileSync(join(outDir, 'sw.js'), fillShell(src, shellOf(html, icons)))
    },
  }
}

export default defineConfig({
  plugins: [react(), pdfjsAssets(), novncLicenses(), shellWorker()],
  // The parser loads parseWorkflow.wasm from beside its own module (since
  // 0.6.1). Pre-bundling moves the module into .vite/deps without the
  // file, so dev served the SPA's HTML in its place and the workflow form
  // never rendered. Production builds copy the file and are unaffected.
  // noVNC (@novnc/novnc, the desktop viewer) awaits at the top of a module
  // (its H.264 check). The engines the Studio runs in have top-level await
  // (Safari 15 and the macOS 14 window, current WebKitGTK, Chromium 89);
  // the module is in the viewer's own chunk, loaded when a desktop first
  // shows, so the rest of the app does not depend on it.
  esbuild: { supported: { 'top-level-await': true } },
  optimizeDeps: { exclude: ['@parallelworks/workflow-parser'], esbuildOptions: { supported: { 'top-level-await': true } } },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:4080',
    },
  },
  build: { outDir: 'dist' },
})
