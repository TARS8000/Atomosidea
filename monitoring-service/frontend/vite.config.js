import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
// 監視ダッシュボード（React）の Vite 設定。
// compose では mon-frontend-builder が `npm run build` で dist を生成するだけで、配信は mon-nginx が行う。
export default defineConfig({
  plugins: [react()],
  // 開発サーバー（`npm run dev`）用の設定。compose の通常運用では使われない。
  // proxy の設定が無いため、開発サーバー単体では /api と /ws へのリクエストはバックエンドに届かない。
  server: {
    watch: {
      // バインドマウント越しではファイル変更通知が届かない環境があるため、ポーリングで変更を検知する
      usePolling: true,
    },
    host: true, // Dockerコンテナ内でアクセス可能にする
    // strictPort: ポートが使用中なら別ポートに逃げずに起動を失敗させる
    strictPort: true,
    port: 5173, // 開発サーバーのポート
  },
  build: {
    outDir: 'dist', // ビルド出力ディレクトリ
  }
})
