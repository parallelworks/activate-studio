import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['./test/setup.ts'],
    include: ['test/**/*.test.{ts,tsx}'],
    // Node 25+ defines its own localStorage global, unusable without
    // --localstorage-file, and it shadows jsdom's.
    execArgv: ['--no-experimental-webstorage'],
  },
})
