import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const apiProxy = env.OPS_WEB_API_PROXY ?? 'http://127.0.0.1:3200'
  return {
    plugins: [react()],
    server: {
      host: '127.0.0.1',
      port: 5173,
      // Local integration can proxy through the Go Web process; production
      // never uses Vite and keeps this development default unchanged.
      proxy: {
        '/api': apiProxy,
        '/healthz': apiProxy,
      },
    },
    build: {
      outDir: 'dist',
      sourcemap: false,
    },
  }
})
