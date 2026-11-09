import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { cpSync, createReadStream, existsSync, statSync } from 'node:fs'
import { dirname, join, normalize } from 'node:path'
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

export default defineConfig({
  plugins: [react(), pdfjsAssets()],
  // The parser loads parseWorkflow.wasm from beside its own module (since
  // 0.6.1). Pre-bundling moves the module into .vite/deps without the
  // file, so dev served the SPA's HTML in its place and the workflow form
  // never rendered. Production builds copy the file and are unaffected.
  optimizeDeps: { exclude: ['@parallelworks/workflow-parser'] },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:4080',
    },
  },
  build: { outDir: 'dist' },
})
