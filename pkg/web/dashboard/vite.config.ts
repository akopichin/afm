import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// Сборка кладёт index.html + assets/* прямо в корень dashboard/, чтобы контракт
// pkg/web/embed.go (//go:embed dashboard/* + fs.Sub) остался без правок.
// emptyOutDir: false обязателен — иначе Vite сотрёт src/, public/, конфиги.
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
  },
})
