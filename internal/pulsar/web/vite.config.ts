import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// In development, Pulsar's Go SSR server can point web.devViteUrl at this Vite
// server for HMR, or Vite can proxy /api and SSR routes directly.
const backend = process.env.PULSAR_DEV_PROXY || 'http://localhost:5073'
const backendPaths = ['/api', '/m', '/share', '/u', '/c', '/history', '/collections', '/settings', '/rewind', '/health', '/alive']

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  build: {
    manifest: true,
    rollupOptions: {
      input: 'src/main.ts',
    },
  },
  server: {
    cors: true,
    proxy: Object.fromEntries(backendPaths.map((p) => [p, backend])),
  },
})
