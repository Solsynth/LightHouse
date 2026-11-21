import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

const backend = process.env.PULSAR_DEV_PROXY || 'http://localhost:5073'
const backendPaths = ['/api', '/m', '/share', '/health', '/alive']

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    cors: true,
    proxy: Object.fromEntries(backendPaths.map((p) => [p, backend])),
  },
})
