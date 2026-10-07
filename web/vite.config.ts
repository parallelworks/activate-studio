import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
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
