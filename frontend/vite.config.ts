import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  // The `base` option is removed. The default '/' is the correct setting for
  // most single-page applications that use client-side routing.
  server: {
    port: 3000,
  }
})
