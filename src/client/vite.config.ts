import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    // Keep the bundle predictable for the embedded asset server.
    target: 'es2020',
    sourcemap: false,
  },
})
