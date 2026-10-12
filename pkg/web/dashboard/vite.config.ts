import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// Собираем assets рядом с исходниками для pkg/web/embed.go; emptyOutDir:false запрещает Vite стирать исходники.
export default defineConfig({
  plugins: [react()],
  // Относительные пути к ассетам — бандл может отдаваться с любого префикса.
  base: './',
  build: {
    outDir: '.',
    emptyOutDir: false,
    assetsDir: 'assets',
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    // Enable raw CSS imports for skin contract tests; Vitest otherwise replaces CSS with empty strings.
    css: true,
  },
})
