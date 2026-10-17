import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueJsx from '@vitejs/plugin-vue-jsx'
import vueDevTools from 'vite-plugin-vue-devtools'

// In development the SPA runs on the Vite server and every backend path is
// proxied to Stargate, so the app uses same-origin relative URLs in both
// modes (in production Stargate serves web/dist itself).
const backend = process.env.STARGATE_DEV_PROXY || 'http://localhost:8080'
const backendPaths = ['/api', '/.well-known', '/swagger', '/health', '/alive']

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), vueJsx(), vueDevTools()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: Object.fromEntries(backendPaths.map((path) => [path, { target: backend, changeOrigin: false }])),
  },
})
