import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The control plane serves the API and the live stream; in development
// Vite proxies to it so the UI stays same-origin.
const controlPlane = process.env.BACKDRAFT_URL ?? 'http://127.0.0.1:7070'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': controlPlane,
      '/ws': { target: controlPlane.replace(/^http/, 'ws'), ws: true },
    },
  },
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 1600,
    rollupOptions: {
      output: {
        manualChunks: {
          three: ['three', '@react-three/fiber', '@react-three/drei'],
        },
      },
    },
  },
})
