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
    // Process CSS so the skins contract test can read authoritative skins/base/*.css
    // via Vite `?raw` imports (otherwise vitest stubs .css to an empty string). No
    // component imports .css directly, so this is inert for every other test.
    css: true,
  },
})
