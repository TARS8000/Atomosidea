import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    watch: {
      usePolling: true,
    },
    host: true, // Dockerコンテナ内でアクセス可能にする
    strictPort: true,
    port: 5173, // 開発サーバーのポート
  },
  build: {
    outDir: 'dist', // ビルド出力ディレクトリ
  }
})
